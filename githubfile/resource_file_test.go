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
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/form3tech-oss/go-github-utils/pkg/branch"
	ghfileutils "github.com/form3tech-oss/go-github-utils/pkg/file"
	"github.com/google/go-github/v54/github"
	"github.com/hashicorp/terraform-plugin-sdk/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/terraform"
	"golang.org/x/oauth2"
)

const (
	testRepoOwner = "form3tech-oss"
	testRepoName  = "terraform-provider-githubfile"
)

var testBranchName string

func newGitHubClient() *github.Client {
	token := os.Getenv("GITHUB_TOKEN")
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	return github.NewClient(tc)
}

func TestMain(m *testing.M) {
	testBranchName = fmt.Sprintf("acc-test-%d", rand.Int63())

	code := m.Run()

	// Cleanup: delete the test branch if it was created
	if os.Getenv("GITHUB_TOKEN") != "" {
		cleanupTestBranch()
	}

	os.Exit(code)
}

func cleanupTestBranch() {
	client := newGitHubClient()
	ref := fmt.Sprintf("refs/heads/%s", testBranchName)
	_, err := client.Git.DeleteRef(context.Background(), testRepoOwner, testRepoName, ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to delete test branch %s: %v\n", testBranchName, err)
	}
}

func createTestBranch(t *testing.T) {
	t.Helper()

	client := newGitHubClient()
	ctx := context.Background()

	// Get the SHA for the default branch (master)
	sha, err := branch.GetSHAForBranch(ctx, client, testRepoOwner, testRepoName, "master")
	if err != nil {
		t.Fatalf("failed to get SHA for master branch: %v", err)
	}

	// Create the test branch
	ref := &github.Reference{
		Ref: github.String(fmt.Sprintf("refs/heads/%s", testBranchName)),
		Object: &github.GitObject{
			SHA: github.String(sha),
		},
	}
	_, _, err = client.Git.CreateRef(ctx, testRepoOwner, testRepoName, ref)
	if err != nil {
		t.Fatalf("failed to create test branch %s: %v", testBranchName, err)
	}
}

func testAccFileCreateConfig() string {
	return fmt.Sprintf(`
resource "githubfile_file" "foo" {
    repository_owner = "%s"
    repository_name  = "%s"
    branch           = "%s"
    path             = "foo/bar/baz/README.md"
    contents         = "foo\nbar\nbaz"
}
`, testRepoOwner, testRepoName, testBranchName)
}

func testAccFileUpdateConfig() string {
	return fmt.Sprintf(`
resource "githubfile_file" "foo" {
    repository_owner = "%s"
    repository_name  = "%s"
    branch           = "%s"
    path             = "foo/bar/baz/README.md"
    contents         = "foo\nbar\nqux"
}
`, testRepoOwner, testRepoName, testBranchName)
}

func TestAccResourceFile_basic(t *testing.T) {
	var (
		before file
	)

	resourceName := "githubfile_file.foo"

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			createTestBranch(t)
		},
		IDRefreshName: resourceName,
		Providers:     testAccProviders,
		CheckDestroy:  testAccCheckFileDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccFileCreateConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckFileExists(resourceName, &before),
					resource.TestCheckResourceAttr(resourceName, repositoryNameAttributeName, testRepoName),
					resource.TestCheckResourceAttr(resourceName, repositoryOwnerAttributeName, testRepoOwner),
					resource.TestCheckResourceAttr(resourceName, branchAttributeName, testBranchName),
					resource.TestCheckResourceAttr(resourceName, pathAttributeName, "foo/bar/baz/README.md"),
					resource.TestCheckResourceAttr(resourceName, contentsAttributeName, "foo\nbar\nbaz"),
				),
			},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: testAccFileUpdateConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckFileExists(resourceName, &before),
					resource.TestCheckResourceAttr(resourceName, repositoryNameAttributeName, testRepoName),
					resource.TestCheckResourceAttr(resourceName, repositoryOwnerAttributeName, testRepoOwner),
					resource.TestCheckResourceAttr(resourceName, branchAttributeName, testBranchName),
					resource.TestCheckResourceAttr(resourceName, pathAttributeName, "foo/bar/baz/README.md"),
					resource.TestCheckResourceAttr(resourceName, contentsAttributeName, "foo\nbar\nqux"),
				),
			},
		},
	})
}

