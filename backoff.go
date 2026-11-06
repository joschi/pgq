package pgq

import (
	"errors"
	"math"
	"time"
)

// ExponentialBackoff configures how long a NACKed message waits before a
// consumer may claim it again. After the n-th failed attempt the delay is
//
//	base  = Initial * Multiplier^(n-1), capped at Max if Max > 0
//	delay = base * (1 - Jitter*r), with r uniform in [0, 1)
//
// The delay is uniform in [(1-Jitter)*base, base], so it never exceeds Max
// and keeps varying after base reaches Max.
//
// The zero value disables backoff: NACKed messages are redelivered
// immediately.
type ExponentialBackoff struct {
	// Initial is the delay after the first failed attempt. Zero disables
	// backoff.
	Initial time.Duration
	// Multiplier grows the delay with every further failed attempt. It must
	// be 0 or at least 1. Zero is treated as 1, a constant delay.
	Multiplier float64
	// Max caps the base delay. Zero means no cap: the delay keeps growing
	// until it saturates at about 292 years.
	Max time.Duration
	// Jitter is the largest fraction, between 0 and 1, by which a delay is
	// shortened at random. 0.2 gives delays between 80% and 100% of base.
	Jitter float64
}

func (b ExponentialBackoff) validate() error {
	switch {
	case b.Initial < 0:
		return errors.New("initial delay must not be negative")
	case b.Max < 0:
		return errors.New("max delay must not be negative")
	case b.Multiplier != 0 && !(b.Multiplier >= 1): // also rejects NaN
		return errors.New("multiplier must be 0 or at least 1")
	case !(b.Jitter >= 0 && b.Jitter <= 1): // also rejects NaN
		return errors.New("jitter must be between 0 and 1")
	}
	return nil
}

// delay returns the backoff after the given failed attempt (1-based). r is a
// random number in [0, 1) used for jitter.
func (b ExponentialBackoff) delay(attempt int, r float64) time.Duration {
	if b.Initial <= 0 || attempt < 1 {
		return 0
	}
	d := float64(b.Initial) * math.Pow(max(b.Multiplier, 1), float64(attempt-1))
	if b.Max > 0 {
		d = min(d, float64(b.Max))
	}
	d *= 1 - b.Jitter*r
	if d >= math.MaxInt64 {
		return math.MaxInt64
	}
	return time.Duration(d)
}
