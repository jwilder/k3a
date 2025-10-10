package retry

import (
	"context"
	"time"
)

// RetryWithDelays # noqa: E501
//
// RetryWithDelays executes the provided operation function up to len(delays)+1 times (an initial attempt plus one retry per delay).
// It waits for the corresponding delay element before each retry. A nil error from op stops immediately with success.
// If isRetriable is non-nil and returns false for a given error, the retry loop stops early and that error is returned.
// This helper is intentionally simple and deterministic so that callers (e.g., cluster/create.go key vault role assignments)
// can specify exact propagation backoff sequences (10s, 30s, 60s) instead of exponential patterns.
//
// :param ctx: context for cancellation
// :type ctx: context.Context
// :param delays: slice of delays (e.g., []time.Duration{10*time.Second,30*time.Second,60*time.Second}) applied before subsequent attempts
// :type delays: []time.Duration
// :param isRetriable: optional classifier; if nil all errors are considered retriable, otherwise must return true to continue retrying
// :type isRetriable: func(error) bool
// :param op: the operation to execute; should be idempotent or safe to retry
// :type op: func(context.Context) error
// :return: final error if all attempts fail, else nil
// :rtype: error
//
// Example:
//
//	import (
//		"errors"
//		"github.com/Azure/azure-sdk-for-go/sdk/azcore"
//	)
//
//	delays := []time.Duration{10*time.Second,30*time.Second,60*time.Second}
//	isRetriable := func(err error) bool {
//		var respErr interface{ StatusCode() int }
//		if errors.As(err, &respErr) {
//			sc := respErr.StatusCode()
//			return sc == 404 || sc == 409 || sc == 429 || (sc >= 500 && sc < 600)
//		}
//		return true
//	}
//	retry.RetryWithDelays(ctx, delays, isRetriable, func(ctx context.Context) error { return doThing() })
func RetryWithDelays(ctx context.Context, delays []time.Duration, isRetriable func(error) bool, op func(context.Context) error) error {
	var err error
	for attempt := 0; attempt <= len(delays); attempt++ {
		if ctx.Err() != nil { // context cancelled or deadline exceeded
			return ctx.Err()
		}
		err = op(ctx)
		if err == nil {
			return nil
		}
		if isRetriable != nil && !isRetriable(err) { // non-retriable
			return err
		}
		if attempt == len(delays) { // no more retries
			break
		}
		select {
		case <-time.After(delays[attempt]):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
