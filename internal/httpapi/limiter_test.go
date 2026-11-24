package httpapi

import (
	"net/http"
	"testing"
	"time"
)

func TestLimiterAllowsABurstThenATrickle(t *testing.T) {
	l := newLimiter(3, 10*time.Second)
	now := time.Unix(1_800_000_000, 0)
	for i := range 3 {
		if ok, _ := l.allow("a", now); !ok {
			t.Fatalf("request %d of the burst was refused", i+1)
		}
	}
	ok, wait := l.allow("a", now)
	if ok || wait <= 0 || wait > 10*time.Second {
		t.Fatalf("after the burst: allowed=%v, wait %s", ok, wait)
	}
	// Someone else is not made to wait for it.
	if ok, _ := l.allow("b", now); !ok {
		t.Fatal("another address was refused")
	}
	// One more every ten seconds, and never more than the burst saved up.
	if ok, _ := l.allow("a", now.Add(9*time.Second)); ok {
		t.Fatal("allowed before its turn")
	}
	if ok, _ := l.allow("a", now.Add(11*time.Second)); !ok {
		t.Fatal("refused when its turn had come")
	}
	later := now.Add(time.Hour)
	for i := range 3 {
		if ok, _ := l.allow("a", later); !ok {
			t.Fatalf("request %d after a long quiet was refused", i+1)
		}
	}
	if ok, _ := l.allow("a", later); ok {
		t.Fatal("a long quiet saved up more than the burst")
	}
}

func TestLimiterForgetsAddressesThatStoppedAsking(t *testing.T) {
	l := newLimiter(2, time.Second)
	now := time.Unix(1_800_000_000, 0)
	for _, key := range []string{"a", "b", "c"} {
		l.allow(key, now)
	}
	l.allow("d", now.Add(10*time.Minute))
	if len(l.buckets) != 1 {
		t.Fatalf("%d addresses remembered, want only the one still asking", len(l.buckets))
	}
}

func TestAddressesAreCountedByNetworkForIPv6(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":          "203.0.113.7",
		"::ffff:203.0.113.7":   "203.0.113.7",
		"2001:db8:1:2::1":      "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::9": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":      "2001:db8:1:3::/64",
		"not an address":       "not an address",
	} {
		if got := addressKey(in); got != want {
			t.Errorf("addressKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSignInIsLimitedPerAddress(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.do("POST", "/api/auth/logout", nil)

	// More than anyone types by hand, from one address, at once. The lock
	// answers first; what matters is that past the burst nothing reaches the
	// password check at all.
	body := map[string]any{"username": "nobody", "password": "wrong password!"}
	limited := 0
	for range 30 {
		got := h.do("POST", "/api/auth/login", body)
		if got.status == http.StatusTooManyRequests && got.errorCode() == "rate_limited" {
			limited++
			if got.header.Get("Retry-After") == "" {
				t.Fatal("no Retry-After on a rate limit")
			}
		}
	}
	if limited < 15 {
		t.Fatalf("%d of 30 requests in a row were turned away, want most of them", limited)
	}
}
