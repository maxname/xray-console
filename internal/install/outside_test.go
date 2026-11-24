package install

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"xray-console/internal/ops"
	"xray-console/internal/xray"
)

// tlsSite is a server that shows one certificate for one name to whoever
// connects — what REALITY hands a stranger to. It returns where it listens
// and the certificate, for a test that wants to trust it.
func tlsSite(t *testing.T, name string) (address string, cert *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, IsCA: true, BasicConstraintsValid: true,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if cert, err = x509.ParseCertificate(der); err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.(*tls.Conn).Handshake()
			}()
		}
	}()
	return listener.Addr().String(), cert
}

func inboundAt(t *testing.T, address, domain string) *xray.Inbound {
	t.Helper()
	_, port, _ := net.SplitHostPort(address)
	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	inbound := &xray.Inbound{Port: number}
	if domain != "" {
		err = inbound.SetDomain(domain)
	} else {
		err = inbound.SetMaskHost(ownDomain)
	}
	if err != nil {
		t.Fatal(err)
	}
	return inbound
}

func TestACertificateThatWasBadBeforeTheJobDoesNotFailIt(t *testing.T) {
	ctx := context.Background()
	address, cert := tlsSite(t, ownDomain)
	trusting, trustingNobody := x509.NewCertPool(), x509.NewCertPool()
	trusting.AddCert(cert)
	code := func(err error) string {
		var failure *ops.Failure
		if errors.As(err, &failure) {
			return failure.Code
		}
		if err != nil {
			return err.Error()
		}
		return ""
	}
	with := func(roots *x509.CertPool) *Installer { return &Installer{acme: ACME{Roots: roots}} }

	// A certificate a visitor would take: fine for every job.
	for _, s := range []*session{
		{inst: with(trusting)},
		{inst: with(trusting), site: &siteChange{to: ownDomain}},
		{inst: with(trusting), params: Params{Domain: ownDomain}},
	} {
		if _, err := s.outside(ctx, nil, address, inboundAt(t, address, ownDomain)); err != nil {
			t.Fatalf("a good certificate: %v", err)
		}
	}

	// One a visitor would not take. The job that gives the server this
	// domain — an installation with it, an apply that sets it — has failed.
	for name, s := range map[string]*session{
		"an apply that sets the domain": {inst: with(trustingNobody), site: &siteChange{to: ownDomain}},
		"an installation with a domain": {inst: with(trustingNobody), params: Params{Domain: ownDomain}},
	} {
		if _, err := s.outside(ctx, nil, address, inboundAt(t, address, ownDomain)); code(err) != CodeCertInvalid {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Any other job — an apply of something else, an update of Xray — found
	// the server like that and goes on: the port answers, and that is what it
	// came to ask.
	for name, s := range map[string]*session{
		"an apply of something else": {inst: with(trustingNobody)},
		"an update of Xray":          {inst: with(trustingNobody), replaced: true},
	} {
		if _, err := s.outside(ctx, nil, address, inboundAt(t, address, ownDomain)); err != nil {
			t.Fatalf("%s was failed by a certificate it did not make: %v", name, err)
		}
	}

	// A server that passes itself off as someone else's site is judged as
	// before: that site's certificate has to be good.
	s := &session{inst: with(nil)}
	if _, err := s.outside(ctx, nil, address, inboundAt(t, address, "")); code(err) != CodeMaskUnreachable {
		t.Fatalf("someone else's site with a certificate nobody signed: %v", err)
	}

	// And a port nobody answers on is that, whatever the job.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	nowhere := closed.Addr().String()
	closed.Close()
	s = &session{inst: with(trustingNobody)}
	if _, err := s.outside(ctx, nil, nowhere, inboundAt(t, nowhere, ownDomain)); code(err) != CodePortUnreachable {
		t.Fatalf("a closed port: %v", err)
	}
}

func TestAPortThatIsTakenOnTheServerIsNotAFailedCheck(t *testing.T) {
	started := func(u *unit) int {
		count := 0
		for _, script := range u.server.ran {
			if strings.Contains(script, "nohup ") && strings.Contains(script, checkDir+"/check-") {
				count++
			}
		}
		return count
	}

	// The temporary client could not listen: the port the panel picked for it
	// is in use on the server. That is tried again with another port, not
	// told as settings that do not work.
	u := newUnit(t)
	u.server.respond = func(script string) (string, error, bool) {
		if strings.Contains(script, "tail -n 15 "+checkDir+"/check-") {
			return "Failed to start: listen tcp 127.0.0.1:40000: bind: address already in use\n", nil, true
		}
		return "", nil, false
	}
	u.failed(u.apply(), CodeCheckFailed, StepVerify)
	if got := started(u); got != checkPortAttempts {
		t.Fatalf("the temporary client was started %d times, want %d", got, checkPortAttempts)
	}

	// Any other reason is the check's answer, and it is asked once.
	u = newUnit(t)
	u.failed(u.apply(), CodeCheckFailed, StepVerify)
	if got := started(u); got != 1 {
		t.Fatalf("the temporary client was started %d times, want 1", got)
	}

	// Nothing picked is a port Linux may have given to an outgoing connection.
	for range 200 {
		if port, err := randomPort(); err != nil || port < 20000 || port >= 30000 {
			t.Fatalf("port %d, %v", port, err)
		}
	}
}
