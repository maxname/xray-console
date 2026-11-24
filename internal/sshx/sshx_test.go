package sshx

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestOutputIsKeptOnlyUpToTheLimit(t *testing.T) {
	var out lockedBuffer
	chunk := bytes.Repeat([]byte("x"), 1<<20)
	for range 6 {
		n, err := out.Write(chunk)
		// Every write is reported as taken, or the command would stall.
		if n != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(chunk))
		}
	}
	if got := len(out.String()); got != maxOutput {
		t.Fatalf("kept %d bytes, want %d", got, maxOutput)
	}
}

func TestOutputUnderTheLimitIsKeptWhole(t *testing.T) {
	var out lockedBuffer
	for _, part := range []string{"one\n", "two\n"} {
		if _, err := out.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if got := out.String(); got != "one\ntwo\n" {
		t.Fatalf("got %q", got)
	}
}

func TestQuoteSurvivesAnything(t *testing.T) {
	for in, want := range map[string]string{
		"plain":        `'plain'`,
		"it's":         `'it'\''s'`,
		"$(reboot)":    `'$(reboot)'`,
		"a\nb; rm -rf": "'a\nb; rm -rf'",
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

// keysFile makes a home directory with an authorized_keys file in it and
// returns the directory and the file.
func keysFile(t *testing.T, content string) (home, file string) {
	t.Helper()
	home = t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(home, ".ssh", "authorized_keys")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, file
}

// removeKey runs the script as the login user of home would.
func removeKey(t *testing.T, home, line string) error {
	t.Helper()
	cmd := exec.Command("sh", "-c", RemoveAuthorizedKey(line))
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	return cmd.Run()
}

const (
	panelKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPanel xray-console"
	ownKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOwner me@laptop"
)

func TestRemovingAKeyTakesOutThatLineOnly(t *testing.T) {
	home, file := keysFile(t, ownKey+"\n"+panelKey+"\n")
	if err := removeKey(t, home, panelKey); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(file); string(got) != ownKey+"\n" {
		t.Fatalf("left %q", got)
	}
	if _, err := os.Stat(file + ".xc-tmp"); !os.IsNotExist(err) {
		t.Fatal("the working copy was left behind")
	}

	// The panel's key alone: an empty file is the right answer, not a failure.
	home, file = keysFile(t, panelKey+"\n")
	if err := removeKey(t, home, panelKey); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(file); len(got) != 0 {
		t.Fatalf("left %q", got)
	}

	// No file at all is nothing to do.
	if err := removeKey(t, t.TempDir(), panelKey); err != nil {
		t.Fatal(err)
	}
}

func TestRemovingAKeyLeavesTheFileAloneWhenTheCopyFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads and writes whatever the permissions say")
	}
	content := ownKey + "\n" + panelKey + "\n"

	// The file cannot be read: grep fails, and nothing may be written back.
	home, file := keysFile(t, content)
	if err := os.Chmod(file, 0o200); err != nil {
		t.Fatal(err)
	}
	if err := removeKey(t, home, panelKey); err == nil {
		t.Fatal("an unreadable file was reported as done")
	}
	_ = os.Chmod(file, 0o600)
	if got, _ := os.ReadFile(file); string(got) != content {
		t.Fatalf("the owner's keys are gone: %q", got)
	}

	// The copy cannot be made: the same.
	home, file = keysFile(t, content)
	dir := filepath.Dir(file)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := removeKey(t, home, panelKey); err == nil {
		t.Fatal("a copy that could not be made was reported as done")
	}
	if got, _ := os.ReadFile(file); string(got) != content {
		t.Fatalf("the owner's keys are gone: %q", got)
	}
}

// sshServer accepts one connection and signs anyone in. answering decides
// whether it replies to what the client asks afterwards — keepalives — or
// goes silent while keeping the connection open, as a server does whose
// address has just vanished from the network.
func sshServer(t *testing.T, answering bool) (Target, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		conn, chans, reqs, err := ssh.NewServerConn(raw, config)
		if err != nil {
			return
		}
		go func() {
			for refused := range chans {
				_ = refused.Reject(ssh.Prohibited, "no channels here")
			}
		}()
		if answering {
			go ssh.DiscardRequests(reqs)
		}
		_ = conn.Wait()
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	return Target{Host: "127.0.0.1", Port: port, User: "root"}, string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
}

// connect dials the test server the way Dial does, with a keepalive as short
// as a test can wait for.
func connect(t *testing.T, target Target, hostKey string, every, wait time.Duration) *Client {
	t.Helper()
	expected, _, _, _, err := ssh.ParseAuthorizedKey([]byte(hostKey))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dial(context.Background(), target, &ssh.ClientConfig{
		User:            target.User,
		HostKeyCallback: ssh.FixedHostKey(expected),
		Timeout:         DialTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{conn: conn, target: target, closed: make(chan struct{})}
	t.Cleanup(func() { client.Close() })
	go client.keepalive(every, wait)
	return client
}

func closedWithin(client *Client, limit time.Duration) bool {
	gone := make(chan struct{})
	go func() {
		_ = client.conn.Wait()
		close(gone)
	}()
	select {
	case <-gone:
		return true
	case <-time.After(limit):
		return false
	}
}

func TestAServerThatGoesSilentIsHungUpOn(t *testing.T) {
	target, hostKey := sshServer(t, false)
	client := connect(t, target, hostKey, 20*time.Millisecond, 100*time.Millisecond)
	// Nothing comes back, and nothing says the connection is gone: without
	// the keepalive a command on it would wait for ever.
	if !closedWithin(client, 3*time.Second) {
		t.Fatal("the connection to a silent server was kept")
	}
}

func TestAServerThatAnswersIsKept(t *testing.T) {
	target, hostKey := sshServer(t, true)
	client := connect(t, target, hostKey, 20*time.Millisecond, 100*time.Millisecond)
	if closedWithin(client, 400*time.Millisecond) {
		t.Fatal("the connection to a server that answers was closed")
	}
	// Closing it ends the keepalive too, without a second close going wrong.
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
}

func TestDialKeepsWatchOverTheConnection(t *testing.T) {
	target, hostKey := sshServer(t, true)
	client, err := Dial(context.Background(), target, Auth{Password: "anything"}, hostKey)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.closed == nil {
		t.Fatal("a dialled connection has no keepalive")
	}
}
