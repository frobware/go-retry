package retry

// This file contains the package's only intentional white-box test.
// Most retry behaviour is tested through the public API. This edge
// case is tested internally because exercising it through DoWithConfig
// would require either a timing-based test or production code changes
// made only for testing.

import (
	"math"
	"testing"
	"time"
)

// TestSleepForRetry_SaturatesToMaxInt64 verifies that when adding
// jitter to the computed base would overflow, sleepForRetry returns
// MaxInt64 rather than wrapping. This is an internal test because the
// branch through DoWithConfig would require effectively sleeping for
// MaxInt64 nanoseconds.
func TestSleepForRetry_SaturatesToMaxInt64(t *testing.T) {
	t.Parallel()
	cfg, err := NewConfig(time.Millisecond,
		WithJitterFunc(func(time.Duration) time.Duration {
			return time.Duration(math.MaxInt64)
		}),
		WithStrategy(func(time.Duration, int64) time.Duration {
			return time.Duration(math.MaxInt64)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	got := cfg.sleepForRetry(1)
	want := time.Duration(math.MaxInt64)
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestSleepForRetry_Capping verifies the clamping and capping
// arithmetic inside sleepInterval. These are internal tests because
// the capping happens after the jitter function returns, making it
// unobservable through the public DoWithConfig API without relying on
// wall-clock timing.
func TestSleepForRetry_Capping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		interval    time.Duration
		strategy    BackoffFunc
		jitterFunc  JitterFunc
		maxInterval time.Duration
		maxJitter   time.Duration
		retry       int64
		want        time.Duration
	}{
		{
			name:       "negative jitter clamped to zero",
			interval:   time.Millisecond,
			jitterFunc: func(base time.Duration) time.Duration { return -2 * base },
			retry:      1,
			want:       time.Millisecond, // base only, jitter clamped to 0
		},
		{
			name:       "maxJitter caps jitter contribution",
			interval:   10 * time.Millisecond,
			strategy:   FixedBackoff(),
			jitterFunc: func(time.Duration) time.Duration { return 5 * time.Millisecond },
			maxJitter:  2 * time.Millisecond,
			retry:      1,
			want:       12 * time.Millisecond, // 10ms base + 2ms capped jitter
		},
		{
			name:        "overflow jitter with maxInterval returns maxInterval",
			interval:    time.Millisecond,
			jitterFunc:  func(time.Duration) time.Duration { return time.Duration(math.MaxInt64) },
			maxInterval: time.Millisecond,
			retry:       1,
			want:        time.Millisecond,
		},
		{
			name:        "post-jitter sleep capped to maxInterval",
			interval:    time.Millisecond,
			jitterFunc:  func(time.Duration) time.Duration { return 10 * time.Millisecond },
			maxInterval: 5 * time.Millisecond,
			retry:       1,
			want:        5 * time.Millisecond,
		},
		{
			name:        "maxInterval caps exponential base on later retry",
			interval:    time.Microsecond,
			strategy:    ExponentialBackoff(),
			maxInterval: time.Microsecond,
			retry:       2, // exponential would give 2us
			want:        time.Microsecond,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var opts []Option
			if tc.strategy != nil {
				opts = append(opts, WithStrategy(tc.strategy))
			}
			if tc.jitterFunc != nil {
				opts = append(opts, WithJitterFunc(tc.jitterFunc))
			}
			if tc.maxInterval > 0 {
				opts = append(opts, WithMaxInterval(tc.maxInterval))
			}
			if tc.maxJitter > 0 {
				opts = append(opts, WithMaxJitter(tc.maxJitter))
			}

			cfg, err := NewConfig(tc.interval, opts...)
			if err != nil {
				t.Fatal(err)
			}

			got := cfg.sleepForRetry(tc.retry)
			if got != tc.want {
				t.Errorf("sleepForRetry(%d) = %v, want %v", tc.retry, got, tc.want)
			}
		})
	}
}
