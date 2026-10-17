package ezpi

import (
	"net"
	"sync"
	"time"
)

// maxBuckets caps how many clients the limiter tracks, so a flood of
// distinct IPs can't eat all memory. It's a var so tests can lower it.
var maxBuckets = 100_000

// overflowKey is the shared bucket for new clients while the table is full:
// a flood of new IPs is limited as one group, and known clients are unaffected.
const overflowKey = "overflow"

// limiter is a per-client token bucket: each client gets perMinute requests,
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

// limitKey decides which addresses share a bucket. IPv6 users usually get a
// whole /64 block and can switch addresses inside it freely, so the block
// counts as one client. IPv4 addresses count individually.
func limitKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() != nil {
		return ip
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
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
		l.sweep(now, rate)
	}

	key := limitKey(ip)
	b, ok := l.buckets[key]
	if !ok {
		// Table full: try an early sweep (at most once a second, so a flood
		// can't make us sweep on every request), else use the shared bucket.
		if len(l.buckets) >= maxBuckets && now.Sub(l.lastSweep) > time.Second {
			l.sweep(now, rate)
		}
		if len(l.buckets) >= maxBuckets {
			key = overflowKey
			b = l.buckets[key]
		}
		if b == nil {
			b = &bucket{tokens: l.perMinute, last: now}
			l.buckets[key] = b
		}
	}
	b.tokens = min(l.perMinute, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) / rate * float64(time.Second))
	}
	b.tokens--
	return true, 0
}

// sweep drops clients whose bucket is full again: forgetting them changes nothing.
func (l *limiter) sweep(now time.Time, rate float64) {
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*rate >= l.perMinute {
			delete(l.buckets, k)
		}
	}
	l.lastSweep = now
}
