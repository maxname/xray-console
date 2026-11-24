package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"xray-console/internal/acmesh"
	"xray-console/internal/ops"
	"xray-console/internal/servers"
	"xray-console/internal/xray"
)

const (
	ownDomain   = "cdn.example.net"
	otherDomain = "edge.example.net"
)

// stubbed prepares the unit for jobs that deal with a stub site: acme.sh
// comes from the test, the domains lead to the server, and the server
// answers like one on which the stub can be set up — its ports free, no nginx
// of anyone's, an authority that issues, an nginx that serves. extra goes
// first and can make any of that go wrong.
func (u *unit) stubbed(extra func(script string) (string, error, bool)) {
	u.t.Helper()
	script := []byte("#!/bin/sh\n# acme.sh, as far as a test needs one\n")
	sum := sha256.Sum256(script)
	u.installer.UseACMEScript(func(context.Context) (*acmesh.Script, error) {
		return &acmesh.Script{Version: "test", Data: script, SHA256: hex.EncodeToString(sum[:])}, nil
	})
	u.installer.resolve = func(context.Context, string) ([]string, error) {
		return []string{u.server.Target().Host}, nil
	}
	u.server.respond = func(script string) (string, error, bool) {
		if extra != nil {
			if out, err, handled := extra(script); handled {
				return out, err, true
			}
		}
		switch {
		case strings.Contains(script, "curl -sS -k"):
			return "200", nil, true
		case strings.Contains(script, "openssl x509"):
			return "notAfter=Jan 18 12:00:00 2027 GMT\n", nil, true
		}
		return "", nil, false
	}
}

// wants edits the server's settings: its own domain, or — for "" — someone
// else's site again.
func (u *unit) wants(domain string) {
	u.t.Helper()
	ctx := context.Background()
	srv, err := u.servers.Get(ctx, u.srv.ID)
	if err != nil {
		u.t.Fatal(err)
	}
	inbound := srv.Inbound.Clone()
	if domain == "" {
		err = inbound.SetMaskHost("www.example.com")
	} else {
		err = inbound.SetDomain(domain)
	}
	if err != nil {
		u.t.Fatal(err)
	}
	if err := u.servers.SetInbound(ctx, u.srv.ID, inbound); err != nil {
		u.t.Fatal(err)
	}
}

// runs makes the server one that already masquerades as this domain: applied,
// with the stub in its manifest.
func (u *unit) runs(domain string) {
	u.t.Helper()
	ctx := context.Background()
	u.wants(domain)
	srv, _ := u.servers.Get(ctx, u.srv.ID)
	// Applied without the override the unit starts with, so that there is
	// still something to apply.
	overrides := srv.Overrides
	srv.Overrides = map[string]string{}
	p, err := u.installer.Planner.For(ctx, srv)
	if err != nil {
		u.t.Fatal(err)
	}
	if err := u.servers.SetApplied(ctx, srv.ID, p.Snapshot(srv, time.Now())); err != nil {
		u.t.Fatal(err)
	}
	srv.Overrides = overrides
	for _, change := range []servers.Change{
		{Kind: changePackages, Text: "nginx (apt)"},
		{Kind: changeSite, Text: siteConfPath},
		{Kind: changeSite, Text: siteRootDir + "/"},
	} {
		if err := u.servers.AddChange(ctx, srv.ID, change); err != nil {
			u.t.Fatal(err)
		}
	}
	u.srv, _ = u.servers.Get(ctx, srv.ID)
}

func (u *unit) has(kind string) bool {
	u.t.Helper()
	srv, err := u.servers.Get(context.Background(), u.srv.ID)
	if err != nil {
		u.t.Fatal(err)
	}
	return hasChange(srv, kind)
}

// at is where in the order of things a script with these fragments ran; the
// test fails if none did.
func (u *unit) at(fragments ...string) int {
	u.t.Helper()
	index, ran := u.server.did(fragments...)
	if !ran {
		u.t.Fatalf("nothing like %q was run:\n%s", fragments, strings.Join(u.server.ran, "\n---\n"))
	}
	return index
}

func (u *unit) never(fragments ...string) {
	u.t.Helper()
	if index, ran := u.server.did(fragments...); ran {
		u.t.Fatalf("%q was run:\n%s", fragments, u.server.ran[index])
	}
}

