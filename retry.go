package retry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// Public API errors.
var (
	// ErrRetryAborted indicates that the retry process exhausted
	// the maximum number of attempts without success.
	ErrRetryAborted = errors.New("retry attempts exhausted")

	// ErrRetryBadOption signifies that an invalid option was
	// provided to the retry function, such as out-of-range values
	// or conflicting settings.
	ErrRetryBadOption = errors.New("bad option")
)

// permanentError wraps an error to indicate it should not be retried.
type permanentError struct {
	err error
}

func (e *permanentError) Error() string {
	// No nil check needed: Permanent(nil) returns nil, not &permanentError{}.
	return e.err.Error()
}

func (e *permanentError) Unwrap() error {
	return e.err
}

// Permanent wraps an error to indicate it should not be retried.
// When an operation returns a permanent error, the retry loop stops
// immediately and returns the underlying error.
//
// If err is nil, Permanent returns nil. Without this guard,
// Permanent(nil) would produce a non-nil *permanentError interface
// value, which would be mistaken for a real permanent failure in
// the retry loop.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// Config holds retry configuration. It must be constructed via
// NewConfig to ensure validation.
type Config struct {
	initialInterval time.Duration
	strategy        BackoffFunc
	maxInterval     time.Duration
	maxJitter       time.Duration
	maxAttempts     int64
	jitterFunc      JitterFunc
}

// NewConfig creates a Config using functional options. The interval
// must be positive. Nil strategy or jitter functions, negative caps,
// and negative maxAttempts are rejected. All invalid inputs return
// [ErrRetryBadOption].
func NewConfig(interval time.Duration, opts ...Option) (*Config, error) {
	if interval <= 0 {
		return nil, fmt.Errorf("initial interval must be greater than zero: %w", ErrRetryBadOption)
	}

	cfg := Config{
		initialInterval: interval,
		strategy:        fixedBackoff,
		jitterFunc:      noJitter,
	}

	for i := range opts {
		opts[i](&cfg)
	}

	if cfg.maxJitter < 0 {
		return nil, fmt.Errorf("%w: jitter cap must be non-negative", ErrRetryBadOption)
	}

	if cfg.maxInterval < 0 {
		return nil, fmt.Errorf("%w: interval cap must be non-negative", ErrRetryBadOption)
	}

	if cfg.maxInterval > 0 && cfg.maxInterval < interval {
		return nil, fmt.Errorf("%w: interval cap must be greater than or equal to interval", ErrRetryBadOption)
	}

	if cfg.maxAttempts < 0 {
		return nil, fmt.Errorf("%w: maxAttempts must be non-negative", ErrRetryBadOption)
	}

	if cfg.strategy == nil {
		return nil, fmt.Errorf("%w: nil strategy", ErrRetryBadOption)
	}

	if cfg.jitterFunc == nil {
		return nil, fmt.Errorf("%w: nil jitter function", ErrRetryBadOption)
	}

	return &cfg, nil
}

// noJitter is the default jitter function that adds no randomisation.
func noJitter(time.Duration) time.Duration { return 0 }

// fixedBackoff is the default strategy: return the initial interval
// unchanged. Defined at package level to avoid allocating a closure
// on every NewConfig call.
func fixedBackoff(initialInterval time.Duration, _ int64) time.Duration {
	return initialInterval
}

type (
	// JitterFunc takes a base interval and returns a duration to
	// be added as jitter.
	//
	// Law: must return a non-negative duration. Returning a
	// negative duration violates this contract and the result
	// will be clamped to zero.
	JitterFunc func(time.Duration) time.Duration

	// Operation defines the function signature for an operation
	// that can be retried. Operations should be context-aware:
	// check ctx.Err() before doing work and return early if
	// cancelled. This avoids unnecessary work when the context
	// is already done.
	//
	// The return value indicates the result:
	//   - nil: success, stop retrying
	//   - Permanent(err): permanent failure, stop retrying
	//   - ctx.Err(): context cancelled, stop retrying
	//   - any other error: transient failure, retry
	Operation func(ctx context.Context) error
)

