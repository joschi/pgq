package pgq

import (
	"math"
	"testing"
	"time"

	"github.com/joschi/pgq/internal/require"
)

func TestExponentialBackoff_delay(t *testing.T) {
	b := ExponentialBackoff{Initial: 5 * time.Second, Multiplier: 2, Max: time.Minute}
	tests := []struct {
		name    string
		b       ExponentialBackoff
		attempt int
		r       float64
		want    time.Duration
	}{
		{"disabled", ExponentialBackoff{}, 1, 0.5, 0},
		{"negative initial disables", ExponentialBackoff{Initial: -time.Second}, 1, 0.5, 0},
		{"attempt 0", b, 0, 0.5, 0},
		{"attempt 1", b, 1, 0.5, 5 * time.Second},
		{"attempt 2", b, 2, 0.5, 10 * time.Second},
		{"attempt 4", b, 4, 0.5, 40 * time.Second},
		{"capped", b, 5, 0.5, time.Minute},
		{"capped far out", b, 1000, 0.5, time.Minute},
		{"multiplier below 1 is constant", ExponentialBackoff{Initial: time.Second, Multiplier: 0.5}, 3, 0.5, time.Second},
		{"no cap does not overflow", ExponentialBackoff{Initial: time.Second, Multiplier: 10}, 1000, 0.5, math.MaxInt64},
		{"jitter low bound", ExponentialBackoff{Initial: 10 * time.Second, Jitter: 0.2}, 1, 0, 8 * time.Second},
		{"jitter midpoint", ExponentialBackoff{Initial: 10 * time.Second, Jitter: 0.2}, 1, 0.5, 10 * time.Second},
		{"jitter high bound", ExponentialBackoff{Initial: 10 * time.Second, Jitter: 0.2}, 1, 1, 12 * time.Second},
		{"jitter clamped to 1", ExponentialBackoff{Initial: 10 * time.Second, Jitter: 5}, 1, 1, 20 * time.Second},
		{"jitter never exceeds max", ExponentialBackoff{Initial: time.Minute, Max: time.Minute, Jitter: 0.5}, 1, 1, time.Minute},
		{"jitter below max at cap", ExponentialBackoff{Initial: time.Minute, Max: time.Minute, Jitter: 0.5}, 1, 0, 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.b.delay(tt.attempt, tt.r))
		})
	}
}
