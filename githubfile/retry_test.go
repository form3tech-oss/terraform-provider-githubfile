package githubfile

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-github/v54/github"
)

func fastRetryConfig() retryConfig {
	return retryConfig{maxRetries: 3, retryWaitMin: time.Millisecond, retryWaitMax: 5 * time.Millisecond}
}

func TestCheckRetry(t *testing.T) {
	resp := func(code int, hdr map[string]string) *http.Response {
		h := http.Header{}
		for k, v := range hdr {
			h.Set(k, v)
		}
		return &http.Response{StatusCode: code, Header: h}
	}

	tests := []struct {
		name string
		resp *http.Response
		want bool
	}{
		{"success", resp(http.StatusOK, nil), false},
		{"too many requests", resp(http.StatusTooManyRequests, nil), true},
		{"rate limited 403", resp(http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "0"}), true},
		{"403 with retry-after", resp(http.StatusForbidden, map[string]string{"Retry-After": "30"}), true},
		{"forbidden for real", resp(http.StatusForbidden, nil), false},
		{"server error", resp(http.StatusInternalServerError, nil), true},
		{"bad gateway", resp(http.StatusBadGateway, nil), true},
		{"not implemented", resp(http.StatusNotImplemented, nil), false},
		{"not found", resp(http.StatusNotFound, nil), false},
		{"unprocessable", resp(http.StatusUnprocessableEntity, nil), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := checkRetry(context.Background(), tt.resp, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("checkRetry(%d) = %v, want %v", tt.resp.StatusCode, got, tt.want)
			}
		})
	}
}

// A 403 that is an authorisation failure rather than a rate limit must not be
// retried: it will never start succeeding, and each attempt costs budget.
func TestCheckRetry_PlainForbiddenIsNotRetried(t *testing.T) {
	r := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}
	r.Header.Set("X-RateLimit-Remaining", "4999")

	got, err := checkRetry(context.Background(), r, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected a 403 with budget remaining to be returned, not retried")
	}
}

func TestBackoff_PrefersRetryAfter(t *testing.T) {
	r := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}
	r.Header.Set("Retry-After", "4")

	got := backoff(time.Second, time.Minute, 0, r)
	if got < 4*time.Second || got >= 6*time.Second {
		t.Errorf("expected roughly the 4s GitHub asked for plus jitter, got %v", got)
	}
}

func TestBackoff_WaitsForTheRateLimitReset(t *testing.T) {
	r := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}
	r.Header.Set("X-RateLimit-Remaining", "0")
	r.Header.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(10*time.Second).Unix(), 10))

	got := backoff(time.Second, time.Minute, 0, r)
	if got < 9*time.Second || got >= 16*time.Second {
		t.Errorf("expected a wait near the 10s reset plus jitter, got %v", got)
	}
}

// A reset an hour away must not become an hour-long sleep inside an apply.
func TestBackoff_IsCappedByRetryWaitMax(t *testing.T) {
	r := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}
	r.Header.Set("X-RateLimit-Remaining", "0")
	r.Header.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))

	max := 30 * time.Second
	got := backoff(time.Second, max, 0, r)
	if got > max+max/2 {
		t.Errorf("expected the wait to be capped near %v, got %v", max, got)
	}
}

func TestBackoff_GrowsAndIsJittered(t *testing.T) {
	min, max := time.Second, time.Minute

	first := backoff(min, max, 0, nil)
	later := backoff(min, max, 3, nil)
	if later <= first {
		t.Errorf("expected the wait to grow with the attempt number, got %v then %v", first, later)
	}

	seen := map[time.Duration]bool{}
	for i := 0; i < 40; i++ {
		seen[backoff(min, max, 2, nil)] = true
	}
	if len(seen) < 5 {
		t.Errorf("expected jittered waits, got %d distinct values across 40 calls", len(seen))
	}
}