// What the stub's scripts look like, for finding them among everything a job
// ran.
var (
	installsNginx = []string{"apt-get", "install nginx"}
	removesStub   = []string{"purge $pkgs", "rm -rf " + siteRootDir}
	swapsConfig   = []string{"mv -f " + candidatePath}
	restoresXray  = []string{"cp -p " + BackupDir, "systemctl restart " + xray.ServiceName}
)

func issues(domain string) []string { return []string{"'--issue'", "'" + domain + "'"} }

func TestTheStubIsSetUpBeforeXrayIsPointedAtItAndGoesWhenTheApplyFails(t *testing.T) {
	u := newUnit(t)
	u.stubbed(nil)
	u.wants(ownDomain)

	op := u.apply()
	// Everything the stub needs worked; what fails is the fake server's
	// check, after the swap.
	if restored := u.failed(op, CodeCheckFailed, StepVerify); restored != true {
		t.Fatalf("restored = %v", restored)
	}
	if len(op.Steps) != 4 || op.Steps[0].ID != StepSite || op.Steps[0].Status != ops.StepDone {
		t.Fatalf("steps: %+v", op.Steps)
	}
	packages, issue, swap := u.at(installsNginx...), u.at(issues(ownDomain)...), u.at(swapsConfig...)
	if !(packages < issue && issue < swap) {
		t.Fatalf("Xray was pointed at the stub before it was there: nginx %d, certificate %d, swap %d", packages, issue, swap)
	}
	// The way back, in the order that never leaves Xray pointing at a stub
	// that is gone: its configuration first.
	restore, removal := u.at(restoresXray...), u.at(removesStub...)
	if !(swap < restore && restore < removal) {
		t.Fatalf("swap %d, configuration restored %d, stub removed %d", swap, restore, removal)
	}
	if !strings.HasPrefix(u.server.ran[removal], "set -e\n") || !strings.HasPrefix(u.server.ran[packages], "set -e\n") {
		t.Fatal("a script that changes the server does not stop at its first failure")
	}
	if u.has(changeSite) || u.has(changePackages) {
		t.Fatal("a stub that was removed is still in the manifest")
	}
	u.unchanged()
}

func TestTheStubTalksToNobodyButTheLoopbackAndTheAuthority(t *testing.T) {
	u := newUnit(t)
	u.stubbed(nil)
	u.wants(ownDomain)
	u.apply()

	// The first configuration has no certificate to name: nginx would not
	// start. It was replaced by the one with TLS once there was one.
	conf := string(u.server.files[siteConfPath])
	for _, line := range []string{
		"listen " + xray.StubListen + " ssl http2;",
		"server_name " + ownDomain + ";",
		"ssl_certificate " + siteCertDir + "/" + ownDomain + "/fullchain.pem;",
		"root " + siteACMEDir + ";",
		"return 301 https://" + ownDomain + "$request_uri;",
	} {
		if !strings.Contains(conf, line) {
			t.Fatalf("nginx's configuration lacks %q:\n%s", line, conf)
		}
	}
	if strings.Contains(conf, "listen 443") || strings.Contains(conf, "listen 0.0.0.0:8443") {
		t.Fatalf("the stub listens where strangers reach it directly:\n%s", conf)
	}
	issue := u.server.ran[u.at(issues(ownDomain)...)]
	for _, arg := range []string{"'--server' 'letsencrypt'", "'-w' '" + siteACMEDir + "'", "--home " + acmeHome} {
		if !strings.Contains(issue, arg) {
			t.Fatalf("acme.sh was not told %s:\n%s", arg, issue)
		}
	}
	// The page says nothing about what the server is for.
	page := strings.ToLower(string(u.server.files[siteHTMLDir+"/index.html"]))
	if !strings.Contains(page, "<title>"+ownDomain+"</title>") {
		t.Fatalf("the stub's page:\n%s", page)
	}
	for _, word := range []string{"xray", "vless", "reality", "console", "proxy", "vpn", "http://", "src="} {
		if strings.Contains(page, word) {
			t.Fatalf("the stub's page mentions %q", word)
		}
	}
	// acme.sh arrived whole, and is the panel's.
	if _, ran := u.server.did("curl", "get.acme.sh"); ran {
		t.Fatal("the server was told to fetch acme.sh itself")
	}
	if string(u.server.files[acmePath]) == "" {
		t.Fatal("acme.sh was not uploaded")
	}
}

