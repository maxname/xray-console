//go:build integration

// A server that masquerades as its own domain, against a real node and a real
// — if test — certificate authority. Start both with test/domain/run.sh:
//
//	env $(test/domain/env.sh) go test -tags integration -run OwnDomain ./internal/install/ -v
package install

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xray-console/internal/acmesh"
	"xray-console/internal/certs"
	"xray-console/internal/ops"
	"xray-console/internal/plan"
	"xray-console/internal/servers"
	"xray-console/internal/sshx"
	"xray-console/internal/templates"
)

const (
	testDomain   = "stub.xc-test.example"
	secondDomain = "other.xc-test.example"
	// awayDomain leads to another machine: the authority's check of it fails.
	awayDomain = "elsewhere.xc-test.example"
)

// pebbleRoots asks Pebble for the root it signs certificates with. It makes a
// new one every time it starts.
func pebbleRoots(t *testing.T, pebble string, apiCA []byte) *x509.CertPool {
	t.Helper()
	trusted := x509.NewCertPool()
	if !trusted.AppendCertsFromPEM(apiCA) {
		t.Fatal("XC_TEST_ACME_CA holds no certificate")
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		// Pebble's own certificate is for the name, not for the address.
		TLSClientConfig: &tls.Config{RootCAs: trusted, ServerName: "pebble"},
	}}
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		resp, err := client.Get("https://" + net.JoinHostPort(pebble, "15000") + "/roots/0")
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(body) {
			t.Fatalf("Pebble's root is not a certificate: %s", body)
		}
		return roots
	}
	t.Fatalf("Pebble does not answer: %v", lastErr)
	return nil
}

// sshxDialPassword signs in to the node with the password, as someone with no
// panel would.
func sshxDialPassword(ctx context.Context, r *rig) (*sshx.Client, error) {
	hostKey, err := sshx.FetchHostKey(ctx, r.target)
	if err != nil {
		return nil, err
	}
	return sshx.Dial(ctx, r.target, sshx.Auth{Password: r.password}, hostKey.Authorized)
}

