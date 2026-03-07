package retry_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/frobware/go-retry"
)

var (
	errTemporary = errors.New("temporary error")
	errPermanent = errors.New("permanent error")
)

// testStrategy calls a BackoffFunc directly with the given inputs and
// asserts the output matches the expected duration.
func testStrategy(t *testing.T, strategy retry.BackoffFunc, testCases []struct {
	name            string
	initialInterval time.Duration
	retry           int64
	expected        time.Duration
},
) {
	t.Helper()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := strategy(tc.initialInterval, tc.retry)
			if got != tc.expected {
				t.Errorf("strategy(%v, %d) = %v, want %v", tc.initialInterval, tc.retry, got, tc.expected)
			}
		})
	}
}

// TestDo_ContextCancellation verifies that when an operation cancels
// the context and returns a transient error, DoWithConfig detects the
// cancellation via ctx.Err() and stops without retrying.
func TestDo_ContextCancellation(t *testing.T) {
	t.Parallel()
	t.Run("operation cancels context and returns transient error", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		cfg, err := retry.NewConfig(time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}

		var attempts int
		err = retry.DoWithConfig(ctx, cfg, func(context.Context) error {
			attempts++
			cancel()
			return errTemporary
		})

		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}

		if attempts != 1 {
			t.Errorf("expected 1 attempt, got %d", attempts)
		}
	})
}

// TestDo_ContextCancellationCause verifies that context.Cause is
// propagated through the error returned by DoWithConfig, and that
// the canonical context error (context.Canceled or
// context.DeadlineExceeded) is always present in the chain — even
// when a custom cause is set.
func TestDo_ContextCancellationCause(t *testing.T) {
	t.Parallel()
	customErr := errors.New("server shutting down")

	tests := []struct {
		name          string
		mkContext     func() (context.Context, func())
		expectedCause error
		alsoExpected  error // if non-nil, errors.Is(err, this) must also be true
	}{
		{
			name: "WithCancelCause preserves custom cause and context.Canceled",
			mkContext: func() (context.Context, func()) {
				ctx, cancel := context.WithCancelCause(context.Background())
				return ctx, func() { cancel(customErr) }
			},
			expectedCause: customErr,
			alsoExpected:  context.Canceled,
		},
		{
			name: "WithCancel wraps context.Canceled",
			mkContext: func() (context.Context, func()) {
				ctx, cancel := context.WithCancel(context.Background())
				return ctx, func() { cancel() }
			},
			expectedCause: context.Canceled,
		},
		{
			name: "WithDeadlineCause preserves custom cause and context.DeadlineExceeded",
			mkContext: func() (context.Context, func()) {
				ctx, cancel := context.WithDeadlineCause(context.Background(), time.Now(), customErr)
				return ctx, func() { cancel() }
			},
			expectedCause: customErr,
			alsoExpected:  context.DeadlineExceeded,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := tc.mkContext()
			defer cancel()

			cfg, err := retry.NewConfig(time.Millisecond)
			if err != nil {
				t.Fatalf("NewConfig: %v", err)
			}

			var attempts int
			err = retry.DoWithConfig(ctx, cfg, func(context.Context) error {
				attempts++
				if attempts == 2 {
					cancel()
				}
				return errTemporary
			})

			if !errors.Is(err, tc.expectedCause) {
				t.Errorf("expected error chain to contain %v, got: %v", tc.expectedCause, err)
			}

			if tc.alsoExpected != nil && !errors.Is(err, tc.alsoExpected) {
				t.Errorf("expected error chain to also contain %v, got: %v", tc.alsoExpected, err)
			}
		})
	}
}