func TestACertificateThatIsNotIssuedLeavesTheServerAsItWas(t *testing.T) {
	for said, code := range map[string]string{
		"Verify error: 198.51.100.200: Invalid response from http://cdn.example.net/.well-known/acme-challenge/x\nurn:ietf:params:acme:error:unauthorized": CodeCertFailed,
		`{"type":"urn:ietf:params:acme:error:rateLimited","detail":"too many certificates already issued"}`:                                                CodeCertRateLimited,
	} {
		u := newUnit(t)
		u.stubbed(func(script string) (string, error, bool) {
			if strings.Contains(script, "'--issue'") {
				return said, refuse(said), true
			}
			return "", nil, false
		})
		u.wants(ownDomain)

		op := u.apply()
		if restored := u.failed(op, code, StepSite); restored != true {
			t.Fatalf("restored = %v", restored)
		}
		// The authority's own words are what the administrator needs.
		if out, _ := op.Error.Details["output"].(string); !strings.Contains(out, "urn:ietf:params:acme:error") {
			t.Fatalf("details: %v", op.Error.Details)
		}
		u.never(swapsConfig...)
		u.never("systemctl restart " + xray.ServiceName)
		u.at(removesStub...)
		if u.has(changeSite) || u.has(changePackages) {
			t.Fatal("the manifest still lists a stub that was taken away")
		}
		u.unchanged()
	}
}

func TestSomeoneElsesNginxOrABusyPortStopsTheStubBeforeAnythingIsChanged(t *testing.T) {
	cases := map[string]func(script string) (string, error, bool){
		CodeNginxExists: func(script string) (string, error, bool) {
			if strings.Contains(script, "command -v nginx") {
				return "present\n", nil, true
			}
			return "", nil, false
		},
		CodePortInUse: func(script string) (string, error, bool) {
			if strings.Contains(script, "'sport = :80'") {
				return `LISTEN 0 511 0.0.0.0:80 0.0.0.0:* users:(("apache2",pid=812,fd=4))` + "\n", nil, true
			}
			return "", nil, false
		},
	}
	for code, respond := range cases {
		u := newUnit(t)
		u.stubbed(respond)
		u.wants(ownDomain)

		op := u.apply()
		if restored := u.failed(op, code, StepSite); restored != true {
			t.Fatalf("%s: restored = %v", code, restored)
		}
		if code == CodePortInUse {
			if op.Error.Details["port"] != float64(80) || op.Error.Details["process"] != "apache2" {
				t.Fatalf("details: %v", op.Error.Details)
			}
		}
		// Nothing was installed, so there is nothing to take away either —
		// least of all the nginx that was there.
		u.never("apt-get")
		u.never("purge")
		u.never("rm -rf " + siteRootDir)
		if u.has(changeSite) || u.has(changePackages) {
			t.Fatalf("%s: the manifest claims a stub that was never set up", code)
		}
		u.unchanged()
	}
}

func TestAStubThatCannotBeTakenAwayAgainIsSaidSo(t *testing.T) {
	u := newUnit(t)
	u.stubbed(func(script string) (string, error, bool) {
		switch {
		case strings.Contains(script, "'--issue'"):
			return "Timeout during connect", refuse("Timeout during connect"), true
		case strings.Contains(script, "rm -rf "+siteRootDir):
			return "E: dpkg was interrupted", refuse("E: dpkg was interrupted"), true
		}
		return "", nil, false
	})
	u.wants(ownDomain)

	op := u.apply()
	if restored := u.failed(op, CodeCertFailed, StepSite); restored != false {
		t.Fatalf("restored = %v, though the stub could not be removed", restored)
	}
	// Still listed: the removal of Xray, or the next apply, finds it there.
	if !u.has(changeSite) || !u.has(changePackages) {
		t.Fatal("a stub that is still on the server was struck off the manifest")
	}
	u.unchanged()
}

