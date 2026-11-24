package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xray-console/internal/secrets"
	"xray-console/internal/store"
)

const goodPassword = "correct horse battery"

type fixture struct {
	*Service
	clock time.Time
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
	f := &fixture{Service: NewService(db, box), clock: time.Unix(1_800_000_000, 0)}
	f.SetClock(func() time.Time { return f.clock })
	return f
}

// setup creates the administrator and returns the TOTP enrollment, if any.
func (f *fixture) setup(t *testing.T, totp bool) *TOTPEnrollment {
	t.Helper()
	ctx := context.Background()
	token, err := f.SetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := f.CreateAdmin(ctx, SetupInput{
		Token: token, Username: "admin", Password: goodPassword,
		PanelURL: "https://console.example.net/", EnableTOTP: totp,
	})
	if err != nil {
		t.Fatal(err)
	}
	return enrollment
}

func (f *fixture) login(password, code string) (*Session, error) {
	return f.Login(context.Background(), LoginInput{
		Username: "admin", Password: password, Code: code, IP: "203.0.113.80",
	})
}

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyPassword(hash, goodPassword); err != nil || !ok {
		t.Fatalf("right password rejected: ok=%v err=%v", ok, err)
	}
	if ok, _ := VerifyPassword(hash, goodPassword+"x"); ok {
		t.Fatal("wrong password accepted")
	}
	if _, err := VerifyPassword("not-a-hash", goodPassword); err == nil {
		t.Fatal("malformed hash accepted")
	}
}

// RFC 6238 appendix B, SHA-1 rows, truncated to the six digits apps show.
func TestTOTPMatchesRFC6238(t *testing.T) {
	secret := []byte("12345678901234567890")
	for unix, want := range map[int64]string{
		59:          "287082",
		1111111109:  "081804",
		1234567890:  "005924",
		20000000000: "353130",
	} {
		if got := totpCode(secret, uint64(unix)/totpPeriod); got != want {
			t.Errorf("t=%d: got %s, want %s", unix, got, want)
		}
	}
}

func TestSetupNeedsTheTokenAndHappensOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	token, err := f.SetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	in := SetupInput{Token: "wrong", Username: "admin", Password: goodPassword, PanelURL: "https://console.example.net"}

	if _, err := f.CreateAdmin(ctx, in); !errors.Is(err, ErrBadSetupToken) {
		t.Fatalf("wrong token: got %v", err)
	}
	in.Token = token
	in.Password = "short"
	if _, err := f.CreateAdmin(ctx, in); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("short password: got %v", err)
	}
	in.Password = goodPassword
	if _, err := f.CreateAdmin(ctx, in); err != nil {
		t.Fatal(err)
	}
	if required, _ := f.SetupRequired(ctx); required {
		t.Fatal("setup still required after creating the administrator")
	}
	if _, err := f.CreateAdmin(ctx, in); !errors.Is(err, ErrAlreadySetUp) {
		t.Fatalf("second setup: got %v", err)
	}
}

func TestNormalizePanelURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://Console.Example.net/":     "https://console.example.net",
		" http://10.0.0.5:8080 ":           "http://10.0.0.5:8080",
		"console.example.net":              "",
		"https://console.example.net/path": "",
		"ftp://console.example.net":        "",
		"https://user@console.example.net": "",
	} {
		got, err := NormalizePanelURL(raw)
		if want == "" {
			if err == nil {
				t.Errorf("%q: accepted as %q", raw, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", raw, got, err, want)
		}
	}
}

func TestLockoutCountsDownThenBlocksThenLifts(t *testing.T) {
	f := newFixture(t)
	f.setup(t, false)

	for attempt := 1; attempt < MaxAttempts; attempt++ {
		_, err := f.login("wrong password!", "")
		var invalid *InvalidCredentialsError
		if !errors.As(err, &invalid) {
			t.Fatalf("attempt %d: got %v", attempt, err)
		}
		if want := MaxAttempts - attempt; invalid.AttemptsLeft != want {
			t.Fatalf("attempt %d: %d attempts left, want %d", attempt, invalid.AttemptsLeft, want)
		}
	}

	var locked *LockedError
	if _, err := f.login("wrong password!", ""); !errors.As(err, &locked) {
		t.Fatalf("last attempt: got %v, want a lock", err)
	}
	// The right password does not get through a lock — otherwise the lock
	// would only slow guessing down, not stop it.
	if _, err := f.login(goodPassword, ""); !errors.As(err, &locked) {
		t.Fatalf("right password while locked: got %v", err)
	}
	// Another address is unaffected.
	if _, err := f.Login(context.Background(), LoginInput{
		Username: "admin", Password: goodPassword, IP: "198.51.100.7",
	}); err != nil {
		t.Fatalf("other address: %v", err)
	}

	f.clock = f.clock.Add(LockDuration + time.Second)
	if _, err := f.login(goodPassword, ""); err != nil {
		t.Fatalf("after the lock expired: %v", err)
	}
}

func TestSuccessfulLoginClearsEarlierFailures(t *testing.T) {
	f := newFixture(t)
	f.setup(t, false)

	for range MaxAttempts - 1 {
		f.login("wrong password!", "")
	}
	if _, err := f.login(goodPassword, ""); err != nil {
		t.Fatal(err)
	}
	_, err := f.login("wrong password!", "")
	var invalid *InvalidCredentialsError
	if !errors.As(err, &invalid) || invalid.AttemptsLeft != MaxAttempts-1 {
		t.Fatalf("got %v, want a fresh count", err)
	}
	if invalid.PreviousLogin.IsZero() {
		t.Fatal("previous successful sign-in not reported")
	}
}

func TestWrongUsernameIsIndistinguishable(t *testing.T) {
	f := newFixture(t)
	f.setup(t, false)
	_, err := f.Login(context.Background(), LoginInput{Username: "root", Password: goodPassword, IP: "203.0.113.80"})
	var invalid *InvalidCredentialsError
	if !errors.As(err, &invalid) {
		t.Fatalf("got %v", err)
	}
}

func TestTOTPEnrollmentAndLogin(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	enrollment := f.setup(t, true)
	if enrollment == nil {
		t.Fatal("no enrollment returned")
	}
	secret, err := base32NoPad.DecodeString(enrollment.Secret)
	if err != nil {
		t.Fatal(err)
	}
	codeNow := func() string { return totpCode(secret, uint64(f.clock.Unix())/totpPeriod) }

	// Until a code is confirmed, the password alone is enough: a secret nobody
	// has scanned must not lock the administrator out.
	if _, err := f.login(goodPassword, ""); err != nil {
		t.Fatalf("before confirmation: %v", err)
	}

	if err := f.ConfirmTOTP(ctx, "000000"); !errors.Is(err, ErrTOTPInvalid) {
		t.Fatalf("wrong confirmation code: got %v", err)
	}
	if err := f.ConfirmTOTP(ctx, codeNow()); err != nil {
		t.Fatal(err)
	}
	if err := f.ConfirmTOTP(ctx, codeNow()); !errors.Is(err, ErrTOTPNotPending) {
		t.Fatalf("second confirmation: got %v", err)
	}

	if _, err := f.login(goodPassword, ""); !errors.Is(err, ErrTOTPRequired) {
		t.Fatalf("no code: got %v", err)
	}
	// The code that confirmed enrollment has been spent.
	var invalid *InvalidCredentialsError
	if _, err := f.login(goodPassword, codeNow()); !errors.As(err, &invalid) {
		t.Fatalf("reused code: got %v", err)
	}

	f.clock = f.clock.Add(totpPeriod * time.Second)
	code := codeNow()
	if _, err := f.login(goodPassword, code[:3]+" "+code[3:]); err != nil {
		t.Fatalf("fresh code: %v", err)
	}
}

func TestSessionLifetimes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.setup(t, false)

	short, err := f.CreateSession(ctx, "203.0.113.80", "test", false)
	if err != nil {
		t.Fatal(err)
	}
	long, err := f.CreateSession(ctx, "203.0.113.80", "test", true)
	if err != nil {
		t.Fatal(err)
	}
	alive := func(s *Session) bool {
		ok, err := f.Authenticate(ctx, s.Token)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	// Activity keeps a not-remembered session going past its first deadline.
	f.clock = f.clock.Add(SessionIdle - time.Hour)
	if !alive(short) {
		t.Fatal("session ended early")
	}
	f.clock = f.clock.Add(SessionIdle - time.Hour)
	if !alive(short) {
		t.Fatal("activity did not extend the session")
	}
	// Idle for the whole window ends it.
	f.clock = f.clock.Add(SessionIdle + time.Minute)
	if alive(short) {
		t.Fatal("idle session survived")
	}
	if !alive(long) {
		t.Fatal("remembered session ended with the idle one")
	}

	// A remembered session has a fixed end, however much it is used.
	f.clock = short.ExpiresAt.Add(SessionRemember)
	if alive(long) {
		t.Fatal("remembered session outlived 30 days")
	}

	if ok, _ := f.Authenticate(ctx, "no-such-token"); ok {
		t.Fatal("unknown token accepted")
	}
}

func TestResetPassword(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.setup(t, false)

	session, err := f.login(goodPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	for range MaxAttempts {
		f.login("wrong password!", "")
	}

	const newPassword = "an entirely new one"
	if err := f.ResetPassword(ctx, newPassword, false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := f.Authenticate(ctx, session.Token); ok {
		t.Fatal("old session survived the reset")
	}
	// The lock is lifted too: the reset is what a locked-out owner reaches for.
	if _, err := f.login(newPassword, ""); err != nil {
		t.Fatalf("new password: %v", err)
	}
	var invalid *InvalidCredentialsError
	if _, err := f.login(goodPassword, ""); !errors.As(err, &invalid) {
		t.Fatalf("old password: got %v", err)
	}
}

func TestABurstOfAttemptsDoesNotGetPastTheLimit(t *testing.T) {
	f := newFixture(t)
	f.setup(t, false)

	// All at once, as a script would send them: the limit is on attempts, not
	// on attempts that happened to arrive one after another.
	const burst = 4 * MaxAttempts
	var wg sync.WaitGroup
	var checked, locked atomic.Int32
	for range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.login("wrong password!", "")
			var invalid *InvalidCredentialsError
			var lock *LockedError
			switch {
			case errors.As(err, &invalid):
				checked.Add(1)
			case errors.As(err, &lock):
				locked.Add(1)
			default:
				t.Errorf("got %v", err)
			}
		}()
	}
	wg.Wait()
	// The last allowed attempt is itself answered with the lock.
	if n := checked.Load(); n != MaxAttempts-1 {
		t.Fatalf("%d attempts were answered \"wrong password\", want %d", n, MaxAttempts-1)
	}
	if n := locked.Load(); n != burst-MaxAttempts+1 {
		t.Fatalf("%d attempts were locked out, want %d", n, burst-MaxAttempts+1)
	}
}

func TestSetupThatCannotSucceedCostsNoHash(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	token, err := f.SetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	input := SetupInput{Username: "admin", Password: goodPassword, PanelURL: "https://console.example.net/"}

	// With every hashing slot taken, a request that hashed would wait here
	// for good; one turned away first comes straight back.
	answer := func(in SetupInput) error {
		for range cap(argonSlots) {
			argonSlots <- struct{}{}
		}
		defer func() {
			for range cap(argonSlots) {
				<-argonSlots
			}
		}()
		done := make(chan error, 1)
		go func() {
			_, err := f.CreateAdmin(ctx, in)
			done <- err
		}()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("the request went on to hash the password")
			return nil
		}
	}

	wrong := input
	wrong.Token = "not the token"
	if err := answer(wrong); !errors.Is(err, ErrBadSetupToken) {
		t.Fatalf("wrong token: got %v", err)
	}
	f.setup(t, false)
	again := input
	again.Token = token
	if err := answer(again); !errors.Is(err, ErrAlreadySetUp) {
		t.Fatalf("after setup: got %v", err)
	}
}

