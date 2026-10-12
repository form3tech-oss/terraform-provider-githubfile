package githubfile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/form3tech-oss/go-github-utils/pkg/commit"
	"github.com/google/go-github/v54/github"
)

// recorder captures the request path and decoded body of every call the client
// makes, so a test can assert on the sequence rather than on one response.
type recorder struct {
	mu    sync.Mutex
	calls []recordedCall
}

type recordedCall struct {
	method string
	path   string
	body   map[string]any
}

func (r *recorder) record(req *http.Request) map[string]any {
	var body map[string]any
	if b, err := io.ReadAll(req.Body); err == nil && len(b) > 0 {
		_ = json.Unmarshal(b, &body)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recordedCall{method: req.Method, path: req.URL.Path, body: body})
	return body
}

func (r *recorder) paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.calls))
	for _, c := range r.calls {
		out = append(out, c.method+" "+c.path)
	}
	return out
}

func (r *recorder) count(method, path string) int {
	n := 0
	for _, c := range r.calls {
		if c.method == method && c.path == path {
			n++
		}
	}
	return n
}

func testRetryConfig() retryConfig {
	return retryConfig{maxRetries: 2, retryWaitMin: time.Millisecond, retryWaitMax: 2 * time.Millisecond}
}

func testCommitOptions() *commit.CommitOptions {
	return &commit.CommitOptions{
		RepoOwner:     "test-owner",
		RepoName:      "test-repo",
		Branch:        "master",
		CommitMessage: "[skip ci] Create \"some/file.txt\".",
		Username:      "jeeves",
		Email:         "jeeves@example.test",
		Changes: []*github.TreeEntry{{
			Content: github.String("hello"),
			Mode:    github.String("100644"),
			Path:    github.String("some/file.txt"),
			Type:    github.String("blob"),
		}},
		MaxRetries:   3,
		RetryBackoff: time.Millisecond,
	}
}

// directCommitServer serves the four calls the direct route makes. updateRef is
// consulted for each ref update so a test can fail the first and pass the next.
func directCommitServer(t *testing.T, rec *recorder, headSHA func() string, updateRef func(attempt int, w http.ResponseWriter) bool) *httptest.Server {
	t.Helper()
	attempts := 0
	mux := http.NewServeMux()

	mux.HandleFunc("/repos/test-owner/test-repo/git/ref/heads/master", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ref":"refs/heads/master","object":{"sha":%q,"type":"commit"}}`, headSHA())
	})
	mux.HandleFunc("/repos/test-owner/test-repo/git/trees", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"sha":"tree-sha"}`)
	})
	mux.HandleFunc("/repos/test-owner/test-repo/git/commits", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"sha":"new-commit-sha"}`)
	})
	mux.HandleFunc("/repos/test-owner/test-repo/git/refs/heads/master", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		attempts++
		if updateRef(attempts, w) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ref":"refs/heads/master","object":{"sha":"new-commit-sha"}}`)
		}
	})

	// Anything else is a route the direct path should never take.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		t.Errorf("unexpected call to %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func writeNonFastForward(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	fmt.Fprint(w, `{"message":"Update is not a fast forward","documentation_url":"https://docs.github.com/rest"}`)
}