func TestMovingToAnotherDomainKeepsThePreviousOneUntilTheChangeIsChecked(t *testing.T) {
	u := newUnit(t)
	u.stubbed(nil)
	u.runs(ownDomain)
	u.wants(otherDomain)

	op := u.apply()
	if restored := u.failed(op, CodeCheckFailed, StepVerify); restored != true {
		t.Fatalf("restored = %v", restored)
	}
	// nginx is there already, and is not the business of this apply.
	u.never(installsNginx...)
	u.never("purge")
	issue, swap := u.at(issues(otherDomain)...), u.at(swapsConfig...)
	if issue > swap {
		t.Fatal("Xray was told the new name before there was a certificate for it")
	}
	// Back on the previous name, whose certificate was never touched.
	if conf := string(u.server.files[siteConfPath]); !strings.Contains(conf, "server_name "+ownDomain+";") {
		t.Fatalf("the stub was not put back on %s:\n%s", ownDomain, conf)
	}
	drop := u.at("'--remove'", "'"+otherDomain+"'")
	if drop < u.at(restoresXray...) {
		t.Fatal("the new certificate was dropped before Xray was back on the previous name")
	}
	u.never("'--remove'", "'"+ownDomain+"'")
	if !u.has(changeSite) || !u.has(changePackages) {
		t.Fatal("the stub is no longer in the manifest")
	}
}

func TestGivingADomainUpDoesNotTouchTheStubUntilTheNewSettingsWork(t *testing.T) {
	u := newUnit(t)
	u.stubbed(nil)
	u.runs(ownDomain)
	u.wants("")

	op := u.apply()
	if restored := u.failed(op, CodeCheckFailed, StepVerify); restored != true {
		t.Fatalf("restored = %v", restored)
	}
	if len(op.Steps) != 3 {
		t.Fatalf("steps: %+v", op.Steps)
	}
	// Xray is back on a configuration that sends strangers to the stub, so
	// the stub has to be exactly as it was.
	u.never("purge")
	u.never("rm -rf " + siteRootDir)
	if _, written := u.server.files[siteConfPath]; written {
		t.Fatal("nginx's configuration was rewritten")
	}
	if !u.has(changeSite) {
		t.Fatal("the stub is no longer in the manifest")
	}
}

// fresh adds a server with nothing on it and answers the installer's
// questions about the system the way a clean Debian does.
func (u *unit) fresh(extra func(script string) (string, error, bool)) *servers.Server {
	u.t.Helper()
	u.release(newVersion)
	u.stubbed(func(script string) (string, error, bool) {
		if extra != nil {
			if out, err, handled := extra(script); handled {
				return out, err, true
			}
		}
		if strings.Contains(script, "os_id=") {
			return fmt.Sprintf("uid=0\nroot=yes\nos_id=debian\nos_version=12\nos_name=Debian GNU/Linux 12\narch=x86_64\n"+
				"systemd=252\ntime=%d\ndisk_kb=9000000\nxray=absent\n", time.Now().Unix()), nil, true
		}
		return "", nil, false
	})
	srv, err := u.servers.Create(context.Background(), servers.NewServer{
		Name: "new-1", Host: u.server.Target().Host, SSHPort: 22, SSHUser: "root", HostKey: "k",
	})
	if err != nil {
		u.t.Fatal(err)
	}
	u.srv = srv
	return srv
}

func (u *unit) install(params Params) *ops.Operation {
	u.t.Helper()
	params.Auth = AuthPanelKey
	return u.finished(u.installer.Start(context.Background(), u.srv.ID, params, ""))
}