func TestASessionKeptAliveStillEndsAfterAWeek(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.setup(t, false)
	session, err := f.CreateSession(ctx, "203.0.113.80", "test", false)
	if err != nil {
		t.Fatal(err)
	}
	started := f.clock
	// Used every few hours, as a stolen cookie in a script would be.
	for f.clock.Sub(started) < SessionMax-SessionIdle {
		f.clock = f.clock.Add(SessionIdle / 2)
		if ok, _ := f.Authenticate(ctx, session.Token); !ok {
			t.Fatalf("session ended after %s of use", f.clock.Sub(started))
		}
	}
	for f.clock.Sub(started) <= SessionMax {
		f.clock = f.clock.Add(SessionIdle / 2)
		_, _ = f.Authenticate(ctx, session.Token)
	}
	if ok, _ := f.Authenticate(ctx, session.Token); ok {
		t.Fatalf("a session in use is still alive after %s", f.clock.Sub(started))
	}
}

func TestFailuresAreCountedAcrossAnIPv6Network(t *testing.T) {
	f := newFixture(t)
	f.setup(t, false)
	attempt := func(ip, password string) error {
		_, err := f.Login(context.Background(), LoginInput{Username: "admin", Password: password, IP: ip})
		return err
	}
	// A new address out of the same /64 for every attempt: one customer.
	for i := 1; i < MaxAttempts; i++ {
		var invalid *InvalidCredentialsError
		if err := attempt(fmt.Sprintf("2001:db8:1:2::%x", i), "wrong password!"); !errors.As(err, &invalid) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	var locked *LockedError
	if err := attempt("2001:db8:1:2::ff", "wrong password!"); !errors.As(err, &locked) {
		t.Fatalf("the attempt past the limit, from a fresh address of the same network: %v", err)
	}
	if err := attempt("2001:db8:1:2:aaaa::1", goodPassword); !errors.As(err, &locked) {
		t.Fatalf("the right password from the locked network: %v", err)
	}
	// The network next door is somebody else.
	if err := attempt("2001:db8:1:3::1", goodPassword); err != nil {
		t.Fatalf("another network: %v", err)
	}
}

const nextPassword = "a different battery staple"

func TestChangingThePasswordNeedsTheOldOneAndSignsOtherBrowsersOut(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.setup(t, false)
	here, err := f.CreateSession(ctx, "203.0.113.80", "this browser", false)
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, err := f.CreateSession(ctx, "198.51.100.7", "another browser", true)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.ChangePassword(ctx, "not the password!", nextPassword, here.Token); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong current password: %v", err)
	}
	if err := f.ChangePassword(ctx, goodPassword, "short", here.Token); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("a password too short: %v", err)
	}
	// Nothing changed yet, nobody was signed out.
	if ok, _ := f.Authenticate(ctx, elsewhere.Token); !ok {
		t.Fatal("a refused change signed a browser out")
	}

	if err := f.ChangePassword(ctx, goodPassword, nextPassword, here.Token); err != nil {
		t.Fatal(err)
	}
	if ok, _ := f.Authenticate(ctx, here.Token); !ok {
		t.Fatal("the browser that changed the password was signed out")
	}
	// Whoever else held a session got it with the old password.
	if ok, _ := f.Authenticate(ctx, elsewhere.Token); ok {
		t.Fatal("another browser is still signed in after the password changed")
	}
	var invalid *InvalidCredentialsError
	if _, err := f.login(goodPassword, ""); !errors.As(err, &invalid) {
		t.Fatalf("the old password: %v", err)
	}
	if _, err := f.login(nextPassword, ""); err != nil {
		t.Fatalf("the new password: %v", err)
	}
}

