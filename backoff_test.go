package pgq

import (
	"math"
	"testing"
	"time"

	"github.com/joschi/pgq/internal/require"
)

func TestExponentialBackoff_delay(t *testing.T) {
	b := ExponentialBackoff{Initial: 5 * time.Second, Multiplier: 2, Max: time.Minute}
	jittered := ExponentialBackoff{Initial: 10 * time.Second, Jitter: 0.2}
	tests := []struct {
		name    string
		b       ExponentialBackoff
		attempt int
		r       float64
		want    time.Duration
	}{
		{"disabled", ExponentialBackoff{}, 1, 0.5, 0},
		{"attempt 0", b, 0, 0.5, 0},
		{"attempt 1", b, 1, 0.5, 5 * time.Second},
		{"attempt 2", b, 2, 0.5, 10 * time.Second},
		{"attempt 4", b, 4, 0.5, 40 * time.Second},
		{"capped", b, 5, 0.5, time.Minute},
		{"capped far out", b, 1000, 0.5, time.Minute},
		{"multiplier 0 is constant", ExponentialBackoff{Initial: time.Second}, 3, 0.5, time.Second},
		{"no cap saturates", ExponentialBackoff{Initial: time.Second, Multiplier: 10}, 1000, 0.5, math.MaxInt64},
		{"jitter r=0 is base", jittered, 1, 0, 10 * time.Second},
		{"jitter r=0.5", jittered, 1, 0.5, 9 * time.Second},
		{"jitter r=0.75", jittered, 1, 0.75, 8500 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.b.delay(tt.attempt, tt.r))
		})
	}
}

func TestExponentialBackoff_jitter_at_cap(t *testing.T) {
	// Base reaches Max at attempt 8 (5s * 2^6 = 320s > 5m).
	b := ExponentialBackoff{Initial: 5 * time.Second, Multiplier: 2, Max: 5 * time.Minute, Jitter: 0.2}
	for _, attempt := range []int{8, 9, 20, 100, 1000} {
		seen := map[time.Duration]bool{}
		for _, r := range []float64{0, 0.25, 0.5, 0.75, math.Nextafter(1, 0)} {
			d := b.delay(attempt, r)
			if d > b.Max || d < 4*time.Minute {
				t.Fatalf("attempt %d, r=%v: delay %v outside [4m, 5m]", attempt, r, d)
			}
			seen[d] = true
		}
		require.Equal(t, 5, len(seen))
		require.Equal(t, 4*time.Minute+30*time.Second, b.delay(attempt, 0.5))
	}
}

func TestExponentialBackoff_validate(t *testing.T) {
	valid := []ExponentialBackoff{
		{},
		{Initial: time.Second},
		{Initial: time.Second, Multiplier: 1, Max: time.Minute, Jitter: 1},
		{Initial: time.Second, Multiplier: 2.5, Jitter: 0.2},
	}
	for _, b := range valid {
		require.NoError(t, b.validate())
	}
	invalid := []ExponentialBackoff{
		{Initial: -time.Second},
		{Initial: time.Second, Max: -time.Second},
		{Initial: time.Second, Multiplier: 0.5},
		{Initial: time.Second, Multiplier: -2},
		{Initial: time.Second, Multiplier: math.NaN()},
		{Initial: time.Second, Jitter: -0.1},
		{Initial: time.Second, Jitter: 1.5},
		{Initial: time.Second, Jitter: math.NaN()},
	}
	for _, b := range invalid {
		require.Error(t, b.validate())
	}
}

func TestNewConsumer_rejects_invalid_backoff(t *testing.T) {
	_, err := NewConsumer(nil, "queue", nil, WithRetryBackoff(ExponentialBackoff{Initial: time.Second, Jitter: 2}))
	require.Error(t, err)
}
