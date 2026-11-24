package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"xray-console/internal/ops"
	"xray-console/internal/sshx"
	"xray-console/internal/xray"
)

// A change that is only about who a server lets in and where their traffic
// goes — a subscription more, one that ran out, an edited rule — is given to
// the Xray that is running, through its own API on the server's loopback,
// instead of restarting it: a restart drops every connection of every client
// of that server. Xray's command-line client does the talking, the same
// binary that runs as the service.

// liveTimeout is how long one of Xray's API commands may take, in seconds.
// Its own default is three, which a list of users of any length outgrows.
const liveTimeout = "30"

// xrayAPI builds a command line for Xray's API client.
func xrayAPI(command string, args ...string) string {
	line := xray.BinaryPath + " api " + command + " --server=" + xray.APIListen + " -timeout=" + liveTimeout
	for _, arg := range args {
		line += " " + arg
	}
	return line
}

// applyLive puts a change on the running Xray. It returns an error when the
// change is not known to be there in full afterwards; the caller then
// restarts Xray, which reads the whole configuration from the file.
func (s *session) applyLive(ctx context.Context, run *ops.Run, change *xray.LiveChange) error {
	client, err := s.connect(ctx)
	if err != nil {
		return err
	}
	if change.Empty() {
		return nil
	}
	// A directory only root can enter: the file of users holds their ids.
	if _, err := s.quietSudo(ctx, "install -d -m 0700 "+checkDir); err != nil {
		return err
	}
	base := checkDir + "/live-" + strconv.FormatInt(s.inst.now().UnixNano(), 10)
	usersFile, rulesFile := base+"-users.json", base+"-rules.json"
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = s.quietSudo(cleanup, "rm -f "+usersFile+" "+rulesFile)
	}()

	// In this order. Those who are to go, first: nothing below should still
	// apply to them. Then the rules, which name the users they are for — a
	// name nobody has yet does no harm, while a user let in before the rules
	// know them would be sent straight out, past the rules of their set.
	if len(change.Remove) > 0 {
		emails := make([]string, 0, len(change.Remove))
		for _, email := range change.Remove {
			emails = append(emails, sshx.Quote(email))
		}
		if _, err := s.sudo(ctx, run, xrayAPI("rmu", append([]string{"-tag=" + xray.InboundTag}, emails...)...)); err != nil {
			return err
		}
	}
	if change.Rules != nil {
		if err := client.WriteFile(ctx, rulesFile, 0o600, bytes.NewReader(change.Rules)); err != nil {
			return err
		}
		if _, err := s.sudo(ctx, run, xrayAPI("adrules", rulesFile)); err != nil {
			return err
		}
	}
	if change.Add != nil {
		if err := client.WriteFile(ctx, usersFile, 0o600, bytes.NewReader(change.Add)); err != nil {
			return err
		}
		if _, err := s.sudo(ctx, run, xrayAPI("adu", usersFile)); err != nil {
			return err
		}
	}

	// Xray's client reports a user it could not add or remove in words and
	// exits as if all were well. So the running Xray is asked who it lets
	// in, and that has to be exactly who it should.
	listed, err := s.quietSudo(ctx, xrayAPI("inbounduser", "-tag="+xray.InboundTag))
	if err != nil {
		return err
	}
	return sameUsers(listed, change.Users)
}

// sameUsers compares what a running Xray says about its inbound's users with
// who should be there: the same emails, each with the same id.
func sameUsers(listed string, want map[string]string) error {
	var answer struct {
		Users []struct {
			Email   string `json:"email"`
			Account struct {
				ID string `json:"id"`
			} `json:"account"`
		} `json:"users"`
	}
	if err := json.Unmarshal([]byte(listed), &answer); err != nil {
		return errors.New("Xray's list of its users could not be read")
	}
	have := make(map[string]string, len(answer.Users))
	for _, user := range answer.Users {
		have[user.Email] = user.Account.ID
	}
	var missing, extra []string
	for email, id := range want {
		if have[email] != id {
			missing = append(missing, email)
		}
	}
	for email := range have {
		if _, wanted := want[email]; !wanted {
			extra = append(extra, email)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return fmt.Errorf("the running Xray does not let in exactly who it should: missing %s; left over %s",
		named(missing), named(extra))
}

// named lists a few names: an email is not a secret, but a log line that
// holds a thousand of them is of no use to anyone.
func named(names []string) string {
	const shown = 5
	switch {
	case len(names) == 0:
		return "nobody"
	case len(names) > shown:
		return strings.Join(names[:shown], ", ") + fmt.Sprintf(" and %d more", len(names)-shown)
	}
	return strings.Join(names, ", ")
}

// liveDetail says what a change without a restart came to, for the step.
func liveDetail(change *xray.LiveChange) string {
	if change.Empty() {
		return "nothing to change · no restart"
	}
	var parts []string
	if change.Added > 0 || len(change.Remove) > 0 {
		parts = append(parts, fmt.Sprintf("users +%d −%d", change.Added, len(change.Remove)))
	}
	if change.Rules != nil {
		parts = append(parts, "rules replaced")
	}
	return strings.Join(append(parts, "no restart"), " · ")
}
