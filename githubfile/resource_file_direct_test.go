// Copyright 2019 Form3 Financial Cloud
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package githubfile

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"

	"github.com/form3tech-oss/go-github-utils/pkg/branch"
	"github.com/google/go-github/v54/github"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// createBranchForTest branches off master for the duration of one test, so that
// tests adding their own do not collide over the single shared branch.
func createBranchForTest(t *testing.T, name string) {
	t.Helper()

	client := newGitHubClient()
	ctx := context.Background()

	sha, err := branch.GetSHAForBranch(ctx, client, testRepoOwner, testRepoName, "master")
	if err != nil {
		t.Fatalf("failed to get SHA for master branch: %v", err)
	}

	ref := &github.Reference{
		Ref:    github.String("refs/heads/" + name),
		Object: &github.GitObject{SHA: github.String(sha)},
	}
	if _, _, err := client.Git.CreateRef(ctx, testRepoOwner, testRepoName, ref); err != nil {
		t.Fatalf("failed to create test branch %s: %v", name, err)
	}

	t.Cleanup(func() {
		if _, err := client.Git.DeleteRef(context.Background(), testRepoOwner, testRepoName, "refs/heads/"+name); err != nil {
			fmt.Fprintf(os.Stderr, "failed to delete test branch %s: %v\n", name, err)
		}
	})
}

func testAccFileDirectCommitConfig(branchName, contents string) string {
	return fmt.Sprintf(`
provider "githubfile" {
    direct_commit = true
}

resource "githubfile_file" "direct" {
    repository_owner = %q
    repository_name  = %q
    branch           = %q
    path             = "foo/bar/baz/DIRECT.md"
    contents         = %q
}
`, testRepoOwner, testRepoName, branchName, contents)
}

// TestAccResourceFile_directCommit drives the full resource lifecycle against a
// real repository with the direct route selected.
//
// The attribute checks are the same as the pull request route's, because the
// resource is meant to behave identically either way. What makes this worth
// running is everything underneath: that GitHub accepts a tree, a commit and a
// compare-and-swap ref update built by hand, and that destroy can remove a file
// the same way. None of that is exercised by a mock.
func TestAccResourceFile_directCommit(t *testing.T) {
	var before file

	branchName := fmt.Sprintf("acc-test-direct-%d", rand.Int63())
	resourceName := "githubfile_file.direct"

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			createBranchForTest(t, branchName)
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckFileDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccFileDirectCommitConfig(branchName, "foo\nbar\nbaz"),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckFileExists(resourceName, &before),
					resource.TestCheckResourceAttr(resourceName, "repository_name", testRepoName),
					resource.TestCheckResourceAttr(resourceName, "repository_owner", testRepoOwner),
					resource.TestCheckResourceAttr(resourceName, "branch", branchName),
					resource.TestCheckResourceAttr(resourceName, "path", "foo/bar/baz/DIRECT.md"),
					resource.TestCheckResourceAttr(resourceName, "contents", "foo\nbar\nbaz"),
					testAccCheckTipIsADirectCommit(branchName, `Create "foo/bar/baz/DIRECT.md".`),
					testAccCheckTipAuthoredByTheConfiguredIdentity(branchName),
				),
			},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: testAccFileDirectCommitConfig(branchName, "foo\nbar\nqux"),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckFileExists(resourceName, &before),
					resource.TestCheckResourceAttr(resourceName, "contents", "foo\nbar\nqux"),
					testAccCheckTipIsADirectCommit(branchName, `Update "foo/bar/baz/DIRECT.md".`),
				),
			},
		},
	})
}

// testAccCheckTipIsADirectCommit asserts that the write landed as a commit on
// the branch itself rather than through a pull request.
//
// This is what separates the two routes, and it is not visible in the resource
// state - the file is in place either way. The pull request route leaves a merge
// commit at the tip: two parents, and a message naming the pull request rather
// than the file. The direct route leaves the file commit itself: one parent, and
// the message the provider composed.
//
// It also covers the prefix. On the pull request route the prefixed commit is
// buried under the merge, so a marker such as "[skip ci]" never reaches the tip
// and never takes effect; here it does, and the assertion fails if it stops
// being applied.
func testAccCheckTipIsADirectCommit(branchName, wantSubject string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		client := newGitHubClient()
		ctx := context.Background()

		sha, err := branch.GetSHAForBranch(ctx, client, testRepoOwner, testRepoName, branchName)
		if err != nil {
			return fmt.Errorf("failed to read the tip of %q: %w", branchName, err)
		}

		c, _, err := client.Repositories.GetCommit(ctx, testRepoOwner, testRepoName, sha, nil)
		if err != nil {
			return fmt.Errorf("failed to read commit %s: %w", sha, err)
		}

		if n := len(c.Parents); n != 1 {
			return fmt.Errorf("expected the tip of %q to be a direct commit with one parent, got %d - a merge commit means the pull request route ran", branchName, n)
		}

		got := c.GetCommit().GetMessage()
		if !strings.Contains(got, wantSubject) {
			return fmt.Errorf("expected the tip commit message to contain %q, got %q", wantSubject, got)
		}

		if prefix := strings.TrimSpace(os.Getenv("COMMIT_MESSAGE_PREFIX")); prefix != "" {
			if !strings.HasPrefix(got, prefix) {
				return fmt.Errorf("expected the tip commit message to start with the configured prefix %q, got %q", prefix, got)
			}
		}

		return nil
	}
}