// TestDo_OperationFailsWithSpecificError verifies that when an
// operation returns Permanent(err) at a specific attempt, DoWithConfig
// stops retrying and the returned error wraps the original.
func TestDo_OperationFailsWithSpecificError(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name             string
		failAtAttempt    int
		expectedError    error
		expectedAttempts int
	}{
		{"Fail at 5th attempt", 5, errPermanent, 5},
		{"Fail at 1st attempt", 1, errPermanent, 1},
		{"Fail at 10th attempt", 10, errPermanent, 10},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var attempts int

			cfg, err := retry.NewConfig(time.Millisecond)
			if err != nil {
				t.Fatalf("failed to create config: %v", err)
			}

			err = retry.DoWithConfig(context.Background(), cfg,
				func(context.Context) error {
					attempts++
					if attempts == tc.failAtAttempt {
						return retry.Permanent(tc.expectedError)
					}
					return errTemporary
				},
			)

			if !errors.Is(err, tc.expectedError) {
				t.Fatalf("expected error %q, but got %q", tc.expectedError, err)
			}

			if attempts != tc.expectedAttempts {
				t.Errorf("expected %d attempts, got %d", tc.expectedAttempts, attempts)
			}
		})
	}
}

// TestDo_NegativeStrategyOutputOnLaterRetry verifies that a strategy
// returning a negative interval on a later retry does not break the
// loop. The negative output is clamped to the initial interval and
// retries continue normally.
func TestDo_NegativeStrategyOutputOnLaterRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	retryInterval := time.Millisecond
	maxAttempts := 5

	// Return negative on even retries to exercise clamping across
	// multiple retries, not just a single one.
	mockStrategy := retry.BackoffFunc(func(initialInterval time.Duration, retry int64) time.Duration {
		if retry%2 == 0 {
			return -time.Millisecond
		}
		return initialInterval
	})

	// Record the base interval the jitter function receives on
	// each retry to verify every negative strategy output is
	// clamped.
	var observedBases []time.Duration
	var attempts int
	operation := func(context.Context) error {
		attempts++
		if attempts >= maxAttempts {
			return nil
		}
		return errTemporary
	}

	cfg, err := retry.NewConfig(retryInterval,
		retry.WithStrategy(mockStrategy),
		retry.WithJitterFunc(func(base time.Duration) time.Duration {
			observedBases = append(observedBases, base)
			return 0
		}),
	)
	if err != nil {
		t.Fatalf("failed to create config: %v", err)
	}

	err = retry.DoWithConfig(ctx, cfg, operation)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	if attempts != maxAttempts {
		t.Errorf("expected %d attempts, got %d", maxAttempts, attempts)
	}

	// 4 sleeps (retries 1-4); even-numbered retries (2 and 4)
	// return negative from the strategy, which must be clamped to
	// initialInterval just like the odd-numbered ones.
	expectedSleeps := maxAttempts - 1
	if len(observedBases) != expectedSleeps {
		t.Fatalf("expected %d jitter calls, got %d", expectedSleeps, len(observedBases))
	}
	for i, base := range observedBases {
		if base != retryInterval {
			t.Errorf("retry %d: jitter received base %v, want %v", i+1, base, retryInterval)
		}
	}
}

// TestDo_JitterAndSleepCapping verifies the defensive clamping and
// capping in sleepInterval: negative jitter is clamped to zero,
// overflow jitter is clamped to maxInterval, and post-jitter sleep
// that merely exceeds maxInterval is capped.
func TestDo_JitterAndSleepCapping(t *testing.T) {
	t.Parallel()
	maxAttempts := 5

	tests := []struct {
		name       string
		jitterFunc retry.JitterFunc
		options    []retry.Option
	}{
		{
			name: "Negative jitter clamped to zero",
			jitterFunc: func(interval time.Duration) time.Duration {
				return -2 * interval
			},
		},
		{
			name: "Overflow with large positive jitter clamped to maxInterval",
			jitterFunc: func(time.Duration) time.Duration {
				return time.Duration(math.MaxInt64)
			},
			options: []retry.Option{
				retry.WithMaxInterval(time.Millisecond),
			},
		},
		{
			name: "Post-jitter sleep capped to maxInterval without overflow",
			jitterFunc: func(time.Duration) time.Duration {
				return 10 * time.Millisecond
			},
			options: []retry.Option{
				retry.WithMaxInterval(5 * time.Millisecond),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var attempts int
			operation := func(context.Context) error {
				attempts++
				if attempts >= maxAttempts {
					return nil
				}
				return errTemporary
			}

			opts := append([]retry.Option{retry.WithJitterFunc(tc.jitterFunc)}, tc.options...)
			cfg, err := retry.NewConfig(time.Millisecond, opts...)
			if err != nil {
				t.Fatalf("failed to create config: %v", err)
			}

			err = retry.DoWithConfig(context.Background(), cfg, operation)
			if err != nil {
				t.Fatalf("expected success, got: %v", err)
			}

			if attempts != maxAttempts {
				t.Errorf("expected %d attempts, got %d", maxAttempts, attempts)
			}
		})
	}
}

