package httpx

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimiter is an in-memory token bucket per key (client IP, token id…).
// One instance per container is enough for the single-host deployment; buckets
// idle for 10 minutes are dropped so memory stays bounded.
type RateLimiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*bucket
	now     func() time.Time
	lastGC  time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter allows `perMinute` requests per minute with bursts of `burst`.
func NewRateLimiter(perMinute, burst int) *RateLimiter {
	return &RateLimiter{
		rate:    float64(perMinute) / 60,
		burst:   float64(burst),
		buckets: map[string]*bucket{},
		now:     time.Now,
	}
}

// Allow consumes one token for key and reports whether the request may go on.
func (l *RateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastGC) > time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.last) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.lastGC = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Middleware rate-limits by client IP.
func (l *RateLimiter) Middleware(ips *ClientIP) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(ips.Key(r)) {
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int(1/l.rate))))
				WriteError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
