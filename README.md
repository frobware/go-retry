# retry

`retry` is a small Go package for retrying operations with configurable
backoff, optional jitter, attempt limits, and context-aware
cancellation. It has no external dependencies.

- Fixed, linear, and exponential backoff
- Optional jitter and interval caps
- Early stop via permanent errors
- Attempt limits and context cancellation
- Custom backoff and jitter functions

## Usage

```go
cfg, err := retry.NewConfig(100 * time.Millisecond)
if err != nil {
    return err
}

err = retry.DoWithConfig(ctx, cfg, func(ctx context.Context) error {
    resp, err := callRemoteSystem(ctx)
    if err != nil {
        return err // transient: will be retried
    }
    if resp.StatusCode == 400 {
        return retry.Permanent(errors.New("bad request")) // stop immediately
    }
    return nil
})
```

`DoWithConfig` retries when the operation returns a non-nil error that
is not marked permanent.

It stops when:

- the operation returns `nil`
- the operation returns `retry.Permanent(err)`
- the context is cancelled
- the configured attempt limit is reached

The context passed to the operation is the same context given to
`DoWithConfig`, so the operation can observe cancellation directly. The
retry loop also checks the context between attempts.

A `Config` created with `NewConfig` may be reused across calls. The
initial interval must be greater than zero.

See [example_test.go](example_test.go) for more examples.

## Backoff strategies

The package provides three built-in backoff strategies. Set one with
`WithStrategy`; the default is fixed.

| Strategy    | Usage                  | Sequence (100ms base)           |
|-------------|------------------------|---------------------------------|
| Fixed       | `FixedBackoff()`       | 100ms, 100ms, 100ms, ...        |
| Linear      | `LinearBackoff()`      | 100ms, 200ms, 300ms, 400ms, ... |
| Exponential | `ExponentialBackoff()` | 100ms, 200ms, 400ms, 800ms, ... |

You may also supply a custom `BackoffFunc`. The `retry` parameter
starts at 1 for the first retry after the initial failure.

```go
cfg, err := retry.NewConfig(100*time.Millisecond,
    retry.WithStrategy(func(initial time.Duration, retry int64) time.Duration {
        return initial * time.Duration(retry*retry) // quadratic
    }),
)
```

## Jitter

Jitter adds an extra duration to the base interval to spread out
retries. `WithMaxJitter` caps the jitter contribution.
`WithMaxInterval` caps both the scheduled base interval and the final
sleep duration.

```go
cfg, err := retry.NewConfig(100*time.Millisecond,
    retry.WithJitterFunc(func(base time.Duration) time.Duration {
        return base / 2
    }),
    retry.WithMaxJitter(250*time.Millisecond),
    retry.WithMaxInterval(2*time.Second),
)
```

## Attempt limits

By default, retries continue until success, a permanent error, or
context cancellation.

`WithMaxAttempts(0)` means unlimited attempts.

`WithMaxAttempts(n)` limits the total number of attempts, including the
initial call.

```go
cfg, err := retry.NewConfig(100*time.Millisecond,
    retry.WithMaxAttempts(5),
)
```

With `WithMaxAttempts(5)`:

- attempt 1: initial call
- attempts 2-5: retries
- after attempt 5 fails, `DoWithConfig` returns an error wrapping
  `retry.ErrRetryAborted`

## Context cancellation

`DoWithConfig` stops retrying when the context is cancelled.

If the context has a cancellation cause, that cause is preserved in the
returned error chain.

```go
cause := errors.New("shutting down")
ctx, cancel := context.WithCancelCause(context.Background())
cancel(cause)

err := retry.DoWithConfig(ctx, cfg, func(ctx context.Context) error {
    return errors.New("transient failure")
})

// errors.Is(err, cause) == true
```

## Error behaviour

Public sentinel errors:

- `retry.ErrRetryBadOption`
- `retry.ErrRetryAborted`

Configuration errors from `NewConfig` wrap `ErrRetryBadOption`.

When all attempts are exhausted, `DoWithConfig` returns an error
wrapping both the last operation error and `ErrRetryAborted`.

```go
if errors.Is(err, retry.ErrRetryBadOption) {
    // invalid config
}

if errors.Is(err, retry.ErrRetryAborted) {
    // attempts exhausted
}
```

## Custom function semantics

- A `BackoffFunc` must return a positive duration. Zero or negative
  values are clamped to the initial interval.
- A `JitterFunc` must return a non-negative duration. Negative values
  are clamped to zero.
- Values that would overflow are saturated to `math.MaxInt64`.

## Benchmarks

A design goal of this package is low overhead both on the fast path
and during retries: constant allocation cost regardless of retry
count.

All results were measured on an Apple M4 using constant/fixed backoff.

### No retries (operation succeeds first try)

| ns/op | B/op | allocs/op |
|------:|-----:|----------:|
|  1.81 |    0 |         0 |

### With retries

Uses `time.Nanosecond` intervals to isolate retry overhead from
actual sleep time. Each iteration fails the stated number of times
before succeeding.

| Retries |  ns/op | B/op | allocs/op |
|--------:|-------:|-----:|----------:|
|       1 |    189 |  248 |         3 |
|      10 |  2,572 |  248 |         3 |
|     100 | 21,856 |  248 |         3 |

Allocations are constant regardless of retry count: 3 allocs/248 B
from the single timer allocation on the first retry.

Reproduce with:

```
make bench              # no retries
make bench-retries      # with retries
```

## Licence

[MIT](LICENSE)