func TestCreateCommitDirect_WritesWithoutPullRequest(t *testing.T) {
	rec := &recorder{}
	server := directCommitServer(t, rec,
		func() string { return "base-sha" },
		func(_ int, _ http.ResponseWriter) bool { return true },
	)

	err := createCommitDirect(context.Background(), newMockGitHubClient(server), testRetryConfig(), testCommitOptions())
	if err != nil {
		t.Fatalf("expected the commit to be written, got: %v", err)
	}

	want := []string{
		"GET /repos/test-owner/test-repo/git/ref/heads/master",
		"POST /repos/test-owner/test-repo/git/trees",
		"POST /repos/test-owner/test-repo/git/commits",
		"PATCH /repos/test-owner/test-repo/git/refs/heads/master",
	}
	got := rec.paths()
	if len(got) != len(want) {
		t.Fatalf("expected exactly %d calls %v, got %d: %v", len(want), want, len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestCreateCommitDirect_RefUpdateIsCompareAndSwap(t *testing.T) {
	rec := &recorder{}
	server := directCommitServer(t, rec,
		func() string { return "base-sha" },
		func(_ int, _ http.ResponseWriter) bool { return true },
	)

	if err := createCommitDirect(context.Background(), newMockGitHubClient(server), testRetryConfig(), testCommitOptions()); err != nil {
		t.Fatalf("expected the commit to be written, got: %v", err)
	}

	for _, c := range rec.calls {
		if c.method != "PATCH" {
			continue
		}
		if force, ok := c.body["force"].(bool); !ok || force {
			t.Fatalf("expected the ref update to be sent with force=false, got body %v", c.body)
		}
		if sha, _ := c.body["sha"].(string); sha != "new-commit-sha" {
			t.Fatalf("expected the ref to be moved to the new commit, got sha %q", sha)
		}
	}
}

func TestCreateCommitDirect_ParentIsTheBranchTip(t *testing.T) {
	rec := &recorder{}
	server := directCommitServer(t, rec,
		func() string { return "base-sha" },
		func(_ int, _ http.ResponseWriter) bool { return true },
	)

	if err := createCommitDirect(context.Background(), newMockGitHubClient(server), testRetryConfig(), testCommitOptions()); err != nil {
		t.Fatalf("expected the commit to be written, got: %v", err)
	}

	for _, c := range rec.calls {
		if c.path != "/repos/test-owner/test-repo/git/commits" {
			continue
		}
		parents, ok := c.body["parents"].([]any)
		if !ok || len(parents) != 1 || parents[0] != "base-sha" {
			t.Fatalf("expected the commit to be parented on the branch tip, got %v", c.body["parents"])
		}
	}
}

// The point of the direct route: a branch that moves under the write is retried
// by rebuilding on the new tip, not by re-attempting a commit whose parent is
// now stale.
func TestCreateCommitDirect_RebuildsOnTheNewTipWhenBranchMoves(t *testing.T) {
	rec := &recorder{}
	head := "base-sha"
	server := directCommitServer(t, rec,
		func() string { return head },
		func(attempt int, w http.ResponseWriter) bool {
			if attempt == 1 {
				// Someone else landed on master between our read and our swap.
				head = "moved-sha"
				writeNonFastForward(w)
				return false
			}
			return true
		},
	)

	if err := createCommitDirect(context.Background(), newMockGitHubClient(server), testRetryConfig(), testCommitOptions()); err != nil {
		t.Fatalf("expected the retry to succeed, got: %v", err)
	}

	if n := rec.count("GET", "/repos/test-owner/test-repo/git/ref/heads/master"); n != 2 {
		t.Errorf("expected the branch to be re-read on retry, got %d reads", n)
	}
	if n := rec.count("POST", "/repos/test-owner/test-repo/git/trees"); n != 2 {
		t.Errorf("expected the tree to be rebuilt on retry, got %d creates", n)
	}

	var parents []string
	for _, c := range rec.calls {
		if c.path == "/repos/test-owner/test-repo/git/commits" {
			if p, ok := c.body["parents"].([]any); ok && len(p) == 1 {
				parents = append(parents, fmt.Sprint(p[0]))
			}
		}
	}
	if len(parents) != 2 || parents[0] != "base-sha" || parents[1] != "moved-sha" {
		t.Fatalf("expected the second commit to be parented on the moved tip, got parents %v", parents)
	}
}

func TestCreateCommitDirect_GivesUpAfterMaxRetries(t *testing.T) {
	rec := &recorder{}
	server := directCommitServer(t, rec,
		func() string { return "base-sha" },
		func(_ int, w http.ResponseWriter) bool { writeNonFastForward(w); return false },
	)

	opts := testCommitOptions()
	opts.MaxRetries = 2

	err := createCommitDirect(context.Background(), newMockGitHubClient(server), testRetryConfig(), opts)
	if err == nil {
		t.Fatal("expected an error once the retries were exhausted")
	}
	if !strings.Contains(err.Error(), "the branch moved under each") {
		t.Errorf("expected the error to say the branch kept moving, got: %v", err)
	}
	if n := rec.count("PATCH", "/repos/test-owner/test-repo/git/refs/heads/master"); n != 2 {
		t.Errorf("expected exactly MaxRetries attempts, got %d", n)
	}
}

func TestCreateCommitDirect_DoesNotRetryOtherFailures(t *testing.T) {
	rec := &recorder{}
	server := directCommitServer(t, rec,
		func() string { return "base-sha" },
		func(_ int, w http.ResponseWriter) bool {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message":"Reference does not exist"}`)
			return false
		},
	)

	err := createCommitDirect(context.Background(), newMockGitHubClient(server), testRetryConfig(), testCommitOptions())
	if err == nil {
		t.Fatal("expected the error to be returned")
	}
	if strings.Contains(err.Error(), "the branch moved under each") {
		t.Errorf("expected a failure that is not a branch race to be returned as-is, got: %v", err)
	}
	if n := rec.count("PATCH", "/repos/test-owner/test-repo/git/refs/heads/master"); n != 1 {
		t.Errorf("expected a single attempt for a non-race failure, got %d", n)
	}
}

// An override naming the branch tip means "base the tree on what is there now",
// so a retry has to move it to the new tip; otherwise the rebuilt tree would
// revert whoever moved the branch.
func TestCreateCommitDirect_TipTrackingOverrideFollowsTheBranch(t *testing.T) {
	rec := &recorder{}
	head := "base-sha"
	server := directCommitServer(t, rec,
		func() string { return head },
		func(attempt int, w http.ResponseWriter) bool {
			if attempt == 1 {
				head = "moved-sha"
				writeNonFastForward(w)
				return false
			}
			return true
		},
	)

	opts := testCommitOptions()
	opts.BaseTreeOverride = github.String("base-sha")

	if err := createCommitDirect(context.Background(), newMockGitHubClient(server), testRetryConfig(), opts); err != nil {
		t.Fatalf("expected the retry to succeed, got: %v", err)
	}

	var baseTrees []string
	for _, c := range rec.calls {
		if c.path == "/repos/test-owner/test-repo/git/trees" {
			baseTrees = append(baseTrees, fmt.Sprint(c.body["base_tree"]))
		}
	}
	if len(baseTrees) != 2 || baseTrees[0] != "base-sha" || baseTrees[1] != "moved-sha" {
		t.Fatalf("expected the override to follow the branch tip, got base trees %v", baseTrees)
	}
}

// An override naming anything else is meant literally and must survive a retry.
func TestCreateCommitDirect_LiteralOverrideIsPreserved(t *testing.T) {
	rec := &recorder{}
	head := "base-sha"
	server := directCommitServer(t, rec,
		func() string { return head },
		func(attempt int, w http.ResponseWriter) bool {
			if attempt == 1 {
				head = "moved-sha"
				writeNonFastForward(w)
				return false
			}
			return true
		},
	)

	opts := testCommitOptions()
	opts.BaseTreeOverride = github.String("")

	if err := createCommitDirect(context.Background(), newMockGitHubClient(server), testRetryConfig(), opts); err != nil {
		t.Fatalf("expected the retry to succeed, got: %v", err)
	}

	for _, c := range rec.calls {
		if c.path != "/repos/test-owner/test-repo/git/trees" {
			continue
		}
		if v, ok := c.body["base_tree"]; ok && v != "" {
			t.Fatalf("expected the empty override to be preserved, got base_tree %v", v)
		}
	}
}

func TestCreateCommit_RoutesOnTheDirectCommitSetting(t *testing.T) {
	rec := &recorder{}
	server := directCommitServer(t, rec,
		func() string { return "base-sha" },
		func(_ int, _ http.ResponseWriter) bool { return true },
	)

	c := &providerConfiguration{githubClient: newMockGitHubClient(server), directCommit: true, retry: testRetryConfig()}
	if err := createCommit(context.Background(), c, testCommitOptions()); err != nil {
		t.Fatalf("expected the direct route to be taken, got: %v", err)
	}

	for _, p := range rec.paths() {
		if strings.Contains(p, "/pulls") {
			t.Fatalf("expected no pull request to be opened, got call %q", p)
		}
	}
	if rec.count("PATCH", "/repos/test-owner/test-repo/git/refs/heads/master") != 1 {
		t.Error("expected the branch ref to be updated directly")
	}
}

func TestIsBranchMoved(t *testing.T) {
	resp := func(code int) *http.Response { return &http.Response{StatusCode: code} }

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "non fast forward",
			err:  &github.ErrorResponse{Response: resp(http.StatusUnprocessableEntity), Message: "Update is not a fast forward"},
			want: true,
		},
		{
			name: "hyphenated wording",
			err:  &github.ErrorResponse{Response: resp(http.StatusUnprocessableEntity), Message: "not a fast-forward update"},
			want: true,
		},
		{
			name: "other unprocessable entity",
			err:  &github.ErrorResponse{Response: resp(http.StatusUnprocessableEntity), Message: "Reference does not exist"},
			want: false,
		},
		{
			name: "rate limited",
			err:  &github.RateLimitError{Message: "API rate limit exceeded"},
			want: false,
		},
		{
			name: "not a github error",
			err:  fmt.Errorf("dial tcp: connection refused"),
			want: false,
		},
		{
			name: "no response attached",
			err:  &github.ErrorResponse{Message: "Update is not a fast forward"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBranchMoved(tt.err); got != tt.want {
				t.Errorf("isBranchMoved(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