// TestValidOptions verifies that NewConfig accepts valid option
// combinations and that DoWithConfig succeeds with each.
func TestValidOptions(t *testing.T) {
	t.Parallel()
	operation := func(context.Context) error {
		return nil
	}

	successTests := []struct {
		name    string
		options []retry.Option
	}{
		{
			name:    "Default strategy",
			options: []retry.Option{},
		},
		{
			name: "FixedBackoff strategy",
			options: []retry.Option{
				retry.WithStrategy(retry.FixedBackoff()),
			},
		},
		{
			name: "ExponentialBackoff strategy",
			options: []retry.Option{
				retry.WithStrategy(retry.ExponentialBackoff()),
			},
		},
		{
			name: "LinearBackoff strategy",
			options: []retry.Option{
				retry.WithStrategy(retry.LinearBackoff()),
			},
		},
		{
			name: "intervalCap equal to interval",
			options: []retry.Option{
				retry.WithMaxInterval(1 * time.Second),
			},
		},
		{
			name: "intervalCap greater than interval",
			options: []retry.Option{
				retry.WithMaxInterval(2 * time.Second),
			},
		},
		{
			name: "jitterCap > 0",
			options: []retry.Option{
				retry.WithMaxJitter(500 * time.Millisecond),
			},
		},
	}

	for _, tc := range successTests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := retry.NewConfig(1*time.Second, tc.options...)
			if err != nil {
				t.Fatalf("failed to create config: %v", err)
			}
			if err := retry.DoWithConfig(context.Background(), cfg, operation); err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
		})
	}
}

// TestInvalidOptions verifies that NewConfig rejects invalid
// configurations and returns ErrRetryBadOption for each.
func TestInvalidOptions(t *testing.T) {
	t.Parallel()
	failureTests := []struct {
		name     string
		interval time.Duration
		options  []retry.Option
	}{
		{
			name:     "invalid interval",
			interval: 0,
			options:  []retry.Option{},
		},
		{
			name:     "intervalCap less than interval",
			interval: 1 * time.Second,
			options: []retry.Option{
				retry.WithStrategy(retry.FixedBackoff()),
				retry.WithMaxInterval(500 * time.Millisecond),
			},
		},
		{
			name:     "negative intervalCap",
			interval: 1 * time.Second,
			options: []retry.Option{
				retry.WithMaxInterval(-1 * time.Second),
			},
		},
		{
			name:     "jitterCap less than zero",
			interval: 1 * time.Second,
			options: []retry.Option{
				retry.WithStrategy(retry.FixedBackoff()),
				retry.WithMaxJitter(-1 * time.Second),
			},
		},
		{
			name:     "explicitly set nil strategy",
			interval: 1 * time.Second,
			options: []retry.Option{
				retry.WithStrategy(nil),
			},
		},
		{
			name:     "explicitly set nil jitter function",
			interval: 1 * time.Second,
			options: []retry.Option{
				retry.WithJitterFunc(nil),
			},
		},
		{
			name:     "negative maxAttempts",
			interval: 1 * time.Second,
			options: []retry.Option{
				retry.WithMaxAttempts(-1),
			},
		},
	}

	for _, tc := range failureTests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := retry.NewConfig(tc.interval, tc.options...)
			if err == nil {
				t.Fatalf("expected error, got none")
			}

			if !errors.Is(err, retry.ErrRetryBadOption) {
				t.Errorf("expected ErrRetryBadOption, got: %v", err)
			}
		})
	}
}