func TestAnInstallationWithADomainHasTheStubAsAStepAndARollbackTakesItAway(t *testing.T) {
	u := newUnit(t)
	u.fresh(nil)

	op := u.install(Params{Domain: "https://CDN.example.net/"})
	// As far as a fake server goes: everything up to the connection check.
	if op.Status != ops.StatusFailed || op.Error.Code != CodeCheckFailed || op.Error.Step != StepCheck {
		t.Fatalf("status %s, error %+v", op.Status, op.Error)
	}
	var ids []string
	for _, step := range op.Steps {
		ids = append(ids, step.ID)
	}
	if got := strings.Join(ids, " "); got != "ssh system xray keys config service firewall site check" {
		t.Fatalf("steps: %s", got)
	}
	if detail := op.Steps[7].Detail; detail != ownDomain+" · HTTP-01 · until 2027-01-18" {
		t.Fatalf("the stub's step says %q", detail)
	}
	if until, _ := op.Result["cert_not_after"].(float64); int64(until) != time.Date(2027, 1, 18, 12, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("result: %v", op.Result)
	}
	srv, _ := u.servers.Get(context.Background(), u.srv.ID)
	if srv.Inbound.Domain != ownDomain || srv.Inbound.Dest != xray.StubListen || srv.Inbound.MaskHost() != ownDomain {
		t.Fatalf("inbound: %+v", srv.Inbound)
	}
	// Whether the name leads here and whether the stub fits were asked
	// before anything was put on the server.
	if asked, created := u.at("command -v nginx"), u.at("useradd"); asked > created {
		t.Fatal("the server was changed before it was known that the stub fits on it")
	}
	// A server's own domain is not looked up on the server: the stub is on
	// its loopback.
	u.never("getent hosts")
	if !u.has(changeSite) || !u.has(changePackages) {
		t.Fatal("the stub is not in the manifest")
	}

	if err := u.installer.Rollback(context.Background(), op.ID); err != nil {
		t.Fatal(err)
	}
	rolled := u.finished(op, nil)
	if rolled.Status != ops.StatusRolledBack {
		t.Fatalf("rollback: status %s, error %+v", rolled.Status, rolled.Error)
	}
	// In reverse: the stub goes before Xray, which goes before the key.
	if stub, binary := u.at(removesStub...), u.at("rm -f "+xray.BinaryPath); stub > binary {
		t.Fatal("the stub was removed after Xray")
	}
	srv, _ = u.servers.Get(context.Background(), u.srv.ID)
	if len(srv.Manifest) != 0 || srv.Status != servers.StatusNew {
		t.Fatalf("after the rollback: status %s, manifest %v", srv.Status, srv.Manifest)
	}
}

func TestADomainThatDoesNotLeadToTheServerStopsTheInstallationAtTheStart(t *testing.T) {
	for code, found := range map[string][]string{
		CodeDomainMismatch:   {"198.51.100.200"},
		CodeDomainUnresolved: nil,
	} {
		u := newUnit(t)
		srv := u.fresh(nil)
		u.installer.resolve = func(context.Context, string) ([]string, error) { return found, nil }

		op := u.install(Params{Domain: ownDomain})
		if op.Status != ops.StatusFailed || op.Error.Code != code || op.Error.Step != StepSystem {
			t.Fatalf("status %s, error %+v", op.Status, op.Error)
		}
		if op.Error.Details["domain"] != ownDomain || op.Error.Details["address"] != srv.Host {
			t.Fatalf("details: %v", op.Error.Details)
		}
		u.never("useradd")
		u.never("apt-get")
	}

	// An address of the domain that is the server's own — an IPv6 one the
	// panel was not given — is not a mismatch.
	u := newUnit(t)
	u.fresh(func(script string) (string, error, bool) {
		if strings.Contains(script, "ip -o addr show scope global") {
			return "192.0.2.10/24\n2001:db8::10/64\n", nil, true
		}
		return "", nil, false
	})
	u.installer.resolve = func(context.Context, string) ([]string, error) {
		return []string{"192.0.2.10", "2001:db8:0:0::10"}, nil
	}
	if op := u.install(Params{Domain: ownDomain}); op.Error.Step != StepCheck {
		t.Fatalf("the server's own IPv6 address was read as someone else's: %+v", op.Error)
	}
}