// Option configures retry behaviour via NewConfig.
type Option func(*Config)

// WithMaxInterval caps both the scheduled base interval and the
// final sleep interval after jitter. Zero (the default) means no cap.
// Must be non-negative and, if positive, at least equal to the
// initial interval.
func WithMaxInterval(maxInterval time.Duration) Option {
	return func(cfg *Config) {
		cfg.maxInterval = maxInterval
	}
}

// WithJitterFunc sets a function that returns the amount of jitter to
// be added to the sleep interval. If not set, no jitter is applied.
// Must not be nil.
func WithJitterFunc(jitterFunc JitterFunc) Option {
	return func(cfg *Config) {
		cfg.jitterFunc = jitterFunc
	}
}

// WithMaxJitter caps the jitter added to the sleep interval. Zero
// (the default) means no cap on jitter. Must be non-negative.
func WithMaxJitter(maxJitter time.Duration) Option {
	return func(cfg *Config) {
		cfg.maxJitter = maxJitter
	}
}

// WithStrategy sets the backoff strategy used during retries. Must
// not be nil.
func WithStrategy(strategy BackoffFunc) Option {
	return func(cfg *Config) {
		cfg.strategy = strategy
	}
}

// WithMaxAttempts sets the maximum number of attempts.
//
// If maxAttempts is 0 (the default), retries continue indefinitely
// until the operation succeeds or the context is cancelled.
//
// If maxAttempts is 1, the operation is attempted once with no retries.
// If maxAttempts is 3, the operation is attempted up to 3 times (the
// initial attempt plus 2 retries).
//
// When the maximum is reached, DoWithConfig returns the last error
// from the operation wrapped with ErrRetryAborted.
func WithMaxAttempts(maxAttempts int64) Option {
	return func(cfg *Config) {
		cfg.maxAttempts = maxAttempts
	}
}

// BackoffFunc is a closed-form schedule that maps (initialInterval,
// retry) to a base interval. The first argument is always the
// initial interval from the config; the second is the retry number
// (1 = first retry after the initial failure). No feedback: the loop
// never passes a previous output back as input.
//
// Law: must return a positive duration. Returning zero or negative
// violates this contract and the result is reset to the initial
// interval.
type BackoffFunc func(initialInterval time.Duration, retry int64) time.Duration

// ExponentialBackoff returns a BackoffFunc that computes
// initialInterval * 2^(retry-1). Sequence: 1x, 2x, 4x, 8x, ...
func ExponentialBackoff() BackoffFunc {
	return func(initialInterval time.Duration, retry int64) time.Duration {
		if retry <= 1 {
			return initialInterval
		}

		exp := retry - 1
		if exp >= 63 {
			return time.Duration(math.MaxInt64)
		}

		shift := time.Duration(1) << uint(exp)
		if initialInterval > time.Duration(math.MaxInt64)/shift {
			return time.Duration(math.MaxInt64)
		}

		return initialInterval * shift
	}
}

// LinearBackoff returns a BackoffFunc that computes
// initialInterval * retry. Sequence: 1x, 2x, 3x, 4x, ...
func LinearBackoff() BackoffFunc {
	return func(initialInterval time.Duration, retry int64) time.Duration {
		if retry <= 0 {
			return initialInterval
		}

		if initialInterval > time.Duration(math.MaxInt64)/time.Duration(retry) {
			return time.Duration(math.MaxInt64)
		}

		return initialInterval * time.Duration(retry)
	}
}

// FixedBackoff returns a BackoffFunc that returns initialInterval
// unchanged, regardless of the retry number.
func FixedBackoff() BackoffFunc {
	return fixedBackoff
}

