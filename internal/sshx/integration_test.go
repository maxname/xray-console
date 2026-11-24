//go:build integration

package sshx

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestIntegrationOutputIsNeverLost(t *testing.T) {
	host := os.Getenv("XC_TEST_NODE")
	if host == "" {
		t.Skip("XC_TEST_NODE is not set")
	}
	ctx := context.Background()
	target := Target{Host: host, Port: 22, User: "root"}
	hostKey, err := FetchHostKey(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	client, err := Dial(ctx, target, Auth{Password: "xc-test-root"}, hostKey.Authorized)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	empty := 0
	for i := 0; i < 300; i++ {
		out, err := client.Sudo(ctx, "sha256sum /etc/hostname")
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if !strings.Contains(out, "/etc/hostname") {
			empty++
			t.Logf("run %d: output %q", i, out)
		}
	}
	if empty > 0 {
		t.Fatalf("%d of 300 runs lost their output", empty)
	}
}
