package pgq

import (
	"math"
	"time"
)

// ExponentialBackoff configures how long a NACKed message waits before a
// consumer may claim it again. The delay after the n-th failed attempt is
//
//	min(Initial * Multiplier^(n-1) * (1 ± Jitter), Max)
//
// The zero value disables backoff: NACKed messages are redelivered
// immediately.
type ExponentialBackoff struct {
	// Initial is the delay after the first failed attempt. Zero disables
	// backoff.
	Initial time.Duration
	// Multiplier grows the delay with every further failed attempt. Values
	// below 1 are treated as 1 (constant delay).
	Multiplier float64
	// Max caps the delay, jitter included. Zero means no cap.
	Max time.Duration
	// Jitter spreads the delay by up to ±Jitter of its value, e.g. 0.2 is
	// ±20%. It is clamped to [0, 1].
	Jitter float64
}

// delay returns the backoff after the given failed attempt (1-based). r is a
// random number in [0, 1) used for jitter.
func (b ExponentialBackoff) delay(attempt int, r float64) time.Duration {
	if b.Initial <= 0 || attempt < 1 {
		return 0
	}
	d := float64(b.Initial) * math.Pow(max(b.Multiplier, 1), float64(attempt-1))
	d *= 1 + min(max(b.Jitter, 0), 1)*(2*r-1)
	if b.Max > 0 && d > float64(b.Max) {
		d = float64(b.Max)
	}
	if d >= math.MaxInt64 {
		return math.MaxInt64
	}
	return time.Duration(d)
}