// baseForRetry computes the base interval for the given retry
// by calling the strategy and applying clamping and capping.
func (c *Config) baseForRetry(retry int64) time.Duration {
	base := c.strategy(c.initialInterval, retry)
	if base <= 0 {
		base = c.initialInterval
	}

	if c.maxInterval > 0 && base > c.maxInterval {
		base = c.maxInterval
	}

	return base
}

// sleepForRetry computes the full sleep duration for a given retry,
// combining the base interval from the strategy with jitter and
// capping.
func (c *Config) sleepForRetry(retry int64) time.Duration {
	return c.sleepInterval(c.baseForRetry(retry))
}

// DoWithConfig executes an operation with retry logic using a
// pre-built Config. The Config must be created via NewConfig which
// validates all parameters upfront. ctx, cfg, and operation must be
// non-nil.
//
// The returned error, when non-nil, wraps one of the following
// with %w so that callers can match using errors.Is:
//
//   - [ErrRetryAborted]: max attempts exhausted. The last error
//     from the operation is also wrapped.
//   - [context.Canceled] or [context.DeadlineExceeded]: the context
//     was done. When a custom cause is set via
//     [context.WithCancelCause] or [context.WithDeadlineCause],
//     both the canonical context error and the cause are wrapped.
//
// The exception is [Permanent]: the inner error is returned
// directly, without any wrapping from the retry loop. If the
// operation returns a [Permanent] error and the context is also
// done, the permanent error takes precedence — the operation is
// closest to the work and its explicit signal is respected.
func DoWithConfig(ctx context.Context, cfg *Config, operation Operation) error {
	opErr := operation(ctx)
	if opErr == nil {
		return nil
	}

	// The retry loop is a closure so that perr (the errors.As
	// target) only escapes to the heap when retries actually
	// happen, keeping the happy path zero-alloc. The loop
	// processes opErr before calling the operation, inverting
	// the natural call -> check -> sleep order.
	return func() error {
		var (
			attempt int64 = 1
			timer   *time.Timer
			perr    *permanentError
		)

		for {
			if errors.As(opErr, &perr) {
				return perr.err
			}

			if ctx.Err() != nil {
				return contextAbortError(ctx, attempt)
			}

			if cfg.maxAttempts > 0 && attempt >= cfg.maxAttempts {
				return fmt.Errorf("failed after %d attempts: %w: %w", attempt, opErr, ErrRetryAborted)
			}

			sleep := cfg.sleepForRetry(attempt)

			if timer == nil {
				timer = time.NewTimer(sleep)
			} else {
				timer.Reset(sleep)
			}

			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return contextAbortError(ctx, attempt)
			}

			attempt++

			opErr = operation(ctx)
			if opErr == nil {
				return nil
			}
		}
	}()
}

// contextAbortError returns an error wrapping the context error and,
// when present, a distinct custom cause. See the package
// documentation's "Context cancellation" section for background on
// why dual wrapping is needed.
func contextAbortError(ctx context.Context, attempt int64) error {
	cause := context.Cause(ctx)
	if cause != ctx.Err() { //nolint:errorlint // identity comparison; errors.Is would match a cause that wraps the context error
		return fmt.Errorf("aborted after attempt %d: %w: %w", attempt, ctx.Err(), cause)
	}
	return fmt.Errorf("aborted after attempt %d: %w", attempt, cause)
}

// sleepInterval computes the sleep duration from a base interval.
// It adds jitter and applies capping as configured.
func (c *Config) sleepInterval(base time.Duration) time.Duration {
	jitter := max(c.jitterFunc(base), 0)

	if c.maxJitter > 0 && jitter > c.maxJitter {
		jitter = c.maxJitter
	}

	sleep := base

	if jitter > time.Duration(math.MaxInt64)-sleep {
		if c.maxInterval > 0 {
			return c.maxInterval
		}
		return time.Duration(math.MaxInt64)
	}

	sleep += jitter

	if c.maxInterval > 0 && sleep > c.maxInterval {
		sleep = c.maxInterval
	}

	return sleep
}
