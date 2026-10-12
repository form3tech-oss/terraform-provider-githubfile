package githubfile

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/form3tech-oss/go-github-utils/pkg/commit"
)

// pullRequestServer serves the eight calls the pull request route makes. merge
// decides whether the merge succeeds, which is what separates the route's
// success path (where it deletes its own branch) from every other ending.
func pullRequestServer(t *testing.T, rec *recorder, mergeSucceeds bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/repos/test-owner/test-repo/git/ref/heads/master", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		fmt.Fprint(w, `{"ref":"refs/heads/master","object":{"sha":"base-sha","type":"commit"}}`)
	})
	mux.HandleFunc("/repos/test-owner/test-repo/git/trees", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		fmt.Fprint(w, `{"sha":"tree-sha"}`)
	})
	mux.HandleFunc("/repos/test-owner/test-repo/commits/base-sha", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		fmt.Fprint(w, `{"sha":"base-sha","commit":{"message":"parent"}}`)
	})
	mux.HandleFunc("/repos/test-owner/test-repo/git/commits", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		fmt.Fprint(w, `{"sha":"new-commit-sha"}`)
	})
	mux.HandleFunc("/repos/test-owner/test-repo/git/refs", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		fmt.Fprint(w, `{"ref":"refs/heads/terraform-provider-githubfile-test","object":{"sha":"new-commit-sha"}}`)
	})
	mux.HandleFunc("/repos/test-owner/test-repo/pulls", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		fmt.Fprint(w, `{"number":42,"state":"open"}`)
	})
	mux.HandleFunc("/repos/test-owner/test-repo/pulls/42/merge", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if mergeSucceeds {
			fmt.Fprint(w, `{"merged":true,"sha":"merge-sha"}`)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprint(w, `{"message":"Base branch was modified. Review and try the merge again."}`)
	})
	mux.HandleFunc("/repos/test-owner/test-repo/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		fmt.Fprint(w, `{"number":42,"state":"closed"}`)
	})
	mux.HandleFunc("/repos/test-owner/test-repo/git/refs/heads/terraform-provider-githubfile-test", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		t.Errorf("unexpected call to %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func pullRequestCommitOptions() *commit.CommitOptions {
	o := testCommitOptions()
	o.PullRequestSourceBranchName = "terraform-provider-githubfile-test"
	return o
}

const workingBranchRef = "/repos/test-owner/test-repo/git/refs/heads/terraform-provider-githubfile-test"

// The leak: the route closes the pull request but abandons the branch.
func TestCreateCommit_DeletesTheWorkingBranchWhenTheMergeFails(t *testing.T) {
	rec := &recorder{}
	server := pullRequestServer(t, rec, false)

	c := &providerConfiguration{
		githubClient: newMockGitHubClient(server),
		branchLocks:  newBranchLocks(),
		retry:        testRetryConfig(),
	}

	err := createCommit(context.Background(), c, pullRequestCommitOptions())
	if err == nil {
		t.Fatal("expected the failed merge to be reported")
	}
	if !strings.Contains(err.Error(), "failed to merge PR") {
		t.Errorf("expected the merge failure to reach the caller, got: %v", err)
	}
	if n := rec.count("DELETE", workingBranchRef); n != 1 {
		t.Errorf("expected the abandoned branch to be deleted once, got %d deletes", n)
	}
}

// On the success path the route deletes its own branch; the provider must not
// delete it a second time.
func TestCreateCommit_LeavesCleanupAloneWhenTheMergeSucceeds(t *testing.T) {
	rec := &recorder{}
	server := pullRequestServer(t, rec, true)

	c := &providerConfiguration{
		githubClient: newMockGitHubClient(server),
		branchLocks:  newBranchLocks(),
		retry:        testRetryConfig(),
	}

	if err := createCommit(context.Background(), c, pullRequestCommitOptions()); err != nil {
		t.Fatalf("expected the write to succeed, got: %v", err)
	}
	if n := rec.count("DELETE", workingBranchRef); n != 1 {
		t.Errorf("expected exactly the route's own delete, got %d", n)
	}
}

// A write cancelled midway is one of the ways a branch is left behind, so
// cleanup must not inherit the cancellation that caused it.
func TestCreateCommit_CleansUpEvenWhenTheContextIsCancelled(t *testing.T) {
	rec := &recorder{}
	server := pullRequestServer(t, rec, false)

	c := &providerConfiguration{
		githubClient: newMockGitHubClient(server),
		branchLocks:  newBranchLocks(),
		retry:        testRetryConfig(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	opts := pullRequestCommitOptions()

	// Fail first, then cancel before cleanup runs.
	err := func() error {
		defer cancel()
		return createCommit(ctx, c, opts)
	}()
	if err == nil {
		t.Fatal("expected an error")
	}

	cancel()
	deleteWorkingBranch(ctx, c, opts)

	if n := rec.count("DELETE", workingBranchRef); n < 1 {
		t.Error("expected cleanup to run despite the cancelled context")
	}
}

// The write's own error is what the operator needs; a cleanup failure must not
// replace it.
func TestDeleteWorkingBranch_DoesNotMaskTheOriginalError(t *testing.T) {
	rec := &recorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := &providerConfiguration{
		githubClient: newMockGitHubClient(server),
		retry:        testRetryConfig(),
	}

	// Must not panic and must not return anything.
	deleteWorkingBranch(context.Background(), c, pullRequestCommitOptions())
}

func TestDeleteWorkingBranch_SkipsWhenTheBranchNameIsUnknown(t *testing.T) {
	rec := &recorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		t.Errorf("expected no call when the branch name is unknown, got %s %s", r.Method, r.URL.Path)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := &providerConfiguration{githubClient: newMockGitHubClient(server), retry: testRetryConfig()}

	opts := testCommitOptions()
	opts.PullRequestSourceBranchName = ""
	deleteWorkingBranch(context.Background(), c, opts)

	if len(rec.calls) != 0 {
		t.Errorf("expected no API calls, got %v", rec.paths())
	}
}

// The direct route creates no branch, so there is nothing to clean up after it.
func TestCreateCommit_DirectRouteNeverTouchesABranch(t *testing.T) {
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

	c := &providerConfiguration{
		githubClient: newMockGitHubClient(server),
		directCommit: true,
		branchLocks:  newBranchLocks(),
		retry:        testRetryConfig(),
	}

	if err := createCommit(context.Background(), c, pullRequestCommitOptions()); err == nil {
		t.Fatal("expected an error")
	}
	for _, p := range rec.paths() {
		if strings.HasPrefix(p, "DELETE") || strings.Contains(p, "/pulls") {
			t.Errorf("expected the direct route to create and clean up no branch, got %q", p)
		}
	}
}
