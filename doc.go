// Package retry executes operations with configurable retry logic.
// It supports fixed, linear, and exponential backoff strategies,
// optional jitter, interval capping, attempt limits, custom backoff
// and jitter functions, and non-retryable errors via [Permanent].
//
//	cfg, err := retry.NewConfig(1*time.Second,
//	    retry.WithStrategy(retry.ExponentialBackoff()),
//	    retry.WithMaxInterval(1*time.Minute),
//	)
//	if err != nil {
//	    log.Fatalf("Invalid config: %v", err)
//	}
//	err = retry.DoWithConfig(ctx, cfg, func(ctx context.Context) error {
//	    return performTask(ctx)
//	})
//
// # Context cancellation
//
// Go 1.20 introduced [context.WithCancelCause] and [context.Cause],
// allowing a custom error to explain why a context was cancelled. A
// subtle consequence is that [context.Cause] returns only the custom
// cause, not [context.Canceled] or [context.DeadlineExceeded]. Code
// that checks errors.Is(err, context.Canceled) will therefore fail to
// match when a custom cause is set.
//
// There is an open proposal to address this in the standard library
// (https://github.com/golang/go/issues/63759), but until it lands,
// libraries must handle it themselves.
//
// DoWithConfig wraps both the canonical context error and the custom
// cause when they differ, using dual %w formatting:
//
//	fmt.Errorf("aborted after attempt %d: %w: %w", attempt, ctx.Err(), cause)
//
// This ensures errors.Is matches both the canonical context error and
// the caller's custom cause. Cancellation with a custom cause:
//
//	ctx, cancel := context.WithCancelCause(parent)
//	go func() { cancel(errShutdown) }()
//
//	err := retry.DoWithConfig(ctx, cfg, operation)
//	errors.Is(err, context.Canceled) // true
//	errors.Is(err, errShutdown)      // true
//
// Deadline exceeded with a custom cause:
//
//	ctx, cancel := context.WithDeadlineCause(parent, deadline, errSlowUpstream)
//	defer cancel()
//
//	err := retry.DoWithConfig(ctx, cfg, operation)
//	errors.Is(err, context.DeadlineExceeded) // true
//	errors.Is(err, errSlowUpstream)          // true
//
// When no custom cause is set (or the cause equals ctx.Err()), only
// the single context error is wrapped.
//
// For further background see
// https://boldlygo.tech/archive/2025-04-29-context-causes/ and
// https://rednafi.com/go/context-cancellation-cause/.
package retry