func TestTwoFactorSignInIsTurnedOnAndOffFromTheAccount(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.setup(t, false)

	if _, err := f.BeginTOTP(ctx, "not the password!"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("a secret asked for with the wrong password: %v", err)
	}
	if err := f.DisableTOTP(ctx, goodPassword, ""); !errors.Is(err, ErrTOTPOff) {
		t.Fatalf("turning off what is not on: %v", err)
	}
	enrollment, err := f.BeginTOTP(ctx, goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := base32NoPad.DecodeString(enrollment.Secret)
	if err != nil {
		t.Fatal(err)
	}
	codeNow := func() string { return totpCode(secret, uint64(f.clock.Unix())/totpPeriod) }

	// Issued and not yet confirmed: the password alone still signs in.
	if _, err := f.login(goodPassword, ""); err != nil {
		t.Fatalf("before the code was confirmed: %v", err)
	}
	if err := f.ConfirmTOTP(ctx, codeNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.login(goodPassword, ""); !errors.Is(err, ErrTOTPRequired) {
		t.Fatalf("after turning it on, without a code: %v", err)
	}
	if _, err := f.BeginTOTP(ctx, goodPassword); !errors.Is(err, ErrTOTPAlreadyOn) {
		t.Fatalf("a second secret while one is in use: %v", err)
	}

	// Off takes both the password and a code: it is what an intruder with a
	// stolen session would do first.
	f.clock = f.clock.Add(totpPeriod * time.Second)
	if err := f.DisableTOTP(ctx, "not the password!", codeNow()); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("off with the wrong password: %v", err)
	}
	if err := f.DisableTOTP(ctx, goodPassword, "000000"); !errors.Is(err, ErrTOTPInvalid) {
		t.Fatalf("off with a wrong code: %v", err)
	}
	if err := f.DisableTOTP(ctx, goodPassword, codeNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.login(goodPassword, ""); err != nil {
		t.Fatalf("after turning it off: %v", err)
	}
}

func TestSessionsAreListedAndEndedOneByOneOrAllButThisOne(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.setup(t, false)
	here, _ := f.CreateSession(ctx, "203.0.113.80", "this browser", false)
	f.clock = f.clock.Add(time.Minute)
	phone, _ := f.CreateSession(ctx, "198.51.100.7", "phone", true)
	f.clock = f.clock.Add(time.Minute)
	old, _ := f.CreateSession(ctx, "198.51.100.9", "old laptop", false)

	list, err := f.Sessions(ctx, here.Token)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("%d sessions listed, want 3", len(list))
	}
	byAgent := map[string]SessionInfo{}
	for _, session := range list {
		byAgent[session.UserAgent] = session
		// The id names a session; it is not a way into one.
		if len(session.ID) != sessionIDLength || strings.Contains(here.Token+phone.Token+old.Token, session.ID) {
			t.Fatalf("session id %q", session.ID)
		}
	}
	if !byAgent["this browser"].Current || byAgent["phone"].Current || !byAgent["phone"].Remember {
		t.Fatalf("sessions: %+v", list)
	}

	if err := f.RevokeSession(ctx, byAgent["phone"].ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := f.Authenticate(ctx, phone.Token); ok {
		t.Fatal("an ended session still signs in")
	}
	if err := f.RevokeSession(ctx, byAgent["phone"].ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("ending a session twice: %v", err)
	}
	if err := f.RevokeSession(ctx, "%"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("something that is not an id: %v", err)
	}

	ended, err := f.RevokeOtherSessions(ctx, here.Token)
	if err != nil || ended != 1 {
		t.Fatalf("ended %d other sessions, %v; want 1", ended, err)
	}
	if ok, _ := f.Authenticate(ctx, old.Token); ok {
		t.Fatal("another session survived \"sign out everywhere else\"")
	}
	if ok, _ := f.Authenticate(ctx, here.Token); !ok {
		t.Fatal("this session was ended with the others")
	}
}
