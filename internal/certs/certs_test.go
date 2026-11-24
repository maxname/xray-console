package certs

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xray-console/internal/events"
	"xray-console/internal/secrets"
	"xray-console/internal/servers"
	"xray-console/internal/store"
	"xray-console/internal/templates"
	"xray-console/internal/xray"
)

type fixture struct {
	t       *testing.T
	watcher *Watcher
	store   *Store
	servers *servers.Store
	events  *events.Store
	now     time.Time
	// What a look finds, by the address looked at.
	leaf *Leaf
	err  error
	// asked is who was asked for what name.
	asked []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &fixture{
		t: t, store: NewStore(db), servers: servers.NewStore(db, box), events: events.NewStore(db, logger),
		now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC),
	}
	f.watcher = NewWatcher(f.servers, f.store, f.events, logger)
	f.watcher.now = func() time.Time { return f.now }
	f.watcher.read = func(_ context.Context, address, serverName string, _ *x509.CertPool) (*Leaf, error) {
		f.asked = append(f.asked, serverName+" at "+address)
		return f.leaf, f.err
	}
	return f
}

// server adds an installed server that runs this domain; "" is one that
// masquerades as someone else's site.
func (f *fixture) server(name, domain string) *servers.Server {
	f.t.Helper()
	ctx := context.Background()
	srv, err := f.servers.Create(ctx, servers.NewServer{Name: name, Host: "192.0.2.10", SSHPort: 22, SSHUser: "root", HostKey: "k"})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.servers.SetStatus(ctx, srv.ID, servers.StatusReady); err != nil {
		f.t.Fatal(err)
	}
	f.runs(srv.ID, domain)
	srv, _ = f.servers.Get(ctx, srv.ID)
	return srv
}

func (f *fixture) runs(serverID int64, domain string) {
	f.t.Helper()
	inbound, err := xray.NewInbound("www.example.com")
	if err == nil && domain != "" {
		err = inbound.SetDomain(domain)
	}
	if err != nil {
		f.t.Fatal(err)
	}
	applied := &servers.Applied{Inbound: inbound, Fields: map[string]string{templates.FieldPort: "443"}}
	if err := f.servers.SetApplied(context.Background(), serverID, applied); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) cert(serverID int64) *State {
	f.t.Helper()
	state, err := f.store.Get(context.Background(), serverID)
	if err != nil {
		f.t.Fatal(err)
	}
	return state
}