// testAccCheckTipAuthoredByTheConfiguredIdentity guards the author fields the
// provider sets by hand on the direct route, which the pull request route gets
// from the same place but which no mock verifies against GitHub.
func testAccCheckTipAuthoredByTheConfiguredIdentity(branchName string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		client := newGitHubClient()
		ctx := context.Background()

		sha, err := branch.GetSHAForBranch(ctx, client, testRepoOwner, testRepoName, branchName)
		if err != nil {
			return fmt.Errorf("failed to read the tip of %q: %w", branchName, err)
		}

		c, _, err := client.Repositories.GetCommit(ctx, testRepoOwner, testRepoName, sha, nil)
		if err != nil {
			return fmt.Errorf("failed to read commit %s: %w", sha, err)
		}

		author := c.GetCommit().GetAuthor()
		if want := os.Getenv("GITHUB_USERNAME"); want != "" && author.GetName() != want {
			return fmt.Errorf("expected the commit author name to be %q, got %q", want, author.GetName())
		}
		if want := os.Getenv("GITHUB_EMAIL"); want != "" && author.GetEmail() != want {
			return fmt.Errorf("expected the commit author email to be %q, got %q", want, author.GetEmail())
		}
		return nil
	}
}

// TestAccResourceFile_directCommitLeavesNoBranches asserts the property the
// pull request route cannot offer: nothing to clean up.
//
// The pull request route pushes to a working branch named
// terraform-provider-githubfile-<nanos> and deletes it only once the merge has
// succeeded, which is how a rollout came to leave several hundred behind. The
// direct route never creates one, so a write that fails leaves nothing.
func TestAccResourceFile_directCommitLeavesNoBranches(t *testing.T) {
	branchName := fmt.Sprintf("acc-test-direct-clean-%d", rand.Int63())

	// Snapshotted in PreCheck rather than when the check is built, so that a
	// run without TF_ACC skips without making an API call.
	var before map[string]bool

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			createBranchForTest(t, branchName)
			before = listWorkingBranches(t)
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckFileDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccFileDirectCommitConfig(branchName, "no working branches please"),
				Check: func(*terraform.State) error {
					var added []string
					for name := range listWorkingBranches(t) {
						if !before[name] {
							added = append(added, name)
						}
					}
					if len(added) > 0 {
						return fmt.Errorf("expected the direct route to create no working branches, found %v", added)
					}
					return nil
				},
			},
		},
	})
}

func listWorkingBranches(t *testing.T) map[string]bool {
	t.Helper()

	client := newGitHubClient()
	out := map[string]bool{}

	opts := &github.ReferenceListOptions{Ref: "heads/terraform-provider-githubfile-"}
	for {
		refs, resp, err := client.Git.ListMatchingRefs(context.Background(), testRepoOwner, testRepoName, opts)
		if err != nil {
			// Not being able to list them is not a reason to fail the write
			// under test; the assertion simply has nothing to compare.
			t.Logf("could not list working branches: %v", err)
			return out
		}
		for _, r := range refs {
			out[r.GetRef()] = true
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out
}

func testAccFilePullRequestConfig(branchName string) string {
	return fmt.Sprintf(`
resource "githubfile_file" "pr" {
    repository_owner = %q
    repository_name  = %q
    branch           = %q
    path             = "foo/bar/baz/PR.md"
    contents         = "written through a pull request"
}
`, testRepoOwner, testRepoName, branchName)
}

// TestAccResourceFile_pullRequestRouteLeavesAMergeCommit is the counterpart to
// the direct route's tip assertion, and pins the behaviour the two routes do not
// share.
//
// It is what makes `commit_message_prefix` mean different things on each route.
// Here the prefixed commit ends up underneath a merge commit, so a marker such
// as "[skip ci]" is not at the tip and the CI it was meant to suppress still
// runs. Selecting the direct route moves that same prefix to the tip, where it
// starts taking effect - which is a change in behaviour worth having a test say
// out loud rather than a comment.
//
// Kept separate from TestAccResourceFile_basic so that the shape assertion can
// be adjusted, or dropped, without touching the lifecycle coverage.
func TestAccResourceFile_pullRequestRouteLeavesAMergeCommit(t *testing.T) {
	branchName := fmt.Sprintf("acc-test-pr-%d", rand.Int63())

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			createBranchForTest(t, branchName)
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckFileDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccFilePullRequestConfig(branchName),
				Check:  testAccCheckTipIsAMergeCommit(branchName),
			},
		},
	})
}

func testAccCheckTipIsAMergeCommit(branchName string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		client := newGitHubClient()
		ctx := context.Background()

		sha, err := branch.GetSHAForBranch(ctx, client, testRepoOwner, testRepoName, branchName)
		if err != nil {
			return fmt.Errorf("failed to read the tip of %q: %w", branchName, err)
		}

		c, _, err := client.Repositories.GetCommit(ctx, testRepoOwner, testRepoName, sha, nil)
		if err != nil {
			return fmt.Errorf("failed to read commit %s: %w", sha, err)
		}

		if n := len(c.Parents); n != 2 {
			return fmt.Errorf("expected the pull request route to leave a merge commit with two parents at the tip of %q, got %d", branchName, n)
		}
		return nil
	}
}
