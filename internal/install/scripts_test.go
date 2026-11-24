package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionCheckFailsForABinaryThatDoesNotRun(t *testing.T) {
	dir := t.TempDir()
	script := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}

	working := script("xray", `echo "Xray 26.1.2 (Xray, Penetrates Everything.)"; echo "A unified platform"`)
	out, err := exec.Command("sh", "-c", versionCheck(working)).CombinedOutput()
	if err != nil {
		t.Fatalf("a binary that runs was refused: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "Xray 26.1.2 (Xray, Penetrates Everything.)" {
		t.Fatalf("printed %q, want the first line only", got)
	}

	// Says something, then fails — as a binary for another CPU does through
	// the loader. Piped into head this used to pass.
	broken := script("xray-broken", `echo "cannot execute binary file"; exit 126`)
	if out, err := exec.Command("sh", "-c", versionCheck(broken)).CombinedOutput(); err == nil {
		t.Fatalf("a binary that does not run passed the check: %s", out)
	}
	if out, err := exec.Command("sh", "-c", versionCheck(filepath.Join(dir, "missing"))).CombinedOutput(); err == nil {
		t.Fatalf("a missing binary passed the check: %s", out)
	}
}
