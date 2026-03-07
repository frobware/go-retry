package retry_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/frobware/go-retry"
)

var errRetry = errors.New("retry")

// BenchmarkDoNoRetry measures the raw overhead of the retry machinery
// when the operation succeeds immediately (no retries, no sleeping).
func BenchmarkDoNoRetry(b *testing.B) {
	ctx := context.Background()
	op := func(context.Context) error { return nil }
	cfg, err := retry.NewConfig(time.Second)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for range b.N {
		_ = retry.DoWithConfig(ctx, cfg, op)
	}
}

// BenchmarkDoRetries measures the cost of the retry loop including
// schedule computation, timer handling, and select. Uses
// time.Nanosecond to minimise actual wait time, though timer and
// scheduler effects are not fully eliminated.
func BenchmarkDoRetries(b *testing.B) {
	ctx := context.Background()
	cfg, err := retry.NewConfig(time.Nanosecond,
		retry.WithMaxAttempts(110),
	)
	if err != nil {
		b.Fatal(err)
	}

	for _, retries := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("retries=%d", retries), func(b *testing.B) {
			var callCount int
			op := func(context.Context) error {
				callCount++
				if callCount <= retries {
					return errRetry
				}
				callCount = 0
				return nil
			}

			b.ResetTimer()
			for range b.N {
				_ = retry.DoWithConfig(ctx, cfg, op)
			}
		})
	}
}