func TestIntegrationOwnDomain(t *testing.T) {
	r := newRigAt(t, "XC_TEST_DOMAIN_NODE")
	pebble := os.Getenv("XC_TEST_PEBBLE")
	apiCA, err := os.ReadFile(os.Getenv("XC_TEST_ACME_CA"))
	if pebble == "" || err != nil {
		t.Skipf("XC_TEST_PEBBLE and XC_TEST_ACME_CA are not set (%v)", err)
	}
	ctx := context.Background()
	roots := pebbleRoots(t, pebble, apiCA)
	r.installer.UseACME(ACME{Directory: "https://pebble:14000/dir", CABundle: apiCA, Roots: roots})
	r.installer.UseACMEScript(acmesh.New(filepath.Join(os.TempDir(), "xc-test-acme-cache")).Fetch)
	// The names exist only on the test's Docker network; to the panel they
	// lead where the node is.
	r.installer.resolve = func(ctx context.Context, host string) ([]string, error) {
		switch host {
		case testDomain, secondDomain, awayDomain:
			return []string{r.target.Host}, nil
		}
		return resolveHost(ctx, host)
	}

	var serverID int64
	remote := func(script string) string {
		t.Helper()
		s := &session{inst: r.installer, serverID: serverID}
		defer s.close()
		out, err := s.quietSudo(ctx, script)
		if err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
		return strings.TrimSpace(out)
	}
	manifest := func(kind string) bool {
		t.Helper()
		srv, err := r.servers.Get(ctx, serverID)
		if err != nil {
			t.Fatal(err)
		}
		return hasChange(srv, kind)
	}
	// stub requires the stub site to be there, serving this domain to
	// strangers who come through port 443, with a certificate the test
	// authority issued for it — or, for "", not to be there at all.
	stub := func(domain string) {
		t.Helper()
		installed := remote("command -v nginx >/dev/null 2>&1 && echo yes || echo no")
		if domain == "" {
			if installed != "no" || manifest(changeSite) || manifest(changePackages) {
				t.Fatalf("the stub site was left behind: nginx installed=%s, manifest site=%v packages=%v",
					installed, manifest(changeSite), manifest(changePackages))
			}
			if left := remote("ls -d " + siteRootDir + " " + siteEtcDir + " " + siteLibDir + " " + siteConfPath + " 2>/dev/null || true"); left != "" {
				t.Fatalf("the stub's files were left behind: %s", left)
			}
			if timer := remote("systemctl is-enabled " + renewTimer + " 2>/dev/null || true"); timer == "enabled" {
				t.Fatal("the renewal timer was left behind")
			}
			return
		}
		if installed != "yes" || !manifest(changeSite) || !manifest(changePackages) {
			t.Fatalf("no stub site: nginx installed=%s, manifest site=%v packages=%v",
				installed, manifest(changeSite), manifest(changePackages))
		}
		srv, _ := r.servers.Get(ctx, serverID)
		leaf, err := certs.Read(ctx, net.JoinHostPort(srv.Host, "443"), domain, roots)
		if err != nil {
			t.Fatalf("a stranger asking for %s is shown nothing: %v", domain, err)
		}
		if leaf.Problem != "" {
			t.Fatalf("a stranger asking for %s is shown a certificate they would refuse: %s", domain, leaf.Problem)
		}
		page := remote("curl -sS -k --resolve " + domain + ":8443:127.0.0.1 https://" + domain + ":8443/")
		if !strings.Contains(page, "<h1>"+domain+"</h1>") {
			t.Fatalf("the stub does not show %s's page:\n%s", domain, page)
		}
		// Port 80 answers the authority's check and sends everyone else on.
		if status := remote("curl -sS -o /dev/null -w '%{http_code} %{redirect_url}' http://127.0.0.1/any"); status != "301 https://"+domain+"/any" {
			t.Fatalf("port 80 answered %q", status)
		}
		if timer := remote("systemctl is-enabled " + renewTimer); timer != "enabled" {
			t.Fatalf("the renewal timer is %s", timer)
		}
	}
	setDomain := func(domain string) {
		t.Helper()
		srv, _ := r.servers.Get(ctx, serverID)
		inbound := srv.Inbound.Clone()
		var err error
		if domain == "" {
			err = inbound.SetMaskHost("www.microsoft.com")
		} else {
			err = inbound.SetDomain(domain)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := r.servers.SetInbound(ctx, serverID, inbound); err != nil {
			t.Fatal(err)
		}
	}
	apply := func() *ops.Operation {
		t.Helper()
		op, err := r.installer.Apply(ctx, serverID)
		if err != nil {
			t.Fatal(err)
		}
		return r.settle(op.ID)
	}
	succeeded := func(op *ops.Operation, what string) {
		t.Helper()
		if op.Status != ops.StatusSucceeded {
			t.Fatalf("%s: status %s, error %+v", what, op.Status, op.Error)
		}
	}

	// 1. An installation that stops at the certificate — the name leads to
	//    another machine — and is gone on with as one without a domain.
	srv := r.addServer("node-domain")
	serverID = srv.ID
	op, err := r.installer.Start(ctx, serverID, Params{Auth: AuthPassword, Domain: awayDomain}, r.password)
	if err != nil {
		t.Fatal(err)
	}
	op = r.settle(op.ID)
	if op.Status != ops.StatusFailed || op.Error == nil || op.Error.Code != CodeCertFailed || op.Error.Step != StepSite {
		t.Fatalf("install with a domain that leads elsewhere: status %s, error %+v", op.Status, op.Error)
	}
	if err := r.installer.WithoutDomain(ctx, op.ID, "www.microsoft.com", ""); err != nil {
		t.Fatal(err)
	}
	op = r.settle(op.ID)
	succeeded(op, "going on without the domain")
	stub("")
	r.probe(serverID, true)
	if applied, _ := r.servers.Get(ctx, serverID); applied.Applied.Inbound.Domain != "" || applied.Applied.Inbound.MaskHost() != "www.microsoft.com" {
		t.Fatalf("the server was recorded as running %+v", applied.Applied.Inbound)
	}

	// 2. The domain given later is a change to apply, and the apply sets the
	//    stub up before pointing Xray at it.
	setDomain(testDomain)
	op = apply()
	succeeded(op, "giving the server its domain")
	if len(op.Steps) != 4 || op.Steps[0].ID != StepSite {
		t.Fatalf("steps of an apply that sets a domain: %+v", op.Steps)
	}
	stub(testDomain)
	r.probe(serverID, true)

	// 3. An apply of something else leaves the stub alone.
	if err := r.servers.SetOverrides(ctx, serverID, map[string]string{templates.FieldPadding: "300-900"}); err != nil {
		t.Fatal(err)
	}
	op = apply()
	succeeded(op, "an apply that does not concern the domain")
	if len(op.Steps) != 3 {
		t.Fatalf("an apply that does not concern the domain has steps %+v", op.Steps)
	}
	stub(testDomain)

	// 4. Renewing by hand, and the timer's own run.
	renewal, err := r.installer.RenewCert(ctx, serverID)
	if err != nil {
		t.Fatalf("renewing by hand: %v", err)
	}
	if renewal.Domain != testDomain || renewal.NotAfter <= time.Now().Unix() {
		t.Fatalf("renewal: %+v", renewal)
	}
	stub(testDomain)
	remote("systemctl start " + renewService)
	if result := remote("systemctl show -p Result --value " + renewService); result != "success" {
		t.Fatalf("the timer's renewal run ended as %s:\n%s", result,
			remote("journalctl -u "+renewService+" -n 30 --no-pager -o cat"))
	}

	// 5. Another domain: a certificate for it first, and the previous one's
	//    goes once the change has passed its check.
	setDomain(secondDomain)
	op = apply()
	succeeded(op, "moving to another domain")
	stub(secondDomain)
	if left := remote("ls -d " + siteCertDir + "/" + testDomain + " 2>/dev/null || true"); left != "" {
		t.Fatal("the certificate of the domain the server moved away from was left behind")
	}
	r.probe(serverID, true)

	// 6. A domain the authority will not vouch for: the apply fails, and the
	//    server is as it was — Xray's settings and the stub both.
	before, _ := r.servers.Get(ctx, serverID)
	setDomain(awayDomain)
	op = apply()
	if op.Status != ops.StatusFailed || op.Error == nil || op.Error.Code != CodeCertFailed || op.Error.Step != StepSite {
		t.Fatalf("apply with a domain that leads elsewhere: status %s, error %+v", op.Status, op.Error)
	}
	if restored := op.Error.Details["restored"]; restored != true {
		t.Fatalf("restored = %v", restored)
	}
	after, _ := r.servers.Get(ctx, serverID)
	if after.Applied.Config != before.Applied.Config {
		t.Fatal("a failed apply was recorded as applied")
	}
	stub(secondDomain)
	r.probe(serverID, true)
	// The edit that could not be applied is thrown away, as "reset" does.
	if err := r.servers.SetInbound(ctx, serverID, after.Applied.Inbound); err != nil {
		t.Fatal(err)
	}

	// 7. Back to someone else's site: the stub and everything it came with
	//    are taken away once the new settings work.
	setDomain("")
	op = apply()
	succeeded(op, "giving the domain up")
	stub("")
	r.probe(serverID, true)

	// 8. Removed, with a rollback of a fresh installation — which this time
	//    has a domain from the start.
	// removed takes Xray off the server and waits for the server to leave the
	// panel — its job goes with it, so there is nothing to look at after.
	removed := func(what string) {
		t.Helper()
		removal, err := r.installer.Uninstall(ctx, serverID, UninstallParams{})
		if err != nil {
			t.Fatal(err)
		}
		r.manager.Wait()
		if _, err := r.servers.Get(ctx, serverID); !errors.Is(err, servers.ErrNotFound) {
			// Still there: show why.
			r.settle(removal.ID)
			t.Fatalf("%s: the server is still in the panel: %v", what, err)
		}
	}
	removed("removing Xray")

	srv = r.addServer("node-domain")
	serverID = srv.ID
	op, err = r.installer.Start(ctx, serverID, Params{Auth: AuthPassword, Domain: testDomain}, r.password)
	if err != nil {
		t.Fatal(err)
	}
	op = r.settle(op.ID)
	succeeded(op, "installing with a domain")
	if len(op.Steps) != 9 || op.Steps[7].ID != StepSite {
		t.Fatalf("steps of an installation with a domain: %+v", op.Steps)
	}
	if until, _ := op.Result["cert_not_after"].(float64); int64(until) <= time.Now().Unix() {
		t.Fatalf("the installation did not say when the certificate runs out: %v", op.Result)
	}
	stub(testDomain)
	r.probe(serverID, true)
	installed, _ := r.servers.Get(ctx, serverID)
	if p, err := r.planner.For(ctx, installed); err != nil || len(p.Changed(installed.Applied)) != 0 || p.Fields[plan.FieldDomain] != testDomain {
		t.Fatalf("a fresh installation has changes waiting: %v %v", p.Changed(installed.Applied), err)
	}

	// 9. Removing Xray takes the stub too. Nobody is left to ask over the
	//    panel's key, so what remains is looked at as the server's owner would.
	removed("removing Xray and the stub")
	client, err := sshxDialPassword(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	left, err := client.Sudo(ctx, "command -v nginx || true; ls -d "+siteRootDir+" "+siteEtcDir+" "+siteLibDir+" /etc/nginx 2>/dev/null || true")
	if err != nil || strings.TrimSpace(left) != "" {
		t.Fatalf("left on the server after the removal: %q (%v)", left, err)
	}
}
