package plan

import (
	"strings"
	"testing"
	"time"

	"xray-console/internal/servers"
	"xray-console/internal/templates"
	"xray-console/internal/xray"
)

func fixture(t *testing.T) (*servers.Server, *templates.Template) {
	t.Helper()
	inbound, err := xray.NewInbound("www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	srv := &servers.Server{ID: 1, Name: "ams-1", Inbound: inbound, Overrides: map[string]string{}}
	tpl := &templates.Template{ID: 7, Name: "edge", Latest: &templates.Version{
		Version: 3, Values: templates.Defaults(), Overridable: []string{templates.FieldMode, templates.FieldPadding},
	}}
	return srv, tpl
}

func TestOverridesApplyOnlyWhereTheTemplateAllows(t *testing.T) {
	srv, tpl := fixture(t)
	srv.Overrides[templates.FieldMode] = "stream-up"
	srv.Overrides[templates.FieldPath] = "/mine" // the template does not open this up

	p, err := Build(srv, tpl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Fields[templates.FieldMode] != "stream-up" || p.Sources[templates.FieldMode] != SourceOverride {
		t.Fatalf("mode: %q from %q", p.Fields[templates.FieldMode], p.Sources[templates.FieldMode])
	}
	if p.Fields[templates.FieldPath] != "/api/v2/stream" || p.Sources[templates.FieldPath] != SourceTemplate {
		t.Fatalf("path: %q from %q", p.Fields[templates.FieldPath], p.Sources[templates.FieldPath])
	}
	if len(p.Overrides) != 1 {
		t.Fatalf("effective overrides: %v", p.Overrides)
	}
	config := string(p.Config)
	for _, want := range []string{`"mode": "stream-up"`, `"path": "/api/v2/stream"`, `"xPaddingBytes": "100-1000"`} {
		if !strings.Contains(config, want) {
			t.Errorf("config lacks %s", want)
		}
	}
	// The stored settings are untouched: the plan works on a copy.
	if srv.Inbound.XHTTPMode != "auto" {
		t.Fatalf("Build changed the server's own inbound: %q", srv.Inbound.XHTTPMode)
	}
}

func TestChangedNamesExactlyWhatDiffers(t *testing.T) {
	srv, tpl := fixture(t)
	first, _ := Build(srv, tpl, nil)
	if got := first.Changed(nil); len(got) != len(first.Fields) {
		t.Fatalf("with nothing applied, %d fields changed: %v", len(got), got)
	}

	srv.Applied = first.Snapshot(srv, time.Unix(1_800_000_000, 0))
	if got := first.Changed(srv.Applied); len(got) != 0 {
		t.Fatalf("right after applying: %v", got)
	}

	// A server-level edit, a template-level edit, and a key rotation.
	srv.Overrides[templates.FieldPadding] = "200-1200"
	tpl.Latest.Values[templates.FieldPath] = "/v3"
	if err := srv.Inbound.RotateKeys(); err != nil {
		t.Fatal(err)
	}
	second, _ := Build(srv, tpl, nil)
	got := strings.Join(second.Changed(srv.Applied), " ")
	if want := "xhttp.path xhttp.xPaddingBytes reality.publicKey reality.shortIds"; got != want {
		t.Fatalf("changed %q, want %q", got, want)
	}
	// The snapshot was not affected by editing the live server afterwards.
	if srv.Applied.Inbound.PublicKey == srv.Inbound.PublicKey || srv.Applied.Overrides[templates.FieldPadding] != "" {
		t.Fatal("the applied snapshot shares state with the server")
	}
}

func TestMaskSecretsHidesTheKeyButShowsARotation(t *testing.T) {
	srv, tpl := fixture(t)
	before, _ := Build(srv, tpl, nil)
	oldKey := srv.Inbound.PrivateKey
	if err := srv.Inbound.RotateKeys(); err != nil {
		t.Fatal(err)
	}
	after, _ := Build(srv, tpl, nil)

	maskedBefore, maskedAfter := MaskSecrets(string(before.Config)), MaskSecrets(string(after.Config))
	if strings.Contains(maskedBefore, oldKey) || strings.Contains(maskedAfter, srv.Inbound.PrivateKey) {
		t.Fatal("a private key survived masking")
	}
	if maskedBefore == maskedAfter {
		t.Fatal("a key rotation is invisible after masking")
	}
	diff := Diff(maskedBefore, maskedAfter)
	var changed []string
	for _, line := range diff {
		if line.Op == "-" || line.Op == "+" {
			changed = append(changed, line.Text)
		}
	}
	if len(changed) == 0 || !strings.Contains(strings.Join(changed, "\n"), "privateKey") {
		t.Fatalf("diff does not show the key line: %v", changed)
	}
}

func TestDiff(t *testing.T) {
	if got := Diff("a\nb", "a\nb"); len(got) != 0 {
		t.Fatalf("equal texts: %v", got)
	}
	before := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10"
	after := "1\n2\n3\n4\nfive\n6\n7\n8\n9\nten"
	var rendered []string
	for _, line := range Diff(before, after) {
		rendered = append(rendered, line.Op+line.Text)
	}
	// Two lines of context each side; the two hunks' contexts meet at 7 and 8,
	// so there is no gap between them.
	want := " 3| 4|-5|+five| 6| 7| 8| 9|-10|+ten"
	if got := strings.Join(rendered, "|"); got != want {
		t.Fatalf("diff\n got %s\nwant %s", got, want)
	}

	far := Diff("a\n1\n2\n3\n4\n5\n6\nz", "A\n1\n2\n3\n4\n5\n6\nZ")
	gaps := 0
	for _, line := range far {
		if line.Op == "@" {
			gaps++
		}
	}
	if gaps != 1 {
		t.Fatalf("distant changes should be separated by one gap, got %d: %v", gaps, far)
	}
}

func TestADomainOfItsOwnIsAChangeAndItsAbsenceIsNot(t *testing.T) {
	srv, tpl := fixture(t)
	first, _ := Build(srv, tpl, nil)
	srv.Applied = first.Snapshot(srv, time.Unix(1_800_000_000, 0))

	// A snapshot from before servers could have a domain says nothing about
	// one, which reads as "none": such a server has no change waiting.
	delete(srv.Applied.Fields, FieldDomain)
	if got := first.Changed(srv.Applied); len(got) != 0 {
		t.Fatalf("a server applied before domains existed shows changes: %v", got)
	}

	// The same name, but now answered by a stub site on the server: the name
	// REALITY borrows has not changed, where it borrows it from has.
	if err := srv.Inbound.SetDomain("www.example.com"); err != nil {
		t.Fatal(err)
	}
	second, _ := Build(srv, tpl, nil)
	if got := strings.Join(second.Changed(srv.Applied), " "); got != FieldDomain {
		t.Fatalf("changed %q, want %q", got, FieldDomain)
	}
	if !strings.Contains(string(second.Config), `"dest": "`+xray.StubListen+`"`) {
		t.Fatal("the configuration does not send strangers to the stub site")
	}
	// What the server runs is kept apart from what it should run.
	running, err := RunningInbound(srv.Applied)
	if err != nil || running.Domain != "" || running.Dest != "www.example.com:443" {
		t.Fatalf("running inbound: %+v %v", running, err)
	}

	srv.Applied = second.Snapshot(srv, time.Unix(1_800_000_100, 0))
	if err := srv.Inbound.SetDomain("cdn.example.net"); err != nil {
		t.Fatal(err)
	}
	third, _ := Build(srv, tpl, nil)
	if got := strings.Join(third.Changed(srv.Applied), " "); got != FieldMaskHost+" "+FieldDomain {
		t.Fatalf("another domain changed %q", got)
	}
	if running, _ := RunningInbound(srv.Applied); running.Domain != "www.example.com" {
		t.Fatalf("the running domain: %q", running.Domain)
	}
}
