package install

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"xray-console/internal/acmesh"
	"xray-console/internal/ops"
	"xray-console/internal/servers"
	"xray-console/internal/sshx"
	"xray-console/internal/xray"
)

// A server that masquerades as its own domain has a stub site: nginx from the
// distribution's packages, answering over TLS on the loopback (where REALITY
// hands it everyone who is not a client) and on port 80 (for the certificate
// authority's HTTP-01 check and a redirect), with a certificate that acme.sh
// obtains and renews. Everything here is the stub's; none of it is shared
// with Xray's own places or with the client the panel installs on devices.

// StepSite is the step that sets the stub site up — or, in a removal, takes
// it away.
const StepSite = "site"

// Manifest kinds of the stub site.
const (
	// changePackages: nginx, installed by the panel from the distribution.
	changePackages = "packages"
	// changeSite: the stub's own files, units and certificate.
	changeSite = "site"
)

// Where the stub site lives on a server.
const (
	// siteLibDir holds acme.sh.
	siteLibDir = "/usr/local/lib/xray-console-site"
	// siteEtcDir holds acme.sh's state, the certificates and the list of
	// packages the panel installed.
	siteEtcDir  = "/usr/local/etc/xray-console-site"
	siteRootDir = "/var/www/xray-console-site"
	// siteHTMLDir is what a visitor sees; siteACMEDir is where acme.sh puts
	// the files the certificate authority asks for.
	siteHTMLDir  = siteRootDir + "/html"
	siteACMEDir  = siteRootDir + "/acme"
	siteConfPath = "/etc/nginx/conf.d/xray-console-site.conf"
	siteCertDir  = siteEtcDir + "/certs"
	acmePath     = siteLibDir + "/acme.sh"
	acmeHome     = siteEtcDir + "/acme"
	acmeCAPath   = siteEtcDir + "/acme-ca.pem"

	packagesBefore = siteEtcDir + "/packages.before"
	packagesOurs   = siteEtcDir + "/packages"

	renewService = "xray-console-site-renew.service"
	renewTimer   = "xray-console-site-renew.timer"
	nginxService = "nginx.service"
	unitDir      = "/etc/systemd/system/"

	// stubRule is the firewall rule the stub needs: the certificate authority
	// comes to port 80, at first issue and at every renewal.
	stubRule = "80/tcp"
)

// siteTimeout is how long setting the stub up may take: apt and the
// certificate authority both take their time.
const siteTimeout = 20 * time.Minute

// Failure codes of the stub site and its certificate.
const (
	// CodeDomainUnresolved: the domain has no address yet.
	CodeDomainUnresolved = "domain_unresolved"
	// CodeDomainMismatch: the domain leads somewhere other than the server.
	CodeDomainMismatch = "domain_mismatch"
	// CodeDomainIPv6: the domain has an IPv6 address, and the server answers
	// for it on IPv4 only.
	CodeDomainIPv6 = "domain_ipv6"
	// CodeNginxExists: someone's nginx is on the server already.
	CodeNginxExists = "nginx_exists"
	// CodePackagesFailed: the distribution's packages could not be installed.
	CodePackagesFailed = "packages_failed"
	// CodeACMEDownloadFailed: the panel could not get acme.sh.
	CodeACMEDownloadFailed = "acme_download_failed"
	// CodeCertFailed: the certificate authority did not issue a certificate.
	CodeCertFailed = "cert_failed"
	// CodeCertRateLimited: it refused for now; trying again makes it longer.
	CodeCertRateLimited = "cert_rate_limited"
	// CodeStubFailed: nginx does not serve the stub site.
	CodeStubFailed = "stub_failed"
	// CodeCertInvalid: from outside, the server does not show a certificate
	// that is valid for its domain.
	CodeCertInvalid = "cert_invalid"
)

// ACME is which certificate authority issues stub sites' certificates. The
// zero value is Let's Encrypt.
type ACME struct {
	// Directory is the authority's ACME directory.
	Directory string
	// CABundle, when set, is what the directory's own HTTPS certificate is
	// checked against on the server: a test authority's root.
	CABundle []byte
	// Roots are what an issued certificate is checked against when the panel
	// looks at the server from outside; nil is the system's.
	Roots *x509.CertPool
}

// UseACME replaces the certificate authority — for a test authority.
func (i *Installer) UseACME(acme ACME) { i.acme = acme }

// UseResolver replaces the panel's view of DNS — for a panel that must not
// ask the system's resolver.
func (i *Installer) UseResolver(resolve func(ctx context.Context, host string) ([]string, error)) {
	i.resolve = resolve
}

// UseACMEScript replaces where acme.sh comes from.
func (i *Installer) UseACMEScript(fetch func(ctx context.Context) (*acmesh.Script, error)) {
	i.acmeScript = fetch
}

// siteChange is a stub site being set up, moved to another domain or — when
// to is empty — given up, in the course of an apply.
type siteChange struct {
	from, to string
	// touched says the stub on the server is no longer as the apply found it.
	touched bool
}

// --- what the stub is made of ----------------------------------------------------