func (f *fixture) feed() []string {
	f.t.Helper()
	recent, err := f.events.Recent(context.Background(), 10)
	if err != nil {
		f.t.Fatal(err)
	}
	var kinds []string
	for _, event := range recent {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func TestTheCertificateAServerShowsIsRememberedAndARenewalIsAnEvent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	srv := f.server("ams-1", "cdn.example.net")
	plain := f.server("fra-1", "")
	issued := f.now.Add(-30 * 24 * time.Hour)
	f.leaf = &Leaf{NotBefore: issued, NotAfter: issued.Add(90 * 24 * time.Hour), Issuer: "R13"}

	f.watcher.RunOnce(ctx)
	// Asked as a visitor would ask: the server's address, the domain's name.
	if len(f.asked) != 1 || f.asked[0] != "cdn.example.net at 192.0.2.10:443" {
		t.Fatalf("asked %q", f.asked)
	}
	state := f.cert(srv.ID)
	if state == nil || state.Domain != "cdn.example.net" || !state.NotAfter.Equal(f.leaf.NotAfter) || state.Issuer != "R13" || state.Error != "" {
		t.Fatalf("recorded %+v", state)
	}
	if f.cert(plain.ID) != nil {
		t.Fatal("a server without a domain has a certificate on record")
	}
	// The first certificate of a name is not a renewal.
	if kinds := f.feed(); len(kinds) != 0 {
		t.Fatalf("events after the first look: %v", kinds)
	}

	// The same one, an hour later: nothing happened.
	f.now = f.now.Add(time.Hour)
	f.watcher.RunOnce(ctx)
	if kinds := f.feed(); len(kinds) != 0 {
		t.Fatalf("events for a certificate that did not change: %v", kinds)
	}

	// One that lasts longer: the server renewed it.
	f.leaf = &Leaf{NotBefore: f.now, NotAfter: f.now.Add(90 * 24 * time.Hour), Issuer: "R13"}
	f.watcher.RunOnce(ctx)
	if kinds := f.feed(); len(kinds) != 1 || kinds[0] != events.CertRenewed {
		t.Fatalf("events after a renewal: %v", kinds)
	}
	if state := f.cert(srv.ID); !state.NotAfter.Equal(f.leaf.NotAfter) {
		t.Fatalf("recorded %+v", state)
	}
}

func TestAServerThatDoesNotAnswerKeepsTheDatesLastRead(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	srv := f.server("ams-1", "cdn.example.net")
	f.leaf = &Leaf{NotBefore: f.now.Add(-time.Hour), NotAfter: f.now.Add(60 * 24 * time.Hour)}
	f.watcher.RunOnce(ctx)
	read := f.cert(srv.ID).NotAfter

	f.leaf, f.err = nil, errors.New("dial tcp 192.0.2.10:443: i/o timeout "+strings.Repeat("x", 500))
	f.now = f.now.Add(time.Hour)
	f.watcher.RunOnce(ctx)
	state := f.cert(srv.ID)
	if !state.NotAfter.Equal(read) || !strings.Contains(state.Error, "i/o timeout") || !state.CheckedAt.Equal(f.now) {
		t.Fatalf("recorded %+v", state)
	}
	// What a server answers with is not trusted to be small.
	if len(state.Error) > maxText {
		t.Fatalf("an error of %d characters was stored", len(state.Error))
	}

	// A certificate a visitor would refuse is read, and said to be refused.
	f.err = nil
	f.leaf = &Leaf{NotBefore: f.now.Add(-100 * 24 * time.Hour), NotAfter: f.now.Add(-10 * 24 * time.Hour), Problem: "certificate has expired"}
	f.watcher.RunOnce(ctx)
	if state := f.cert(srv.ID); state.Error != "certificate has expired" || !state.Overdue(f.now) {
		t.Fatalf("recorded %+v", state)
	}
}

func TestADomainThatIsGivenUpOrChangedIsNotConfusedWithTheLastOne(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	srv := f.server("ams-1", "cdn.example.net")
	f.leaf = &Leaf{NotBefore: f.now, NotAfter: f.now.Add(30 * 24 * time.Hour)}
	f.watcher.RunOnce(ctx)

	// Another domain, with a certificate that lasts longer: not a renewal.
	f.runs(srv.ID, "edge.example.net")
	f.leaf = &Leaf{NotBefore: f.now, NotAfter: f.now.Add(90 * 24 * time.Hour)}
	f.watcher.RunOnce(ctx)
	if state := f.cert(srv.ID); state.Domain != "edge.example.net" {
		t.Fatalf("recorded %+v", state)
	}
	if kinds := f.feed(); len(kinds) != 0 {
		t.Fatalf("a change of domain was read as a renewal: %v", kinds)
	}

	// No domain at all any more: nothing is kept, and nobody is asked.
	f.runs(srv.ID, "")
	f.asked = nil
	f.watcher.RunOnce(ctx)
	if f.cert(srv.ID) != nil || len(f.asked) != 0 {
		t.Fatalf("a server without a domain: %+v, asked %q", f.cert(srv.ID), f.asked)
	}
}

func TestOverdueIsLongAfterTheServerShouldHaveRenewed(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for name, c := range map[string]struct {
		life, left time.Duration
		want       bool
	}{
		"freshly issued":                  {90 * day, 89 * day, false},
		"when acme.sh renews":             {90 * day, 60 * day, false},
		"three weeks left":                {90 * day, 21 * day, false},
		"past the point of no renewal":    {90 * day, 19 * day, true},
		"run out":                         {90 * day, -2 * day, true},
		"a short one, half way":           {6 * day, 3 * day, false},
		"a short one, its last quarter":   {6 * day, day, true},
		"a long one with two weeks to go": {397 * day, 14 * day, true},
	} {
		state := State{NotBefore: now.Add(c.left - c.life), NotAfter: now.Add(c.left)}
		if got := state.Overdue(now); got != c.want {
			t.Errorf("%s: overdue = %v", name, got)
		}
	}
	// One that has never been read is not overdue: nothing is known of it.
	if (State{}).Overdue(now) {
		t.Fatal("a certificate nobody has seen was called overdue")
	}
}

func TestBeingToldIsAboutOneCertificate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	srv := f.server("ams-1", "cdn.example.net")
	f.leaf = &Leaf{NotBefore: f.now.Add(-80 * 24 * time.Hour), NotAfter: f.now.Add(10 * 24 * time.Hour)}
	f.watcher.RunOnce(ctx)
	if f.cert(srv.ID).Told() {
		t.Fatal("told before anyone was")
	}
	if err := f.store.MarkTold(ctx, srv.ID, f.leaf.NotAfter); err != nil {
		t.Fatal(err)
	}
	// Looking again does not forget what was said.
	f.watcher.RunOnce(ctx)
	if !f.cert(srv.ID).Told() {
		t.Fatal("a look at the same certificate forgot that the administrator was told")
	}
	// A renewal makes it another certificate.
	f.leaf = &Leaf{NotBefore: f.now, NotAfter: f.now.Add(90 * 24 * time.Hour)}
	f.watcher.RunOnce(ctx)
	if f.cert(srv.ID).Told() {
		t.Fatal("the next certificate counts as told about already")
	}
	// The server leaves the panel, and its certificate with it.
	if err := f.servers.Delete(ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := f.store.List(ctx); len(list) != 0 {
		t.Fatalf("a removed server's certificate stayed: %+v", list)
	}
}

func TestReadJudgesWhatItIsShownWithoutRefusingToLook(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "https://")
	trusted := x509.NewCertPool()
	trusted.AddCert(server.Certificate())
	ctx := context.Background()

	// The test server's certificate is for example.com.
	leaf, err := Read(ctx, address, "example.com", trusted)
	if err != nil || leaf.Problem != "" || leaf.NotAfter.IsZero() {
		t.Fatalf("a certificate that is good for the name: %+v %v", leaf, err)
	}
	// For another name, or signed by nobody a visitor trusts: still read,
	// and said to be no good.
	for name, roots := range map[string]*x509.CertPool{"other.example.net": trusted, "example.com": x509.NewCertPool()} {
		leaf, err := Read(ctx, address, name, roots)
		if err != nil || leaf.Problem == "" || !leaf.NotAfter.Equal(server.Certificate().NotAfter) {
			t.Fatalf("%s: %+v %v", name, leaf, err)
		}
	}
	server.Close()
	if _, err := Read(ctx, address, "example.com", trusted); err == nil {
		t.Fatal("a server that is not there showed a certificate")
	}
}
