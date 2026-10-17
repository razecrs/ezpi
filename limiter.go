package ezpi

import (
	"sync"
	"time"
)

// limiter is a per-IP token bucket: each IP gets perMinute requests,
// refilled smoothly over the minute.
type limiter struct {
	mu        sync.Mutex
	perMinute float64
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(perMinute int) *limiter {
	if perMinute <= 0 {
		return nil
	}
	return &limiter{perMinute: float64(perMinute), buckets: map[string]*bucket{}, lastSweep: time.Now()}
}

// allow reports whether ip may make a request now, and if not, how long to wait.
func (l *limiter) allow(ip string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	rate := l.perMinute / 60 // tokens per second
	if now.Sub(l.lastSweep) > time.Minute {
		// Drop IPs whose bucket is full again, so memory doesn't grow forever.
		for k, b := range l.buckets {
			if b.tokens+now.Sub(b.last).Seconds()*rate >= l.perMinute {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
	}

	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{tokens: l.perMinute, last: now}
		l.buckets[ip] = b
	}
	b.tokens = min(l.perMinute, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) / rate * float64(time.Second))
	}
	b.tokens--
	return true, 0
}
