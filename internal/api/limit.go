package api

import (
	"sync"
	"time"
)

// limiter is a per-key token bucket. Keys are remote IPs. remoteIP only reads
// X-Forwarded-For from addresses listed in TRUSTED_PROXIES, so a client that
// spoofs the header cannot mint extra buckets.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64
	burst   float64
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(perMinute int) *limiter {
	if perMinute <= 0 {
		return nil
	}

	return &limiter{
		buckets: make(map[string]*bucket),
		rate:    float64(perMinute) / 60,
		burst:   float64(perMinute),
	}
}

func (l *limiter) Allow(key string) bool {
	if l == nil {
		return true
	}

	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		l.gcLocked(now)
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}

	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}

	b.last = now

	if b.tokens < 1 {
		return false
	}

	b.tokens--

	return true
}

// gcLocked drops buckets that have been idle long enough to refill completely,
// so a scan across many source addresses cannot grow the map without bound.
func (l *limiter) gcLocked(now time.Time) {
	if len(l.buckets) < 1024 {
		return
	}

	stale := time.Duration(float64(time.Second) * (l.burst / l.rate) * 2)
	for k, b := range l.buckets {
		if now.Sub(b.last) > stale {
			delete(l.buckets, k)
		}
	}
}