// TestWithMaxAttempts verifies attempt limiting. Zero means unlimited,
// a positive value caps the total attempts, and exceeding the limit
// returns ErrRetryAborted wrapping the last operation error.
func TestWithMaxAttempts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		maxAttempts      int64
		expectedAttempts int
		expectError      bool
	}{
		{
			name:             "Zero means unlimited (success on 5th)",
			maxAttempts:      0,
			expectedAttempts: 5,
			expectError:      false,
		},
		{
			name:             "Max 1 means single attempt",
			maxAttempts:      1,
			expectedAttempts: 1,
			expectError:      true,
		},
		{
			name:             "Max 3 allows 3 attempts",
			maxAttempts:      3,
			expectedAttempts: 3,
			expectError:      true,
		},
		{
			name:             "Max 5 succeeds on 5th attempt",
			maxAttempts:      5,
			expectedAttempts: 5,
			expectError:      false,
		},
		{
			name:             "Max 10 succeeds on 5th attempt",
			maxAttempts:      10,
			expectedAttempts: 5,
			expectError:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var attempts int
			succeedOnAttempt := 5

			cfg, err := retry.NewConfig(time.Microsecond, retry.WithMaxAttempts(tc.maxAttempts))
			if err != nil {
				t.Fatalf("failed to create config: %v", err)
			}

			err = retry.DoWithConfig(context.Background(), cfg, func(context.Context) error {
				attempts++
				if attempts >= succeedOnAttempt {
					return nil
				}
				return errTemporary
			})

			if tc.expectError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, retry.ErrRetryAborted) {
					t.Errorf("expected ErrRetryAborted, got: %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if attempts != tc.expectedAttempts {
				t.Errorf("expected %d attempts, got %d", tc.expectedAttempts, attempts)
			}
		})
	}
}

// TestBackoffSchedules verifies the closed-form base interval
// produced by each strategy function in isolation, with and without
// maxInterval capping applied manually. The package's own scheduling
// path through baseForRetry is exercised separately by the
// DoWithConfig tests.
func TestBackoffSchedules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		strategy        retry.BackoffFunc
		initialInterval time.Duration
		maxInterval     time.Duration
		want            []time.Duration
	}{
		{
			name:            "FixedBackoff",
			strategy:        retry.FixedBackoff(),
			initialInterval: 100 * time.Microsecond,
			maxInterval:     100 * time.Microsecond,
			want: []time.Duration{
				100 * time.Microsecond,
				100 * time.Microsecond,
				100 * time.Microsecond,
				100 * time.Microsecond,
				100 * time.Microsecond,
			},
		},
		{
			name:            "ExponentialBackoff capped",
			strategy:        retry.ExponentialBackoff(),
			initialInterval: 100 * time.Microsecond,
			maxInterval:     200 * time.Microsecond,
			// retry 1: 100µs * 2^0 = 100µs
			// retry 2: 100µs * 2^1 = 200µs (= cap)
			// retry 3: 100µs * 2^2 = 400µs, capped to 200µs
			want: []time.Duration{
				100 * time.Microsecond,
				200 * time.Microsecond,
				200 * time.Microsecond,
			},
		},
		{
			name:            "ExponentialBackoff uncapped",
			strategy:        retry.ExponentialBackoff(),
			initialInterval: 100 * time.Microsecond,
			want: []time.Duration{
				100 * time.Microsecond,
				200 * time.Microsecond,
				400 * time.Microsecond,
				800 * time.Microsecond,
			},
		},
		{
			name:            "LinearBackoff capped",
			strategy:        retry.LinearBackoff(),
			initialInterval: 100 * time.Microsecond,
			maxInterval:     250 * time.Microsecond,
			// retry 1: 100µs * 1 = 100µs
			// retry 2: 100µs * 2 = 200µs
			// retry 3: 100µs * 3 = 300µs, capped to 250µs
			// retry 4: 100µs * 4 = 400µs, capped to 250µs
			want: []time.Duration{
				100 * time.Microsecond,
				200 * time.Microsecond,
				250 * time.Microsecond,
				250 * time.Microsecond,
			},
		},
		{
			name:            "LinearBackoff uncapped",
			strategy:        retry.LinearBackoff(),
			initialInterval: 100 * time.Microsecond,
			want: []time.Duration{
				100 * time.Microsecond,
				200 * time.Microsecond,
				300 * time.Microsecond,
				400 * time.Microsecond,
				500 * time.Microsecond,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := retry.NewConfig(tc.initialInterval,
				retry.WithStrategy(tc.strategy),
				retry.WithMaxInterval(tc.maxInterval),
			)
			if err != nil {
				t.Fatalf("NewConfig: %v", err)
			}

			for i, want := range tc.want {
				n := int64(i + 1)
				got := tc.strategy(tc.initialInterval, n)
				if tc.maxInterval > 0 && got > tc.maxInterval {
					got = tc.maxInterval
				}
				if got != want {
					t.Errorf("retry %d: got %v, want %v", n, got, want)
				}
			}
		})
	}
}

