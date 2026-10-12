package githubfile

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"github.com/google/go-github/v54/github"
	"github.com/hashicorp/go-retryablehttp"
)

const (
	defaultMaxRetries   = 5
	defaultRetryWaitMin = 1 * time.Second
	defaultRetryWaitMax = 60 * time.Second
)

type retryConfig struct {
	maxRetries   int
	retryWaitMin time.Duration
	retryWaitMax time.Duration
}

// newRetryableClient returns an HTTP client that applies tokenTransport to every
// attempt and retries the ones worth retrying.
func newRetryableClient(tokenTransport http.RoundTripper, cfg retryConfig) *http.Client {
	rc := retryablehttp.NewClient()
	rc.HTTPClient = &http.Client{Transport: tokenTransport}
	rc.RetryMax = cfg.maxRetries
	rc.RetryWaitMin = cfg.retryWaitMin
	rc.RetryWaitMax = cfg.retryWaitMax
	rc.CheckRetry = checkRetry
	rc.Backoff = backoff
	// retryablehttp logs every attempt to stderr by default, which in a provider
	// means it lands in Terraform's output uninvited.
	rc.Logger = nil
	return rc.StandardClient()
}

// checkRetry retries what GitHub is telling us to come back for, and nothing
// else. A 403 is only retried when it carries rate-limit information: GitHub
// uses the same status for "you may not do this", which will never succeed.
func checkRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil {
		// Connection-level failure; retryablehttp's own policy already
		// distinguishes the unrecoverable ones.
		return retryablehttp.DefaultRetryPolicy(ctx, resp, err)
	}
	if resp == nil {
		return false, nil
	}

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return true, nil
	case resp.StatusCode == http.StatusForbidden:
		return isRateLimited(resp), nil
	case resp.StatusCode >= 500 && resp.StatusCode != http.StatusNotImplemented:
		return true, nil
	}
	return false, nil
}

func isRateLimited(resp *http.Response) bool {
	if resp.Header.Get("Retry-After") != "" {
		return true
	}
	return resp.Header.Get("X-RateLimit-Remaining") == "0"
}

// backoff prefers what the response asks for over anything we would invent:
// Retry-After when GitHub sends it, the reset timestamp when the primary budget
// is spent, and exponential growth otherwise. Every branch is jittered - callers
// rejected by the same exhausted token would otherwise wake together and spend
// the new budget in the same instant they spent the last one.
func backoff(min, max time.Duration, attemptNum int, resp *http.Response) time.Duration {
	if resp != nil {
		if d, ok := retryAfter(resp); ok {
			return jitter(clamp(d, min, max))
		}
		if d, ok := rateLimitReset(resp); ok {
			return jitter(clamp(d, min, max))
		}
	}

	d := min * time.Duration(1<<uint(attemptNum))
	return jitter(clamp(d, min, max))
}

func retryAfter(resp *http.Response) (time.Duration, bool) {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t), true
	}
	return 0, false
}

func rateLimitReset(resp *http.Response) (time.Duration, bool) {
	if resp.Header.Get("X-RateLimit-Remaining") != "0" {
		return 0, false
	}
	v := resp.Header.Get("X-RateLimit-Reset")
	if v == "" {
		return 0, false
	}
	secs, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return time.Until(time.Unix(secs, 0)), true
}

func clamp(d, min, max time.Duration) time.Duration {
	if d < min {
		return min
	}
	if d > max {
		return max
	}
	return d
}

// jitter spreads a wait over [d, 1.5d).
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d + time.Duration(rand.Int63n(int64(d)/2+1))
}

// withRateLimitRetry runs fn, waiting and retrying while the failure is GitHub
// saying the request cannot be made yet - including go-github's local refusal,
// which never reaches the transport.
//
// It wraps individual calls rather than whole operations. A write is a sequence
// of requests, and re-running one from the start after it has already created
// something would leave the earlier objects behind; retrying the single request
// that was refused resumes where it stopped.
func withRateLimitRetry(ctx context.Context, cfg retryConfig, fn func() error) error {
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}

		wait, ok := rateLimitWait(err)
		if !ok || attempt >= cfg.maxRetries {
			return err
		}

		select {
		case <-time.After(jitter(clamp(wait, cfg.retryWaitMin, cfg.retryWaitMax))):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// rateLimitWait reports how long to wait before retrying err, and whether err is
// a rate limit at all.
func rateLimitWait(err error) (time.Duration, bool) {
	var rle *github.RateLimitError
	if errors.As(err, &rle) {
		if !rle.Rate.Reset.Time.IsZero() {
			return time.Until(rle.Rate.Reset.Time), true
		}
		return 0, true
	}

	var arle *github.AbuseRateLimitError
	if errors.As(err, &arle) {
		if arle.RetryAfter != nil {
			return *arle.RetryAfter, true
		}
		return 0, true
	}

	return 0, false
}