func testAccCheckFileExists(resourceName string, f *file) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		r, ok := state.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("not found: %q", resourceName)
		}
		ro, rn, b, p, err := parseFileID(r.Primary.ID)
		if err != nil {
			return err
		}
		h, err := ghfileutils.GetFile(context.Background(), testAccProvider.Meta().(*providerConfiguration).githubClient, ro, rn, b, p)
		if err != nil {
			return err
		}
		c, err := h.GetContent()
		if err != nil {
			return err
		}
		f.repositoryOwner = ro
		f.repositoryName = rn
		f.branch = b
		f.path = p
		f.contents = c
		return nil
	}
}

func testAccCheckFileDestroy(s *terraform.State) error {
	for _, r := range s.RootModule().Resources {
		if r.Type != resourceFileName {
			continue
		}
		ro, rn, b, p, err := parseFileID(r.Primary.ID)
		if err != nil {
			return err
		}
		_, err = ghfileutils.GetFile(context.Background(), testAccProvider.Meta().(*providerConfiguration).githubClient, ro, rn, b, p)
		if err == nil {
			return fmt.Errorf(`%q still exists in branch %q of repository "%s/%s"`, p, b, ro, rn)
		}
		if err != ghfileutils.ErrNotFound {
			return err
		}
	}
	return nil
}

// newTestResourceData creates a schema.ResourceData with the file resource schema
// populated from the given raw values, suitable for unit testing.
func newTestResourceData(t *testing.T, values map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, resourceFile().Schema, values)
}

// newMockGitHubClient creates a github.Client pointing at the given httptest.Server.
func newMockGitHubClient(server *httptest.Server) *github.Client {
	client := github.NewClient(server.Client())
	serverURL, _ := url.Parse(server.URL + "/")
	client.BaseURL = serverURL
	return client
}

func TestResourceFileDelete_ArchivedRepo(t *testing.T) {
	// Set up a mock HTTP server that returns an archived repository
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test-owner/test-repo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":1,"name":"test-repo","full_name":"test-owner/test-repo","archived":true}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	config := &providerConfiguration{
		githubClient: newMockGitHubClient(server),
	}

	d := newTestResourceData(t, map[string]interface{}{
		repositoryOwnerAttributeName: "test-owner",
		repositoryNameAttributeName:  "test-repo",
		branchAttributeName:          "main",
		pathAttributeName:            "some/file.txt",
		contentsAttributeName:        "some content",
	})
	d.SetId("test-owner/test-repo:main:some/file.txt")

	// Delete should succeed without error — archived repo skips deletion
	err := resourceFileDelete(d, config)
	if err != nil {
		t.Fatalf("expected no error for archived repo deletion, got: %v", err)
	}
}

func TestResourceFileDelete_NonArchivedRepo_FileNotFound(t *testing.T) {
	// Set up a mock HTTP server that returns a non-archived repository
	// and a 404 for the file content (file doesn't exist)
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test-owner/test-repo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":1,"name":"test-repo","full_name":"test-owner/test-repo","archived":false}`)
	})
	mux.HandleFunc("/repos/test-owner/test-repo/contents/some/file.txt", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	config := &providerConfiguration{
		githubClient: newMockGitHubClient(server),
	}

	d := newTestResourceData(t, map[string]interface{}{
		repositoryOwnerAttributeName: "test-owner",
		repositoryNameAttributeName:  "test-repo",
		branchAttributeName:          "main",
		pathAttributeName:            "some/file.txt",
		contentsAttributeName:        "some content",
	})
	d.SetId("test-owner/test-repo:main:some/file.txt")

	// Delete should succeed — file not found is treated as already deleted
	err := resourceFileDelete(d, config)
	if err != nil {
		t.Fatalf("expected no error when file not found on non-archived repo, got: %v", err)
	}
}