// TestDo_MaxJitter verifies that WithMaxJitter caps the jitter
// contribution during attempts, even when the jitter function
// returns a larger value.
func TestDo_MaxJitter(t *testing.T) {
	t.Parallel()
	baseInterval := 10 * time.Millisecond
	requestedJitter := 5 * time.Millisecond
	maxJitter := 2 * time.Millisecond
	maxAttempts := 5

	var attempts int

	cfg, err := retry.NewConfig(baseInterval,
		retry.WithJitterFunc(func(_ time.Duration) time.Duration {
			return requestedJitter // Request more jitter than allowed
		}),
		retry.WithMaxJitter(maxJitter),
		retry.WithStrategy(retry.FixedBackoff()),
	)
	if err != nil {
		t.Fatalf("failed to create config: %v", err)
	}

	err = retry.DoWithConfig(context.Background(), cfg, func(context.Context) error {
		attempts++
		if attempts >= maxAttempts {
			return nil
		}
		return errTemporary
	})
	if err != nil {
		t.Fatalf("expected operation to succeed, got error: %v", err)
	}

	if attempts != maxAttempts {
		t.Errorf("expected %d attempts, got %d", maxAttempts, attempts)
	}
}

// TestDo_ContextCancelledDuringWait verifies that cancelling the
// context while the retry loop is sleeping causes DoWithConfig to
// return promptly with context.Canceled.
func TestDo_ContextCancelledDuringWait(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	cfg, err := retry.NewConfig(time.Hour) // long sleep so context cancels first
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	var attempts int
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	err = retry.DoWithConfig(ctx, cfg, func(context.Context) error {
		attempts++
		return errTemporary
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}

	if attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", attempts)
	}
}

// TestDo_ContextCancelCauseDuringWait verifies that cancelling the
// context with a custom cause while the retry loop is sleeping
// preserves both the canonical context error and the cause.
func TestDo_ContextCancelCauseDuringWait(t *testing.T) {
	t.Parallel()
	errShutdown := errors.New("shutdown")
	ctx, cancel := context.WithCancelCause(context.Background())

	cfg, err := retry.NewConfig(time.Hour)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	var attempts int
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel(errShutdown)
	}()

	err = retry.DoWithConfig(ctx, cfg, func(context.Context) error {
		attempts++
		return errTemporary
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}

	if !errors.Is(err, errShutdown) {
		t.Fatalf("expected shutdown cause, got: %v", err)
	}

	if attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", attempts)
	}
}

