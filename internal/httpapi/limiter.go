package httpapi

import (
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// limiter allows each address a burst of requests and then a steady trickle.
// It stands in front of what can be asked without signing in: creating the
// administrator, signing in, the subscription link. Each of those costs
// something — a password hash, a database write — and without a limit anyone
// can ask for it as fast as the line carries.
type limiter struct {
	burst float64
	every time.Duration // one more request is allowed this often

	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter(burst int, every time.Duration) *limiter {
	return &limiter{burst: float64(burst), every: every, buckets: map[string]*bucket{}}
}

// allow reports whether key may make a request now, and if not, how long
// until it may.
func (l *limiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Addresses that stopped asking are forgotten, so the map is as large as
	// the number of addresses asking lately, not ever.
	if now.Sub(l.swept) > time.Minute {
		l.swept = now
		idle := time.Duration(l.burst) * l.every
		for k, b := range l.buckets {
			if now.Sub(b.at) > idle {
				delete(l.buckets, k)
			}
		}
	}

	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	b.tokens += float64(now.Sub(b.at)) / float64(l.every)
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.at = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) * float64(l.every))
	}
	b.tokens--
	return true, 0
}

// addressKey is what an address is counted under. An IPv6 customer is handed
// a whole /64 and can use a new address out of it for every request, so those
// are counted as the one customer they are.
func addressKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return addr.String()
	}
	return prefix.String()
}

// limited wraps a handler that can be reached without signing in.
func (s *Server) limited(l *limiter, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, wait := l.allow(addressKey(s.clientIP(r)), time.Now())
		if !ok {
			seconds := int(wait/time.Second) + 1
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests from this address.",
				map[string]any{"retry_after_seconds": seconds})
			return
		}
		next(w, r)
	}
}
