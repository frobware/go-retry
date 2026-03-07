package retry_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/frobware/go-retry"
)

// Retry an operation that succeeds after a few transient failures.
func ExampleDoWithConfig() {
	cfg, err := retry.NewConfig(10*time.Millisecond, retry.WithMaxAttempts(5))
	if err != nil {
		fmt.Printf("failed to create config: %v\n", err)
		return
	}

	var attempts int

	if err := retry.DoWithConfig(context.Background(), cfg, func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary failure")
		}

		fmt.Printf("succeeded on attempt %d\n", attempts)
		return nil
	}); err != nil {
		fmt.Printf("operation failed: %v\n", err)
		return
	}

	fmt.Println("done")
	// Output:
	// succeeded on attempt 3
	// done
}

// Exhausting all attempts returns ErrRetryAborted wrapping the last
// operation error.
func ExampleDoWithConfig_maxAttempts() {
	cfg, err := retry.NewConfig(time.Millisecond, retry.WithMaxAttempts(3))
	if err != nil {
		fmt.Printf("config: %v\n", err)
		return
	}

	errFlaky := errors.New("flaky")

	err = retry.DoWithConfig(context.Background(), cfg, func(context.Context) error {
		return errFlaky
	})

	fmt.Println(errors.Is(err, retry.ErrRetryAborted))
	fmt.Println(errors.Is(err, errFlaky))
	// Output:
	// true
	// true
}

// Returning a Permanent error stops the retry loop and returns the
// inner error directly, without ErrRetryAborted wrapping.
func ExampleDoWithConfig_permanent() {
	cfg, err := retry.NewConfig(time.Millisecond, retry.WithMaxAttempts(5))
	if err != nil {
		fmt.Printf("config: %v\n", err)
		return
	}

	errFatal := errors.New("fatal")

	var attempts int
	err = retry.DoWithConfig(context.Background(), cfg, func(context.Context) error {
		attempts++
		if attempts == 2 {
			return retry.Permanent(errFatal)
		}
		return errors.New("transient")
	})

	fmt.Println(errors.Is(err, errFatal))
	fmt.Println(errors.Is(err, retry.ErrRetryAborted))
	fmt.Println(attempts)
	// Output:
	// true
	// false
	// 2
}

// When the operation returns a Permanent error and the context is
// also cancelled, the permanent error takes precedence.
func ExampleDoWithConfig_permanentOverridesCancel() {
	cfg, err := retry.NewConfig(time.Millisecond)
	if err != nil {
		fmt.Printf("config: %v\n", err)
		return
	}

	errFatal := errors.New("fatal")
	ctx, cancel := context.WithCancelCause(context.Background())

	// The operation cancels the context and returns a permanent
	// error in the same call. Permanent takes precedence because
	// the operation is in control: it decided this failure is
	// non-retryable regardless of context state.
	err = retry.DoWithConfig(ctx, cfg, func(context.Context) error {
		cancel(errors.New("shutting down"))
		return retry.Permanent(errFatal)
	})

	fmt.Println(errors.Is(err, errFatal))
	fmt.Println(errors.Is(err, context.Canceled))
	// Output:
	// true
	// false
}

// Cancelling the context stops the retry loop and returns
// context.Canceled.
func ExampleDoWithConfig_canceled() {
	cfg, err := retry.NewConfig(time.Millisecond)
	if err != nil {
		fmt.Printf("config: %v\n", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())

	var attempts int
	err = retry.DoWithConfig(ctx, cfg, func(context.Context) error {
		attempts++
		if attempts == 2 {
			cancel()
		}
		return errors.New("transient")
	})

	fmt.Println(errors.Is(err, context.Canceled))
	// Output:
	// true
}

// Exceeding the context deadline returns context.DeadlineExceeded.
func ExampleDoWithConfig_deadlineExceeded() {
	cfg, err := retry.NewConfig(time.Millisecond)
	if err != nil {
		fmt.Printf("config: %v\n", err)
		return
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now())
	defer cancel()

	err = retry.DoWithConfig(ctx, cfg, func(context.Context) error {
		return errors.New("transient")
	})

	fmt.Println(errors.Is(err, context.DeadlineExceeded))
	// Output:
	// true
}

// When a custom cause is set via WithCancelCause, errors.Is matches
// both context.Canceled and the custom cause.
func ExampleDoWithConfig_cancelCause() {
	cfg, err := retry.NewConfig(time.Millisecond)
	if err != nil {
		fmt.Printf("config: %v\n", err)
		return
	}

	errShutdown := errors.New("shutdown")
	ctx, cancel := context.WithCancelCause(context.Background())

	var attempts int
	err = retry.DoWithConfig(ctx, cfg, func(context.Context) error {
		attempts++
		if attempts == 2 {
			cancel(errShutdown)
		}
		return errors.New("transient")
	})

	fmt.Println(errors.Is(err, context.Canceled))
	fmt.Println(errors.Is(err, errShutdown))
	// Output:
	// true
	// true
}

// When a custom cause is set via WithDeadlineCause, errors.Is matches
// both context.DeadlineExceeded and the custom cause.
func ExampleDoWithConfig_deadlineCause() {
	cfg, err := retry.NewConfig(time.Millisecond)
	if err != nil {
		fmt.Printf("config: %v\n", err)
		return
	}

	errSlowUpstream := errors.New("upstream too slow")
	ctx, cancel := context.WithDeadlineCause(
		context.Background(), time.Now(), errSlowUpstream,
	)
	defer cancel()

	err = retry.DoWithConfig(ctx, cfg, func(context.Context) error {
		return errors.New("transient")
	})

	fmt.Println(errors.Is(err, context.DeadlineExceeded))
	fmt.Println(errors.Is(err, errSlowUpstream))
	// Output:
	// true
	// true
}