// TestDo_MaxIntervalCapsBaseInLoop exercises the maxInterval cap on
// the base inside DoWithConfig's retry loop.
func TestDo_MaxIntervalCapsBaseInLoop(t *testing.T) {
	t.Parallel()
	maxInterval := time.Microsecond
	maxAttempts := 6

	var observedBases []time.Duration
	cfg, err := retry.NewConfig(time.Microsecond,
		retry.WithStrategy(retry.ExponentialBackoff()),
		retry.WithMaxInterval(maxInterval),
		retry.WithJitterFunc(func(base time.Duration) time.Duration {
			observedBases = append(observedBases, base)
			return 0
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	var attempts int
	err = retry.DoWithConfig(context.Background(), cfg, func(context.Context) error {
		attempts++
		if attempts >= maxAttempts {
			return nil
		}
		return errTemporary
	})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	if attempts != maxAttempts {
		t.Errorf("expected %d attempts, got %d", maxAttempts, attempts)
	}

	// 5 sleeps; exponential backoff would produce 1us, 2us, 4us,
	// 8us, 16us but maxInterval caps every base to 1us. Retry 1
	// happens to equal maxInterval; retries 2-5 all exceed it and
	// must be capped.
	expectedSleeps := maxAttempts - 1
	if len(observedBases) != expectedSleeps {
		t.Fatalf("expected %d jitter calls, got %d", expectedSleeps, len(observedBases))
	}
	for i, base := range observedBases {
		if base != maxInterval {
			t.Errorf("retry %d: jitter received base %v, want %v", i+1, base, maxInterval)
		}
	}
}

// TestDo_FirstRetryBaseIsInitialInterval verifies that the base
// interval computed for the first retry equals the initial interval
// for each built-in strategy. The jitter function is used as a probe
// to observe the base; no actual sleep timing is involved. This
// guards against off-by-one errors in the loop's retry numbering.
func TestDo_FirstRetryBaseIsInitialInterval(t *testing.T) {
	t.Parallel()
	strategies := []struct {
		name     string
		strategy retry.BackoffFunc
	}{
		{"FixedBackoff", retry.FixedBackoff()},
		{"LinearBackoff", retry.LinearBackoff()},
		{"ExponentialBackoff", retry.ExponentialBackoff()},
	}

	for _, tc := range strategies {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			initialInterval := 10 * time.Millisecond

			// Use the jitter function as a probe: it receives
			// the base interval that baseForRetry computed.
			var firstBase time.Duration
			cfg, err := retry.NewConfig(initialInterval,
				retry.WithStrategy(tc.strategy),
				retry.WithMaxAttempts(2),
				retry.WithJitterFunc(func(base time.Duration) time.Duration {
					firstBase = base
					return 0
				}),
			)
			if err != nil {
				t.Fatal(err)
			}

			err = retry.DoWithConfig(context.Background(), cfg, func(context.Context) error {
				return errTemporary
			})
			if !errors.Is(err, retry.ErrRetryAborted) {
				t.Fatalf("expected ErrRetryAborted, got %v", err)
			}

			if firstBase != initialInterval {
				t.Errorf("first retry base = %v, want %v", firstBase, initialInterval)
			}
		})
	}
}

// TestDo_NonPositiveStrategyOnFirstRetry verifies that when a
// strategy returns zero or negative on the first retry, the loop
// clamps the base to the initial interval rather than sleeping for
// zero or a negative duration.
func TestDo_NonPositiveStrategyOnFirstRetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		output time.Duration
	}{
		{"zero", 0},
		{"negative", -time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			initialInterval := time.Millisecond
			strategy := retry.BackoffFunc(func(time.Duration, int64) time.Duration {
				return tc.output
			})

			var firstBase time.Duration
			cfg, err := retry.NewConfig(initialInterval,
				retry.WithStrategy(strategy),
				retry.WithMaxAttempts(2),
				retry.WithJitterFunc(func(base time.Duration) time.Duration {
					firstBase = base
					return 0
				}),
			)
			if err != nil {
				t.Fatal(err)
			}

			err = retry.DoWithConfig(context.Background(), cfg, func(context.Context) error {
				return errTemporary
			})
			if !errors.Is(err, retry.ErrRetryAborted) {
				t.Fatalf("expected ErrRetryAborted, got %v", err)
			}

			if firstBase != initialInterval {
				t.Errorf("first retry base = %v, want %v (clamped to initial)", firstBase, initialInterval)
			}
		})
	}
}