// stubPage is what the domain shows a visitor. Nothing in it says what the
// server is for, and nothing in it is fetched from elsewhere.
func stubPage(domain string) string {
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>` + domain + `</title>
<style>
html{height:100%}
body{min-height:100%;margin:0;display:flex;align-items:center;justify-content:center;font:16px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif;color:#1f2933;background:#f5f7fa}
main{max-width:32rem;padding:2rem;text-align:center}
h1{margin:0 0 .5rem;font-size:1.5rem;font-weight:600}
p{margin:0;color:#52606d}
</style>
</head>
<body>
<main>
<h1>` + domain + `</h1>
<p>This site is being set up. Please check back later.</p>
</main>
</body>
</html>
`
}

func certPath(domain, file string) string { return siteCertDir + "/" + domain + "/" + file }

// siteConf renders nginx's configuration for the stub. Without withTLS it is
// only what the certificate authority needs to find its files — what there is
// before the first certificate exists, and nginx will not start with a
// certificate it cannot read.
func siteConf(domain string, withTLS, ipv6 bool) string {
	var b strings.Builder
	b.WriteString("# Written by Xray Console; changes made here are overwritten.\nserver {\n")
	b.WriteString("    listen 80 default_server;\n")
	if ipv6 {
		b.WriteString("    listen [::]:80 default_server;\n")
	}
	b.WriteString(`    server_name _;
    server_tokens off;
    location ^~ /.well-known/acme-challenge/ {
        root ` + siteACMEDir + `;
        default_type text/plain;
    }
    location / {
        return 301 https://` + domain + `$request_uri;
    }
}
`)
	if !withTLS {
		return b.String()
	}
	// "listen … http2" rather than "http2 on": the distributions the panel
	// installs on still ship nginx from before the newer spelling.
	b.WriteString(`server {
    listen ` + xray.StubListen + ` ssl http2;
    server_name ` + domain + `;
    server_tokens off;
    ssl_certificate ` + certPath(domain, "fullchain.pem") + `;
    ssl_certificate_key ` + certPath(domain, "key.pem") + `;
    ssl_protocols TLSv1.2 TLSv1.3;
    root ` + siteHTMLDir + `;
    index index.html;
    location / {
        try_files $uri $uri/ =404;
    }
}
`)
	return b.String()
}

func renewServiceUnit() string {
	return `[Unit]
Description=Renew the stub site's certificate (managed by Xray Console)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=` + acmePath + ` --cron --home ` + acmeHome + ` --config-home ` + acmeHome + `
`
}

func renewTimerUnit() string {
	return `[Unit]
Description=Daily check of the stub site's certificate (managed by Xray Console)

[Timer]
OnCalendar=daily
RandomizedDelaySec=6h
Persistent=true

[Install]
WantedBy=timers.target
`
}

// acme builds an acme.sh command line. Every argument is quoted: the domain
// has a strict shape already, the directory of a test authority does not.
func acme(args ...string) string {
	cmd := acmePath + " --home " + acmeHome + " --config-home " + acmeHome
	for _, arg := range args {
		cmd += " " + sshx.Quote(arg)
	}
	return cmd
}

// --- before anything is changed ----------------------------------------------------

// portHolder says whether something on the server listens on a TCP port, and
// what. Needs root to see which process owns the socket.
func (s *session) portHolder(ctx context.Context, run *ops.Run, port int) (map[string]any, bool, error) {
	text := strconv.Itoa(port)
	// Not through a pipe: an ss that fails must not read as a free port.
	out, err := s.sudo(ctx, run, "ss -H -ltnp 'sport = :"+text+"'")
	if err != nil {
		return nil, false, err
	}
	listener, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	listener = strings.TrimSpace(listener)
	if listener == "" {
		return nil, false, nil
	}
	details := map[string]any{"port": port, "listener": listener}
	if match := listenerPattern.FindStringSubmatch(listener); match != nil {
		details["process"], details["pid"] = match[1], match[2]
	}
	return details, true, nil
}

// sitePreflight refuses a server on which the stub cannot be set up without
// touching what is someone else's: a port it needs is taken, or there is an
// nginx the panel did not install. Nothing is changed.
func (s *session) sitePreflight(ctx context.Context, run *ops.Run) error {
	for _, port := range []int{80, xray.StubPort} {
		details, busy, err := s.portHolder(ctx, run, port)
		if err != nil {
			return err
		}
		if busy {
			return ops.Fail(CodePortInUse, fmt.Sprintf("port %d is in use: %v", port, details["listener"]), details)
		}
	}
	out, err := s.quietSudo(ctx, "if command -v nginx >/dev/null 2>&1 || [ -d /etc/nginx ]; then echo present; else echo absent; fi")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "present" {
		return ops.Fail(CodeNginxExists, "nginx is already installed on the server", nil)
	}
	return nil
}

// DomainCheck is where a domain leads, compared with where a server is.
type DomainCheck struct {
	Domain string `json:"domain"`
	// Addresses is what the domain resolves to, as the panel sees it.
	Addresses []string `json:"addresses"`
	// Expected is the server's addresses, as far as they are known.
	Expected []string `json:"expected"`
	// Match: at least one of the domain's addresses is the server's.
	Match bool `json:"match"`
	// Other is the domain's addresses that are not known to be the server's.
	// The certificate authority may come to any of them.
	Other []string `json:"other"`
	// IPv6 is the domain's IPv6 addresses, whoever's they are. Xray answers
	// on IPv4 only, so over any of them the domain's site would not open.
	IPv6 []string `json:"ipv6"`
}

// resolveHost is the panel's own resolver. A name that does not exist is an
// empty answer, not an error.
func resolveHost(ctx context.Context, host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	found, err := net.DefaultResolver.LookupHost(ctx, host)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil, nil
	}
	return found, err
}

// addresses tidies a list of IP addresses: one spelling each, no repeats,
// sorted. What is not an address is dropped.
func addresses(raw []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, text := range raw {
		addr, err := netip.ParseAddr(strings.TrimSpace(text))
		if err != nil {
			continue
		}
		if spelled := addr.Unmap().WithZone("").String(); !seen[spelled] {
			seen[spelled] = true
			out = append(out, spelled)
		}
	}
	sort.Strings(out)
	return out
}

// CheckDomain resolves a domain and compares it with a server's address. own
// is what else is known to be the server's — the addresses on its interfaces.
func (i *Installer) CheckDomain(ctx context.Context, domain, host string, own []string) (*DomainCheck, error) {
	domain, err := xray.NormalizeDomain(domain)
	if err != nil {
		return nil, err
	}
	expected := append([]string{}, own...)
	if _, err := netip.ParseAddr(host); err == nil {
		expected = append(expected, host)
	} else {
		// The server was added by name: it is wherever that name leads.
		byName, err := i.resolve(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
		expected = append(expected, byName...)
	}
	found, err := i.resolve(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", domain, err)
	}
	check := &DomainCheck{
		Domain: domain, Addresses: addresses(found), Expected: addresses(expected),
		Other: []string{}, IPv6: []string{},
	}
	known := map[string]bool{}
	for _, addr := range check.Expected {
		known[addr] = true
	}
	for _, addr := range check.Addresses {
		if parsed, err := netip.ParseAddr(addr); err == nil && parsed.Is6() {
			check.IPv6 = append(check.IPv6, addr)
		}
		if known[addr] {
			check.Match = true
		} else {
			check.Other = append(check.Other, addr)
		}
	}
	return check, nil
}

// domainPointsHere fails unless the domain leads to this server and nowhere
// else: the certificate authority picks the address it comes to, and one that
// is another machine's fails the issue in a way that reads like anything but
// DNS. An IPv6 address is refused even when it is the server's own: the
// authority would be answered there, on port 80, but Xray listens on IPv4
// only, and the domain's site would not open for anyone who comes over IPv6.
func (s *session) domainPointsHere(ctx context.Context, run *ops.Run, srv *servers.Server, domain string) error {
	// What the server calls its own: on some hostings the public IPv4 is
	// somewhere in front of it, but an IPv6 address usually is on the machine.
	out, err := s.quietSudo(ctx, "ip -o addr show scope global 2>/dev/null | awk '{print $4}' | head -n 64")
	if err != nil {
		return err
	}
	var own []string
	for _, line := range strings.Fields(out) {
		prefix, _, _ := strings.Cut(line, "/")
		own = append(own, prefix)
	}
	run.Cmd("resolve " + domain)
	check, err := s.inst.CheckDomain(ctx, domain, srv.Host, own)
	if err != nil {
		return ops.Fail(CodeDomainUnresolved, err.Error(), map[string]any{"domain": domain, "address": srv.Host})
	}
	run.Out(strings.Join(check.Addresses, " "))
	details := map[string]any{
		"domain": domain, "address": srv.Host,
		"found": strings.Join(check.Addresses, ", "), "other": strings.Join(check.Other, ", "),
	}
	switch {
	case len(check.Addresses) == 0:
		return ops.Fail(CodeDomainUnresolved, domain+" does not resolve", details)
	case len(check.IPv6) > 0:
		details["ipv6"] = strings.Join(check.IPv6, ", ")
		return ops.Fail(CodeDomainIPv6, domain+" has an IPv6 address ("+details["ipv6"].(string)+"); the server answers on IPv4 only", details)
	case !check.Match || len(check.Other) > 0:
		return ops.Fail(CodeDomainMismatch, domain+" resolves to "+details["found"].(string)+", not only to this server", details)
	}
	return nil
}

// --- setting the stub up -------------------------------------------------------------

// listPackages is a script that works out which of the packages now on the
// server came with nginx, by comparing with the list taken before it was
// installed, and keeps the answer. Only those are ever removed again.
const listPackages = `if [ -f ` + packagesBefore + ` ] && [ ! -f ` + packagesOurs + ` ]; then
dpkg-query -W -f '${Package}\n' > ` + packagesOurs + `.all
sort -u ` + packagesOurs + `.all > ` + packagesOurs + `.now
comm -13 ` + packagesBefore + ` ` + packagesOurs + `.now > ` + packagesOurs + `.new
grep -E '^(nginx|libnginx-mod)[a-z0-9.+-]*$' ` + packagesOurs + `.new > ` + packagesOurs + `.tmp || [ $? = 1 ]
mv -f ` + packagesOurs + `.tmp ` + packagesOurs + `
rm -f ` + packagesOurs + `.all ` + packagesOurs + `.now ` + packagesOurs + `.new
fi`

const aptGet = "apt-get -o DPkg::Lock::Timeout=120 -q"

// sitePackages installs nginx and what acme.sh needs from the distribution.
func (s *session) sitePackages(ctx context.Context, run *ops.Run) error {
	// set -e: a list of packages that was not taken must not be followed by an
	// installation nobody can tell apart from what was there.
	script := `set -e
export DEBIAN_FRONTEND=noninteractive
install -d -m 0755 ` + siteEtcDir + `
if [ ! -f ` + packagesBefore + ` ]; then
dpkg-query -W -f '${Package}\n' > ` + packagesBefore + `.all
sort -u ` + packagesBefore + `.all > ` + packagesBefore + `.new
rm -f ` + packagesBefore + `.all
mv -f ` + packagesBefore + `.new ` + packagesBefore + `
fi
` + aptGet + ` update
` + aptGet + ` -y --no-install-recommends install nginx curl openssl ca-certificates
` + listPackages
	if out, err := s.sudo(ctx, run, script); err != nil {
		var exit *sshx.ExitError
		if errors.As(err, &exit) {
			return ops.Fail(CodePackagesFailed, "apt-get could not install nginx", map[string]any{"output": tail(out, 1500)})
		}
		return err
	}
	return nil
}

// tail keeps the end of a long output: that is where the error is.
func tail(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) > limit {
		text = strings.ToValidUTF8(text[len(text)-limit:], "")
	}
	return text
}

// siteConfKept is where the previous configuration waits while nginx judges
// the new one. nginx reads conf.d/*.conf, so this name is not read.
const siteConfKept = siteConfPath + ".kept"

// writeSiteConf puts nginx's configuration for the stub in place and has nginx
// take it. nginx judges the file first; one it rejects is not left there —
// the file that was there before, if there was one, goes back.
func (s *session) writeSiteConf(ctx context.Context, run *ops.Run, domain string, withTLS bool) error {
	client, err := s.connect(ctx)
	if err != nil {
		return err
	}
	ipv6, err := s.quietSudo(ctx, "[ -f /proc/net/if_inet6 ] && echo yes || echo no")
	if err != nil {
		return err
	}
	keep := `if [ -f ` + siteConfPath + ` ]; then cp -p ` + siteConfPath + ` ` + siteConfKept + `; else rm -f ` + siteConfKept + `; fi`
	if _, err := s.quietSudo(ctx, keep); err != nil {
		return err
	}
	run.Cmd("write " + siteConfPath)
	conf := siteConf(domain, withTLS, strings.TrimSpace(ipv6) == "yes")
	if err := client.WriteFile(ctx, siteConfPath, 0o644, strings.NewReader(conf)); err != nil {
		return commandFailure(err, client.Target().User)
	}
	if out, err := s.sudo(ctx, run, "nginx -t"); err != nil {
		var exit *sshx.ExitError
		if errors.As(err, &exit) {
			back := `if [ -f ` + siteConfKept + ` ]; then mv -f ` + siteConfKept + ` ` + siteConfPath + `; else rm -f ` + siteConfPath + `; fi`
			if _, undoErr := s.quietSudo(ctx, back); undoErr != nil {
				info(run, "the rejected configuration could not be taken away: "+undoErr.Error())
			}
			return ops.Fail(CodeStubFailed, "nginx rejected the stub site's configuration",
				map[string]any{"output": tail(out, 1500)})
		}
		return err
	}
	if _, err := s.quietSudo(ctx, "rm -f "+siteConfKept); err != nil {
		return err
	}
	// set -e: an nginx that could not be told about the file is not asked
	// whether it is running.
	reload := `set -e
systemctl enable ` + nginxService + `
systemctl reload-or-restart ` + nginxService
	if _, err := s.sudo(ctx, run, reload); err != nil {
		return s.nginxFailure(ctx, run, err)
	}
	state, err := s.sudo(ctx, run, sshx.ServiceSettled(nginxService, 2))
	if err != nil {
		return err
	}
	if state = strings.TrimSpace(state); state != "active" {
		return s.nginxFailure(ctx, run, errors.New(nginxService+" is "+state))
	}
	return nil
}

// nginxFailure is a stub that does not run, with nginx's own words for why.
func (s *session) nginxFailure(ctx context.Context, run *ops.Run, cause error) error {
	// A lost connection or a refusing sudo already says what it is.
	var failure *ops.Failure
	if errors.As(cause, &failure) {
		return cause
	}
	journal, _ := s.quietSudo(ctx, "journalctl -u "+nginxService+" -n 20 --no-pager -o cat")
	logOutput(run, journal)
	return ops.Fail(CodeStubFailed, cause.Error(), map[string]any{"output": tail(journal, 1500)})
}

// putACME makes sure the panel's acme.sh is on the server, as it should be.
func (s *session) putACME(ctx context.Context, run *ops.Run) error {
	client, err := s.connect(ctx)
	if err != nil {
		return err
	}
	script, err := s.inst.acmeScript(ctx)
	if err != nil {
		return ops.Fail(CodeACMEDownloadFailed, err.Error(), map[string]any{"version": acmesh.Version})
	}
	sum, err := s.quietSudo(ctx, "sha256sum "+sshx.Quote(acmePath)+" 2>/dev/null || true")
	if err != nil {
		return err
	}
	if got, _, _ := strings.Cut(strings.TrimSpace(sum), " "); got != script.SHA256 {
		run.Cmd(fmt.Sprintf("upload acme.sh %s → %s (downloaded by the panel, checksum verified)", script.Version, acmePath))
		if err := client.WriteFile(ctx, acmePath, 0o755, bytes.NewReader(script.Data)); err != nil {
			return commandFailure(err, client.Target().User)
		}
		sum, err := s.quietSudo(ctx, "sha256sum "+sshx.Quote(acmePath))
		if err != nil {
			return err
		}
		if got, _, _ := strings.Cut(strings.TrimSpace(sum), " "); got != script.SHA256 {
			return ops.Fail(CodeUploadCorrupted,
				fmt.Sprintf("%s: checksum after upload is %q, expected %s", acmePath, got, script.SHA256),
				map[string]any{"file": acmePath})
		}
	}
	if len(s.inst.acme.CABundle) > 0 {
		if err := client.WriteFile(ctx, acmeCAPath, 0o644, bytes.NewReader(s.inst.acme.CABundle)); err != nil {
			return commandFailure(err, client.Target().User)
		}
	}
	return nil
}

// authority is the arguments that name the certificate authority.
func (s *session) authority() []string {
	args := []string{"--server", "letsencrypt"}
	if s.inst.acme.Directory != "" {
		args[1] = s.inst.acme.Directory
	}
	if len(s.inst.acme.CABundle) > 0 {
		args = append(args, "--ca-bundle", acmeCAPath)
	}
	return args
}

// acmeSkipped is how acme.sh exits when it was asked for a certificate it
// already has and that is not due for renewal.
const acmeSkipped = 2

// certFailure is acme.sh not coming back with a certificate, told apart by
// what the authority said: "not now" is a different problem from "no".
func certFailure(domain, out string) error {
	code := CodeCertFailed
	said := strings.ToLower(out)
	if strings.Contains(said, "ratelimited") || strings.Contains(said, "too many certificates") ||
		strings.Contains(said, "too many failed authorizations") || strings.Contains(said, "too many new orders") {
		code = CodeCertRateLimited
	}
	return ops.Fail(code, "no certificate was issued for "+domain,
		map[string]any{"domain": domain, "output": tail(out, 1500)})
}

// issueCert has acme.sh obtain a certificate for the domain and put it where
// nginx reads it. One that is already there and not due is left as it is.
func (s *session) issueCert(ctx context.Context, run *ops.Run, domain string) error {
	issue := append([]string{"--issue", "-d", domain, "-w", siteACMEDir, "--keylength", "ec-256"}, s.authority()...)
	if out, err := s.sudo(ctx, run, acme(issue...)); err != nil {
		var exit *sshx.ExitError
		switch {
		case !errors.As(err, &exit):
			// Not acme.sh's verdict: the connection, sudo.
			return err
		case exit.Code == acmeSkipped:
			run.Info("a certificate for " + domain + " is already there and not due for renewal")
		default:
			return certFailure(domain, out)
		}
	}
	// set -e: a certificate that was not copied must not be reported as in
	// place. The reload is also what acme.sh runs after every renewal.
	install := `set -e
install -d -m 0700 ` + sshx.Quote(siteCertDir+"/"+domain) + `
` + acme("--install-cert", "-d", domain, "--ecc",
		"--key-file", certPath(domain, "key.pem"),
		"--fullchain-file", certPath(domain, "fullchain.pem"),
		"--reloadcmd", "systemctl reload "+nginxService)
	if out, err := s.sudo(ctx, run, install); err != nil {
		var exit *sshx.ExitError
		if errors.As(err, &exit) {
			return ops.Fail(CodeCertFailed, "the certificate for "+domain+" could not be put in place",
				map[string]any{"domain": domain, "output": tail(out, 1500)})
		}
		return err
	}
	return nil
}

// certExpiry reads when the domain's certificate on the server runs out; zero
// when the server's answer is not a date.
func (s *session) certExpiry(ctx context.Context, domain string) time.Time {
	out, err := s.quietSudo(ctx, "openssl x509 -noout -enddate -in "+sshx.Quote(certPath(domain, "fullchain.pem")))
	if err != nil {
		return time.Time{}
	}
	_, date, _ := strings.Cut(strings.TrimSpace(out), "=")
	until, err := time.Parse("Jan _2 15:04:05 2006 MST", date)
	if err != nil {
		return time.Time{}
	}
	return until
}

// stubAnswers asks the stub for its page on the loopback, as REALITY will.
// Whether the certificate is one a stranger would accept is not asked here:
// that is looked at from outside, where a stranger stands.
func (s *session) stubAnswers(ctx context.Context, run *ops.Run, domain string) error {
	port := strconv.Itoa(xray.StubPort)
	out, err := s.sudo(ctx, run, "curl -sS -k -o /dev/null -w '%{http_code}' --max-time 10 --resolve "+
		sshx.Quote(domain+":"+port+":127.0.0.1")+" "+sshx.Quote("https://"+domain+":"+port+"/"))
	if err != nil {
		var exit *sshx.ExitError
		if errors.As(err, &exit) {
			return ops.Fail(CodeStubFailed, "the stub site does not answer on "+xray.StubListen,
				map[string]any{"output": tail(out, 1500)})
		}
		return err
	}
	if status := strings.TrimSpace(out); status != "200" {
		return ops.Fail(CodeStubFailed, "the stub site answered HTTP "+status,
			map[string]any{"output": "HTTP " + status})
	}
	return nil
}

// record notes on the job what certificate the stub has now.
func (s *session) recordCert(ctx context.Context, run *ops.Run, domain string) {
	detail := domain + " · HTTP-01"
	if until := s.certExpiry(ctx, domain); !until.IsZero() {
		run.SetResult("cert_not_after", until.Unix())
		detail += " · until " + until.UTC().Format("2006-01-02")
	}
	run.SetResult("domain", domain)
	run.Detail(detail)
}

// setUpSite puts the whole stub on a server that has none: nginx, the page,
// acme.sh, a certificate for the domain, the timer that renews it. Safe to
// run again after it stopped anywhere.
func (s *session) setUpSite(ctx context.Context, run *ops.Run, domain string) error {
	client, err := s.connect(ctx)
	if err != nil {
		return err
	}
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}

	// An nginx that was there before the panel is not the panel's to
	// configure — and, later, not the panel's to remove. So it goes into the
	// manifest only when this step installs it, and before it does.
	if !hasChange(srv, changePackages) {
		if err := s.sitePreflight(ctx, run); err != nil {
			return err
		}
		if err := s.addChange(ctx, changePackages, "nginx (apt)"); err != nil {
			return err
		}
	}
	if err := s.sitePackages(ctx, run); err != nil {
		return err
	}

	for _, thing := range []string{siteConfPath, siteRootDir + "/", siteEtcDir + "/", siteLibDir + "/", renewTimer} {
		if err := s.addChange(ctx, changeSite, thing); err != nil {
			return err
		}
	}
	// The distribution's own "Welcome to nginx" site would answer on port 80
	// instead of the stub; this nginx is here for the stub alone.
	prepare := `set -e
install -d -m 0755 ` + siteLibDir + ` ` + siteRootDir + ` ` + siteHTMLDir + ` ` + siteACMEDir + `
install -d -m 0700 ` + acmeHome + ` ` + siteCertDir + `
rm -f /etc/nginx/sites-enabled/default`
	if _, err := s.sudo(ctx, run, prepare); err != nil {
		return err
	}
	run.Cmd("write " + siteHTMLDir + "/index.html")
	if err := client.WriteFile(ctx, siteHTMLDir+"/index.html", 0o644, strings.NewReader(stubPage(domain))); err != nil {
		return commandFailure(err, client.Target().User)
	}
	if err := s.openPort(ctx, run, stubRule); err != nil {
		return err
	}

	// nginx has to be running before acme.sh is asked for anything: the
	// authority's check comes to it, and so does the reload acme.sh ends
	// with. Until there is a certificate it can only serve that check; with
	// one kept from an attempt that failed, it serves the site at once.
	if err := s.writeSiteConf(ctx, run, domain, s.hasCert(ctx, domain)); err != nil {
		return err
	}
	if err := s.putACME(ctx, run); err != nil {
		return err
	}
	if err := s.issueCert(ctx, run, domain); err != nil {
		return err
	}
	if err := s.writeSiteConf(ctx, run, domain, true); err != nil {
		return err
	}
	if err := s.stubAnswers(ctx, run, domain); err != nil {
		return err
	}

	for name, unit := range map[string]string{renewService: renewServiceUnit(), renewTimer: renewTimerUnit()} {
		if err := client.WriteFile(ctx, unitDir+name, 0o644, strings.NewReader(unit)); err != nil {
			return commandFailure(err, client.Target().User)
		}
	}
	timer := `set -e
systemctl daemon-reload
systemctl enable --now ` + renewTimer
	if _, err := s.sudo(ctx, run, timer); err != nil {
		return err
	}
	s.recordCert(ctx, run, domain)
	return nil
}

// moveSite takes a stub that serves one domain to another: a certificate for
// the new name, then the page and nginx's configuration. The previous name's
// certificate stays until the change has been checked.
func (s *session) moveSite(ctx context.Context, run *ops.Run, domain string) error {
	client, err := s.connect(ctx)
	if err != nil {
		return err
	}
	if err := s.putACME(ctx, run); err != nil {
		return err
	}
	// Port 80 answers the authority's check whatever the name asked for.
	if err := s.issueCert(ctx, run, domain); err != nil {
		return err
	}
	if err := client.WriteFile(ctx, siteHTMLDir+"/index.html", 0o644, strings.NewReader(stubPage(domain))); err != nil {
		return commandFailure(err, client.Target().User)
	}
	if err := s.writeSiteConf(ctx, run, domain, true); err != nil {
		return err
	}
	if err := s.stubAnswers(ctx, run, domain); err != nil {
		return err
	}
	s.recordCert(ctx, run, domain)
	return nil
}

// --- taking the stub away --------------------------------------------------------------

// undoSite removes the stub site and everything that came with it, nginx
// included when the panel installed it. run is nil outside an operation.
func (s *session) undoSite(ctx context.Context, run *ops.Run) error {
	return s.removeSite(ctx, run, false)
}

// parkSite removes the stub site but keeps what the certificate authority
// gave: acme.sh's account and the certificates. It is how an apply that
// failed takes back a stub it had just set up — the next attempt finds the
// certificate there and does not ask for another, of which the authority
// issues only a few a week for one name. What is kept stays in the manifest
// and goes when the domain is given up or Xray is removed.
func (s *session) parkSite(ctx context.Context, run *ops.Run) error {
	return s.removeSite(ctx, run, true)
}

func (s *session) removeSite(ctx context.Context, run *ops.Run, keepCerts bool) error {
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}
	if !hasChange(srv, changePackages) && !hasChange(srv, changeSite) {
		// Nothing of the stub was ever put there; the port it would have
		// needed is still closed below if this run opened it.
		return s.closePort(ctx, run, stubRule)
	}
	// set -e: files that were not removed must not be struck off the list of
	// what the panel left on the server.
	script := `set -e
systemctl disable --now ` + renewTimer + ` 2>/dev/null || true
rm -f ` + unitDir + renewService + ` ` + unitDir + renewTimer + `
systemctl daemon-reload
rm -f ` + siteConfPath + ` ` + siteConfKept + `
` + listPackages + `
pkgs=
if [ -f ` + packagesOurs + ` ]; then pkgs=$(grep -E '^(nginx|libnginx-mod)[a-z0-9.+-]*$' ` + packagesOurs + ` || [ $? = 1 ]); fi
if [ -n "$pkgs" ]; then
systemctl stop ` + nginxService + ` 2>/dev/null || true
DEBIAN_FRONTEND=noninteractive ` + aptGet + ` -y purge $pkgs
elif systemctl is-active --quiet ` + nginxService + `; then
systemctl reload ` + nginxService + ` || true
fi
`
	if keepCerts {
		// The lists of packages describe an nginx that is gone now; the next
		// set-up takes them anew.
		script += `rm -rf ` + siteRootDir + ` ` + siteLibDir + `
rm -f ` + packagesBefore + ` ` + packagesOurs
	} else {
		script += `rm -rf ` + siteRootDir + ` ` + siteEtcDir + ` ` + siteLibDir
	}
	if _, err := s.sudo(ctx, run, script); err != nil {
		return err
	}
	if err := s.closePort(ctx, run, stubRule); err != nil {
		return err
	}
	for _, kind := range []string{changeSite, changePackages} {
		if err := s.inst.Servers.RemoveChanges(ctx, s.serverID, kind); err != nil {
			return err
		}
	}
	if keepCerts {
		return s.addChange(ctx, changeSite, siteEtcDir+"/")
	}
	return nil
}

// hasCert reports whether a certificate for the domain is on the server.
func (s *session) hasCert(ctx context.Context, domain string) bool {
	has, err := s.quietSudo(ctx, "[ -s "+sshx.Quote(certPath(domain, "fullchain.pem"))+" ] && [ -s "+
		sshx.Quote(certPath(domain, "key.pem"))+" ] && echo yes || echo no")
	return err == nil && strings.TrimSpace(has) == "yes"
}

// dropOtherCerts forgets every certificate on the server but the one of the
// domain it answers for: the one of a name it moved away from, and any that
// an apply which failed left for its next attempt and that was never used.
// The names are the server's own account of what is there, so they are never
// put into a command as text: the loop hands each to acme.sh as one argument.
func (s *session) dropOtherCerts(ctx context.Context, run *ops.Run, keep string) error {
	script := `keep=` + sshx.Quote(keep) + `
for dir in ` + siteCertDir + `/*/; do
[ -d "$dir" ] || continue
name=$(basename "$dir")
[ "$name" = "$keep" ] && continue
` + acmePath + ` --home ` + acmeHome + ` --config-home ` + acmeHome + ` --remove -d "$name" --ecc >/dev/null 2>&1 || true
rm -rf "$dir" ` + acmeHome + `/"$name"_ecc
echo "$name"
done`
	out, err := s.quietSudo(ctx, script)
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(tail(out, 1500)) {
		info(run, "the certificate of "+name+" is no longer used and was removed")
	}
	return nil
}

// --- the step of an installation -------------------------------------------------------

// stepSite sets the stub up for a server that is being installed with its own
// domain — or, when the administrator chose to go on without the domain after
// this step failed, takes away what was set up and points REALITY at the
// other site instead.
func (s *session) stepSite(ctx context.Context, run *ops.Run) error {
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}
	if srv.Inbound == nil {
		return errors.New("the server has no inbound settings yet")
	}
	if srv.Inbound.Domain != "" {
		return s.setUpSite(ctx, run, srv.Inbound.Domain)
	}

	run.Info("going on without the server's own domain")
	if err := s.undoSite(ctx, run); err != nil {
		return err
	}
	// The configuration put on the server earlier in this installation still
	// sends strangers to a stub that is no longer there.
	if configured, _ := run.Result("domain").(string); configured != "" {
		p, _, err := s.buildPlan(ctx)
		if err != nil {
			return err
		}
		if err := s.stageConfig(ctx, run, p); err != nil {
			return err
		}
		swap := `set -e
mv -f ` + candidatePath + ` ` + xray.ConfigPath + `
systemctl restart ` + xray.ServiceName
		if _, err := s.sudo(ctx, run, swap); err != nil {
			return err
		}
		if err := s.xraySettled(ctx, run); err != nil {
			return err
		}
		s.plan = p
		run.SetResult("mask_host", p.Inbound.MaskHost())
	}
	run.SetResult("domain", "")
	run.Detail("without a domain · " + srv.Inbound.MaskHost())
	return nil
}

// xraySettled waits out the first restart interval and fails unless Xray is
// still up, as the same process.
func (s *session) xraySettled(ctx context.Context, run *ops.Run) error {
	state, err := s.sudo(ctx, run, sshx.ServiceSettled(xray.ServiceName, settleSeconds))
	if err != nil {
		return err
	}
	if state = strings.TrimSpace(state); state != "active" {
		journal, _ := s.quietSudo(ctx, "journalctl -u "+xray.ServiceName+" -n 20 --no-pager -o cat")
		logOutput(run, journal)
		return ops.Fail(CodeServiceFailed, xray.ServiceName+" is "+state,
			map[string]any{"state": state, "output": strings.TrimSpace(journal)})
	}
	return nil
}

// --- the stub during an apply ----------------------------------------------------------

// applySite is the first step of an apply that gives the server a domain or
// moves it to another one: the stub has to answer for the new name before
// Xray is told to send strangers to it.
func (s *session) applySite(ctx context.Context, run *ops.Run) error {
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}
	// Asked before anything is installed: a name that leads elsewhere is told
	// as that, not as a certificate that would not be issued.
	if err := s.domainPointsHere(ctx, run, srv, s.site.to); err != nil {
		return withRestored(err, true)
	}
	s.site.touched = true
	if s.site.from == "" {
		err = s.setUpSite(ctx, run, s.site.to)
	} else {
		err = s.moveSite(ctx, run, s.site.to)
	}
	if err != nil {
		// Xray's configuration has not been touched yet.
		return withRestored(err, s.revertSite(ctx, run))
	}
	return nil
}

// revertSite puts the stub back as the apply found it: gone, or serving the
// previous domain. It reports whether that worked.
func (s *session) revertSite(ctx context.Context, run *ops.Run) bool {
	if s.site == nil || !s.site.touched || s.site.to == "" {
		return true
	}
	// Even if the job was cancelled, like the configuration's own way back.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()
	// A certificate that was obtained for the new name is kept either way:
	// asking for it again at every attempt is how a domain gets refused by
	// the authority for a week.
	kept := s.hasCert(ctx, s.site.to)
	var err error
	switch {
	case s.site.from != "":
		if err = s.revertPage(ctx, s.site.from); err == nil {
			err = s.writeSiteConf(ctx, run, s.site.from, true)
		}
	case kept:
		err = s.parkSite(ctx, run)
	default:
		err = s.undoSite(ctx, run)
	}
	if err != nil {
		run.Info("could not put the stub site back as it was: " + err.Error())
		return false
	}
	run.Info("the stub site is as it was before")
	if kept {
		run.Info("the certificate of " + s.site.to + " stays on the server for the next attempt")
	}
	s.site.touched = false
	return true
}

func (s *session) revertPage(ctx context.Context, domain string) error {
	client, err := s.connect(ctx)
	if err != nil {
		return err
	}
	if err := client.WriteFile(ctx, siteHTMLDir+"/index.html", 0o644, strings.NewReader(stubPage(domain))); err != nil {
		return commandFailure(err, client.Target().User)
	}
	return nil
}

// settleSite tidies up after an apply that passed its check: a stub the
// server no longer uses goes, and so does the certificate of a domain it
// moved away from. Neither is a reason to call the apply failed — the new
// settings are on the server and work — so what could not be removed is said
// in the log and stays in the manifest, where the next apply or the removal
// of Xray finds it.
func (s *session) settleSite(ctx context.Context, run *ops.Run) {
	srv, err := s.server(ctx)
	if err != nil {
		return
	}
	switch {
	case s.plan.Inbound.Domain == "" && (hasChange(srv, changeSite) || hasChange(srv, changePackages)):
		if err := s.undoSite(ctx, run); err != nil {
			run.Info("the stub site is no longer used but could not be removed: " + err.Error())
			run.SetResult("stub_left", true)
			return
		}
		run.Info("the stub site and its certificate were removed")
	case s.plan.Inbound.Domain != "" && hasChange(srv, changeSite):
		if err := s.dropOtherCerts(ctx, run, s.plan.Inbound.Domain); err != nil {
			run.Info("certificates the server no longer uses could not be removed: " + err.Error())
		}
	}
}

// --- renewing by hand --------------------------------------------------------------------

// ErrNoDomain: the server does not masquerade as its own domain.
var ErrNoDomain = errors.New("the server has no domain of its own")

// CertRenewal is what renewing a certificate by hand came to.
type CertRenewal struct {
	Domain string `json:"domain"`
	// NotAfter is when the certificate now on the server runs out; 0 when the
	// server did not say.
	NotAfter int64 `json:"not_after"`
}

// RenewCert has acme.sh obtain a new certificate for the server's domain now,
// whatever the one it has is good for. The authority counts these: asked too
// often, it refuses for days.
func (i *Installer) RenewCert(ctx context.Context, serverID int64) (*CertRenewal, error) {
	srv, err := i.Servers.Get(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if srv.Status != servers.StatusReady {
		return nil, ErrNotReady
	}
	// The domain the server runs, not one that is still waiting to be applied.
	if srv.Applied == nil || srv.Applied.Inbound == nil || srv.Applied.Inbound.Domain == "" {
		return nil, ErrNoDomain
	}
	// Not a job, but nothing else may run on the server meanwhile — a second
	// renewal least of all: each one is a certificate the authority counts.
	release, err := i.Ops.Hold(serverID)
	if err != nil {
		return nil, err
	}
	defer release()
	domain := srv.Applied.Inbound.Domain
	s := &session{inst: i, serverID: serverID}
	defer s.close()
	renew := append([]string{"--renew", "-d", domain, "--ecc", "--force"}, s.authority()...)
	if out, err := s.quietSudo(ctx, acme(renew...)); err != nil {
		var exit *sshx.ExitError
		if errors.As(err, &exit) {
			return nil, certFailure(domain, out)
		}
		return nil, err
	}
	renewal := &CertRenewal{Domain: domain}
	if until := s.certExpiry(ctx, domain); !until.IsZero() {
		renewal.NotAfter = until.Unix()
	}
	if i.CertChanged != nil {
		i.CertChanged(serverID)
	}
	return renewal, nil
}