func TestAnInstallationCanGoOnWithoutTheDomain(t *testing.T) {
	u := newUnit(t)
	u.fresh(func(script string) (string, error, bool) {
		if strings.Contains(script, "'--issue'") {
			return "urn:ietf:params:acme:error:unauthorized", refuse("urn:ietf:params:acme:error:unauthorized"), true
		}
		return "", nil, false
	})
	ctx := context.Background()

	op := u.install(Params{Domain: ownDomain})
	if op.Status != ops.StatusFailed || op.Error.Code != CodeCertFailed || op.Error.Step != StepSite {
		t.Fatalf("status %s, error %+v", op.Status, op.Error)
	}
	// Only an installation that has a domain can go on without one, and only
	// to a site that can be one.
	if err := u.installer.WithoutDomain(ctx, op.ID, "not a host", ""); !errors.Is(err, xray.ErrInvalidMaskHost) {
		t.Fatalf("a bad masking site: %v", err)
	}
	keys, _ := u.servers.Get(ctx, u.srv.ID)

	if err := u.installer.WithoutDomain(ctx, op.ID, "www.example.org", ""); err != nil {
		t.Fatal(err)
	}
	op = u.finished(op, nil)
	// The stub's step did what was left for it to do; the fake server's
	// check is what stops the job now.
	if op.Error == nil || op.Error.Step != StepCheck || op.Steps[7].Status != ops.StepDone {
		t.Fatalf("status %s, error %+v, steps %+v", op.Status, op.Error, op.Steps)
	}
	if detail := op.Steps[7].Detail; detail != "without a domain · www.example.org" {
		t.Fatalf("the stub's step says %q", detail)
	}
	var params Params
	if err := json.Unmarshal(op.Params, &params); err != nil || params.Domain != "" || params.MaskHost != "www.example.org" {
		t.Fatalf("the job still remembers the domain: %s", op.Params)
	}
	srv, _ := u.servers.Get(ctx, u.srv.ID)
	if srv.Inbound.Domain != "" || srv.Inbound.Dest != "www.example.org:443" || srv.Inbound.PublicKey != keys.Inbound.PublicKey {
		t.Fatalf("inbound: %+v", srv.Inbound)
	}
	// What was set up for the domain is gone, and Xray was given a
	// configuration that no longer points at it — judged by Xray first.
	removal := u.at(removesStub...)
	test, lookup := u.at("getent hosts 'www.example.org'"), u.at("mv -f "+candidatePath, "systemctl restart "+xray.ServiceName)
	if !(removal < test && test < lookup) {
		t.Fatalf("stub removed %d, new configuration judged %d, swapped in %d", removal, test, lookup)
	}
	if !strings.Contains(string(u.server.files[candidatePath]), `"dest": "www.example.org:443"`) {
		t.Fatalf("the configuration put on the server:\n%s", u.server.files[candidatePath])
	}
	if u.has(changeSite) || u.has(changePackages) {
		t.Fatal("the manifest still lists the stub")
	}

	if err := u.installer.WithoutDomain(ctx, op.ID, "www.example.org", ""); !errors.Is(err, ErrNoDomainToDrop) {
		t.Fatalf("going on without a domain twice: %v", err)
	}
}