// TestDoWithConfig_ExhaustionPreservesLastError verifies that when
// attempt exhaustion occurs, the returned error wraps both
// ErrRetryAborted and the last operation error.
func TestDoWithConfig_ExhaustionPreservesLastError(t *testing.T) {
	t.Parallel()
	cfg, err := retry.NewConfig(time.Nanosecond, retry.WithMaxAttempts(3))
	if err != nil {
		t.Fatal(err)
	}

	lastErr := errors.New("final failure")
	var attempts int

	err = retry.DoWithConfig(context.Background(), cfg, func(context.Context) error {
		attempts++
		return lastErr
	})

	if !errors.Is(err, retry.ErrRetryAborted) {
		t.Fatalf("expected ErrRetryAborted, got: %v", err)
	}
	if !errors.Is(err, lastErr) {
		t.Fatalf("expected last operation error to be preserved, got: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

// TestPermanent_NilError verifies that wrapping a nil error with
// Permanent returns nil rather than a non-nil error interface.
func TestPermanent_NilError(t *testing.T) {
	t.Parallel()
	if got := retry.Permanent(nil); got != nil {
		t.Errorf("Permanent(nil) = %v, want nil", got)
	}
}

// TestPermanent_ErrorMethod verifies that the Error method on a
// Permanent-wrapped error delegates to the underlying error's message.
func TestPermanent_ErrorMethod(t *testing.T) {
	t.Parallel()
	err := retry.Permanent(errors.New("boom"))
	if got := err.Error(); got != "boom" {
		t.Errorf("Error() = %q, want %q", got, "boom")
	}
}

// TestPermanent_Unwrap verifies that a Permanent-wrapped error
// exposes the underlying error via the standard unwrap chain.
func TestPermanent_Unwrap(t *testing.T) {
	t.Parallel()
	inner := errors.New("inner")
	err := retry.Permanent(inner)
	if !errors.Is(err, inner) {
		t.Errorf("errors.Is(Permanent(inner), inner) = false, want true")
	}
}

// TestDo_RespectPermanentError verifies that the retry process respects
// permanent errors returned by the operation.
func TestDo_RespectPermanentError(t *testing.T) {
	t.Parallel()
	baseInterval := 10 * time.Millisecond
	var attempts int

	cfg, err := retry.NewConfig(baseInterval, retry.WithStrategy(retry.FixedBackoff()))
	if err != nil {
		t.Fatalf("failed to create config: %v", err)
	}

	err = retry.DoWithConfig(context.Background(), cfg, func(context.Context) error {
		attempts++
		if attempts == 3 {
			return retry.Permanent(errPermanent)
		}
		return errTemporary
	})

	if err == nil || !errors.Is(err, errPermanent) {
		t.Fatalf("Expected %q, got: %v", errPermanent, err)
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got: %d", attempts)
	}
}

// TestDo_WrappedPermanentError verifies that a Permanent error
// wrapped with fmt.Errorf is still detected by DoWithConfig, stopping
// retries and preserving the original error in the chain.
func TestDo_WrappedPermanentError(t *testing.T) {
	t.Parallel()
	var attempts int

	cfg, err := retry.NewConfig(time.Millisecond)
	if err != nil {
		t.Fatalf("failed to create config: %v", err)
	}

	err = retry.DoWithConfig(context.Background(), cfg, func(context.Context) error {
		attempts++
		if attempts == 2 {
			return fmt.Errorf("while calling backend: %w", retry.Permanent(errPermanent))
		}
		return errTemporary
	})

	if !errors.Is(err, errPermanent) {
		t.Fatalf("expected %q through wrapping, got: %v", errPermanent, err)
	}

	// The returned error must be exactly the inner error from
	// Permanent, not a *permanentError or an intermediate wrapper.
	if err != errPermanent { //nolint:errorlint // intentional identity check: Permanent must return the exact inner error
		t.Errorf("expected exact error %q, got %q (type %T)", errPermanent, err, err)
	}

	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

// TestDo_ContextAlreadyCancelled verifies that when the context is
// cancelled before DoWithConfig is called, the operation runs once
// and context.Canceled is returned.
func TestDo_ContextAlreadyCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg, err := retry.NewConfig(time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	var attempts int
	err = retry.DoWithConfig(ctx, cfg, func(ctx context.Context) error {
		attempts++
		return ctx.Err()
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}

	if attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", attempts)
	}
}

// TestFixedBackoff verifies that FixedBackoff always returns the
// initial interval unchanged, regardless of retry number or duration
// magnitude.
func TestFixedBackoff(t *testing.T) {
	t.Parallel()
	testStrategy(t, retry.FixedBackoff(), []struct {
		name            string
		initialInterval time.Duration
		retry           int64
		expected        time.Duration
	}{
		{"Normal case", time.Second, 1, time.Second},
		{"High retry", time.Second, 1000000, time.Second},
		{"Max retry", time.Second, math.MaxInt64, time.Second},
		{"Max duration, retry 0", time.Duration(math.MaxInt64), 0, time.Duration(math.MaxInt64)},
		{"Max duration, retry 1", time.Duration(math.MaxInt64), 1, time.Duration(math.MaxInt64)},
		{"Max duration, retry 2", time.Duration(math.MaxInt64), 2, time.Duration(math.MaxInt64)},
		{"Max duration and max retry", time.Duration(math.MaxInt64), math.MaxInt64, time.Duration(math.MaxInt64)},
	})
}

// TestExponentialBackoff verifies that ExponentialBackoff computes
// initialInterval * 2^(retry-1) and saturates at MaxInt64 on
// overflow.
func TestExponentialBackoff(t *testing.T) {
	t.Parallel()
	testStrategy(t, retry.ExponentialBackoff(), []struct {
		name            string
		initialInterval time.Duration
		retry           int64
		expected        time.Duration
	}{
		{"retry 1 returns initial", time.Second, 1, time.Second},
		{"retry 2 doubles", time.Second, 2, 2 * time.Second},
		{"retry 3 quadruples", time.Second, 3, 4 * time.Second},
		{"retry 4 octuples", time.Second, 4, 8 * time.Second},
		{"different initial", 500 * time.Millisecond, 3, 2 * time.Second},
		{"retry 0 returns initial", time.Second, 0, time.Second},
		{"large exponent saturates", time.Second, 64, time.Duration(math.MaxInt64)},
		{"overflow saturates", time.Duration(math.MaxInt64), 2, time.Duration(math.MaxInt64)},
		{"max duration retry 1", time.Duration(math.MaxInt64), 1, time.Duration(math.MaxInt64)},
	})
}

// TestLinearBackoff verifies that LinearBackoff scales the initial
// interval by the retry number, returns it unchanged for
// non-positive values, and saturates at MaxInt64 on overflow.
func TestLinearBackoff(t *testing.T) {
	t.Parallel()
	testStrategy(t, retry.LinearBackoff(), []struct {
		name            string
		initialInterval time.Duration
		retry           int64
		expected        time.Duration
	}{
		{"retry 0 (edge case)", time.Second, 0, time.Second},
		{"retry -1 (edge case)", time.Second, -1, time.Second},
		{"retry 1", time.Second, 1, 1 * time.Second},
		{"retry 2", time.Second, 2, 2 * time.Second},
		{"retry 3", time.Second, 3, 3 * time.Second},
		{"retry 4", time.Second, 4, 4 * time.Second},
		{"large retry", time.Millisecond, 1000, time.Second},
		{"overflow check", time.Duration(math.MaxInt64) - time.Second, 2, time.Duration(math.MaxInt64)},
		{"overflow with large retry", time.Second, math.MaxInt64, time.Duration(math.MaxInt64)},
		{"Max duration", time.Duration(math.MaxInt64), 1, time.Duration(math.MaxInt64)},
	})
}
