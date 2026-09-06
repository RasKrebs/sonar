package relay

import (
	"sync"
	"time"
)

// Rate limit defaults: one batch per second per install, bursting to ten. A
// client that batches (which the desktop client does) never comes near it; a
// loop that does not is throttled without the relay having to decide whether
// it is malice or a bug.
const (
	DefaultRate  = 1.0
	DefaultBurst = 10
	// idleBucketTTL is how long a silent install keeps its bucket. Sweeping
	// matters: install_id is attacker-chosen, so an unbounded map of buckets
	// would be a memory leak with a trigger.
	idleBucketTTL = 15 * time.Minute
)

// limiter is a token bucket per key, swept of idle keys as it goes.
type limiter struct {
	rate  float64 // tokens per second
	burst float64
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	lastGC  time.Time
	maxKeys int
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(rate float64, burst int, now func() time.Time) *limiter {
	if now == nil {
		now = time.Now
	}
	return &limiter{
		rate:    rate,
		burst:   float64(burst),
		now:     now,
		buckets: map[string]*bucket{},
		lastGC:  now(),
		maxKeys: 100_000,
	}
}

// allow takes a token for key, reporting whether there was one and, when there
// was not, how long until there is.
func (l *limiter) allow(key string) (bool, time.Duration) {
	if l == nil || l.rate <= 0 {
		return true, 0
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.gcLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.maxKeys {
			// Full and nothing to evict: fail open rather than lock every
			// honest install out because one host is spraying ids.
			return true, 0
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}

	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens = min(l.burst, b.tokens+elapsed*l.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}

func (l *limiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < idleBucketTTL {
		return
	}
	l.lastGC = now
	for k, b := range l.buckets {
		if now.Sub(b.last) > idleBucketTTL {
			delete(l.buckets, k)
		}
	}
}