func TestRemovingXrayTakesTheStubToo(t *testing.T) {
	u := newUnit(t)
	// The first time round apt is in the middle of something else.
	locked := true
	u.stubbed(func(script string) (string, error, bool) {
		if locked && strings.Contains(script, "purge $pkgs") {
			return "E: Could not get lock /var/lib/dpkg/lock-frontend", refuse("E: Could not get lock"), true
		}
		return "", nil, false
	})
	u.runs(ownDomain)
	ctx := context.Background()

	op := u.finished(u.installer.Uninstall(ctx, u.srv.ID, UninstallParams{}))
	if op.Status != ops.StatusFailed || op.Error.Step != StepSite {
		t.Fatalf("status %s, error %+v", op.Status, op.Error)
	}
	var ids []string
	for _, step := range op.Steps {
		ids = append(ids, step.ID)
	}
	if got := strings.Join(ids, " "); got != "stop files site ports access" {
		t.Fatalf("steps: %s", got)
	}
	if xrayGone, stubGone := u.at("rm -f "+xray.BinaryPath), u.at(removesStub...); stubGone < xrayGone {
		t.Fatal("the stub was removed while Xray could still send it visitors")
	}
	// Not struck off: it is still there, and the removal can be resumed.
	if srv, _ := u.servers.Get(ctx, u.srv.ID); srv.Status != servers.StatusUninstalling || !hasChange(srv, changeSite) {
		t.Fatalf("after a removal that stopped at the stub: %+v", srv)
	}

	locked = false
	if err := u.installer.RetryUninstall(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	u.manager.Wait()
	if _, err := u.servers.Get(ctx, u.srv.ID); !errors.Is(err, servers.ErrNotFound) {
		t.Fatalf("the server is still in the panel: %v", err)
	}

	// A server without a stub is removed in the four steps it always was.
	if got := strings.Join(uninstallOrder(false), " "); got != "stop files ports access" {
		t.Fatalf("steps of a removal without a stub: %s", got)
	}
	u = newUnit(t)
	if _, err := u.installer.Uninstall(ctx, u.srv.ID, UninstallParams{}); err != nil {
		t.Fatal(err)
	}
	u.manager.Wait()
	u.never("purge")
	u.never("nginx")
}

func TestAnUpdateOfXrayDoesNotTouchTheStub(t *testing.T) {
	u := newUnit(t)
	u.stubbed(nil)
	u.runs(ownDomain)
	u.update()
	for _, script := range u.server.ran {
		for _, word := range []string{"nginx", "acme.sh", siteRootDir, siteEtcDir, "apt-get"} {
			if strings.Contains(script, word) {
				t.Fatalf("an update of Xray ran something about the stub:\n%s", script)
			}
		}
	}
}

func TestRenewingByHand(t *testing.T) {
	u := newUnit(t)
	u.stubbed(nil)
	ctx := context.Background()
	if _, err := u.installer.RenewCert(ctx, u.srv.ID); !errors.Is(err, ErrNoDomain) {
		t.Fatalf("renewing on a server without a domain: %v", err)
	}
	// A domain that is still waiting to be applied has no certificate yet.
	u.wants(ownDomain)
	if _, err := u.installer.RenewCert(ctx, u.srv.ID); !errors.Is(err, ErrNoDomain) {
		t.Fatalf("renewing for a domain that is not applied: %v", err)
	}

	u = newUnit(t)
	u.stubbed(nil)
	u.runs(ownDomain)
	var told []int64
	u.installer.CertChanged = func(id int64) { told = append(told, id) }
	renewal, err := u.installer.RenewCert(ctx, u.srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if renewal.Domain != ownDomain || renewal.NotAfter != time.Date(2027, 1, 18, 12, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("renewal: %+v", renewal)
	}
	u.at("'--renew'", "'"+ownDomain+"'", "'--force'")
	if len(told) != 1 || told[0] != u.srv.ID {
		t.Fatalf("nobody was told to look at the certificate again: %v", told)
	}

	u = newUnit(t)
	u.stubbed(func(script string) (string, error, bool) {
		if strings.Contains(script, "'--renew'") {
			return "urn:ietf:params:acme:error:rateLimited", refuse("urn:ietf:params:acme:error:rateLimited"), true
		}
		return "", nil, false
	})
	u.runs(ownDomain)
	var failure *ops.Failure
	if _, err := u.installer.RenewCert(ctx, u.srv.ID); !errors.As(err, &failure) || failure.Code != CodeCertRateLimited {
		t.Fatalf("a renewal the authority refused: %v", err)
	}
}

func TestWhereADomainLeads(t *testing.T) {
	u := newUnit(t)
	ctx := context.Background()
	answers := map[string][]string{
		"here.example.net":  {"192.0.2.10"},
		"both.example.net":  {"192.0.2.10", "2001:db8::10"},
		"away.example.net":  {"198.51.100.200"},
		"panel.example.net": {"192.0.2.10"},
	}
	u.installer.resolve = func(_ context.Context, host string) ([]string, error) { return answers[host], nil }

	for domain, want := range map[string]struct {
		match bool
		other int
	}{
		"here.example.net": {true, 0},
		// The server's IPv4 is among them; what the other address is, only
		// the server can say.
		"both.example.net": {true, 1},
		"away.example.net": {false, 1},
		"none.example.net": {false, 0},
	} {
		check, err := u.installer.CheckDomain(ctx, "https://"+strings.ToUpper(domain)+"/", "192.0.2.10", nil)
		if err != nil {
			t.Fatal(err)
		}
		if check.Domain != domain || check.Match != want.match || len(check.Other) != want.other {
			t.Errorf("%s: %+v", domain, check)
		}
	}
	// A server that was added by name is wherever that name leads.
	if check, err := u.installer.CheckDomain(ctx, "here.example.net", "panel.example.net", nil); err != nil || !check.Match {
		t.Fatalf("a server known by name: %+v %v", check, err)
	}
	if _, err := u.installer.CheckDomain(ctx, "192.0.2.10", "192.0.2.10", nil); !errors.Is(err, xray.ErrInvalidDomain) {
		t.Fatalf("an address given as a domain: %v", err)
	}
	u.installer.resolve = func(context.Context, string) ([]string, error) { return nil, errors.New("no resolver") }
	if _, err := u.installer.CheckDomain(ctx, "here.example.net", "192.0.2.10", nil); err == nil {
		t.Fatal("a resolver that does not answer was read as a domain without an address")
	}
}