// The failure that aborted the rollout: go-github refuses the call locally,
// without a request, so no transport-level retry can see it.
func TestWithRateLimitRetry_HandlesTheLocalRefusal(t *testing.T) {
	calls := 0
	err := withRateLimitRetry(context.Background(), fastRetryConfig(), func() error {
		calls++
		if calls == 1 {
			return &github.RateLimitError{
				Rate:     github.Rate{Limit: 5000, Remaining: 0, Reset: github.Timestamp{Time: time.Now().Add(time.Millisecond)}},
				Response: &http.Response{StatusCode: http.StatusForbidden},
				Message:  "API rate limit of 5000 still exceeded until ..., not making remote request.",
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected the call to be retried after the reset, got: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
}

func TestWithRateLimitRetry_HandlesSecondaryRateLimit(t *testing.T) {
	after := time.Millisecond
	calls := 0
	err := withRateLimitRetry(context.Background(), fastRetryConfig(), func() error {
		calls++
		if calls == 1 {
			return &github.AbuseRateLimitError{
				Response:   &http.Response{StatusCode: http.StatusForbidden},
				Message:    "You have exceeded a secondary rate limit",
				RetryAfter: &after,
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected the call to be retried, got: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
}

func TestWithRateLimitRetry_PassesOtherErrorsStraightBack(t *testing.T) {
	calls := 0
	want := fmt.Errorf("something else")
	err := withRateLimitRetry(context.Background(), fastRetryConfig(), func() error {
		calls++
		return want
	})
	if err != want {
		t.Errorf("expected the original error, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected no retry for an unrelated error, got %d calls", calls)
	}
}

func TestWithRateLimitRetry_GivesUp(t *testing.T) {
	cfg := fastRetryConfig()
	calls := 0
	err := withRateLimitRetry(context.Background(), cfg, func() error {
		calls++
		return &github.RateLimitError{
			Rate:     github.Rate{Reset: github.Timestamp{Time: time.Now().Add(time.Millisecond)}},
			Response: &http.Response{StatusCode: http.StatusForbidden},
			Message:  "still exceeded",
		}
	})
	if err == nil {
		t.Fatal("expected the error once retries were exhausted")
	}
	if calls != cfg.maxRetries+1 {
		t.Errorf("expected %d calls, got %d", cfg.maxRetries+1, calls)
	}
}

func TestWithRateLimitRetry_StopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := retryConfig{maxRetries: 5, retryWaitMin: time.Hour, retryWaitMax: time.Hour}

	calls := 0
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	err := withRateLimitRetry(ctx, cfg, func() error {
		calls++
		return &github.RateLimitError{
			Rate:     github.Rate{Reset: github.Timestamp{Time: time.Now().Add(time.Hour)}},
			Response: &http.Response{StatusCode: http.StatusForbidden},
			Message:  "still exceeded",
		}
	})
	if err != context.Canceled {
		t.Errorf("expected the cancellation to be returned, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected the wait to be interrupted, got %d calls", calls)
	}
}

// End to end through the transport: the client retries a 500 and the caller only
// ever sees the success.
func TestNewRetryableClient_RetriesThroughTheTransport(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ref":"refs/heads/master","object":{"sha":"abc"}}`)
	}))
	defer server.Close()

	hc := newRetryableClient(http.DefaultTransport, fastRetryConfig())
	resp, err := hc.Get(server.URL)
	if err != nil {
		t.Fatalf("expected the retry to succeed, got: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 after retry, got %d", resp.StatusCode)
	}
	if n := atomic.LoadInt32(&attempts); n != 2 {
		t.Errorf("expected 2 attempts, got %d", n)
	}
}

// The token has to be applied to every attempt, not just the first.
func TestNewRetryableClient_AuthenticatesEveryAttempt(t *testing.T) {
	var attempts int32
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()

	hc := newRetryableClient(stubAuthTransport{}, fastRetryConfig())
	resp, err := hc.Get(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if len(seen) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(seen))
	}
	for i, h := range seen {
		if h != "Bearer test-token" {
			t.Errorf("attempt %d was not authenticated, got %q", i+1, h)
		}
	}
}

type stubAuthTransport struct{}

func (stubAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer test-token")
	return http.DefaultTransport.RoundTrip(r)
}
