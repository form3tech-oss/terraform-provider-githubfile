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
	"testing"

	ghfileutils "github.com/form3tech-oss/go-github-utils/pkg/file"
	"github.com/hashicorp/terraform/helper/resource"
	"github.com/hashicorp/terraform/terraform"
)

const (
	testAccFileCreate = `
resource "githubfile_file" "foo" {
    repository_owner = "form3tech-oss"
    repository_name  = "terraform-provider-githubfile-test"
	branch           = "master"
	path             = "foo/bar/baz/README.md"
	contents         = "foo\nbar\nbaz"
}
`
	testAccFileUpdate = `
resource "githubfile_file" "foo" {
    repository_owner = "form3tech-oss"
    repository_name  = "terraform-provider-githubfile-test"
	branch           = "master"
	path             = "foo/bar/baz/README.md"
	contents         = "foo\nbar\nqux"
}
`
	testAccFileUpdatueOldBranch= `
resource "githubfile_file" "bar" {
    repository_owner = "form3tech-oss"
    repository_name  = "terraform-provider-githubfile-test"
	branch           = "master"
	path             = "foo/bar/test/README.md"
	contents         = "foo\nbar\nqux"
}
`
	testAccFileUpdatueNewBranch = `
resource "githubfile_file" "bar" {
    repository_owner = "form3tech-oss"
    repository_name  = "terraform-provider-githubfile-test"
	branch           = "main"
	path             = "foo/bar/test/README.md"
	contents         = "foo\nbar\baz"
}
`
)

func TestAccResourceFile_basic(t *testing.T) {
	var (
		before file
	)

	resourceName := "githubfile_file.foo"

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
		},
		IDRefreshName: resourceName,
		Providers:     testAccProviders,
		CheckDestroy:  testAccCheckFileDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccFileCreate,
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckFileExists(resourceName, &before),
					resource.TestCheckResourceAttr(resourceName, repositoryNameAttributeName, "terraform-provider-githubfile-test"),
					resource.TestCheckResourceAttr(resourceName, repositoryOwnerAttributeName, "form3tech-oss"),
					resource.TestCheckResourceAttr(resourceName, branchAttributeName, "master"),
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
				Config: testAccFileUpdate,
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckFileExists(resourceName, &before),
					resource.TestCheckResourceAttr(resourceName, repositoryNameAttributeName, "terraform-provider-githubfile-test"),
					resource.TestCheckResourceAttr(resourceName, repositoryOwnerAttributeName, "form3tech-oss"),
					resource.TestCheckResourceAttr(resourceName, branchAttributeName, "master"),
					resource.TestCheckResourceAttr(resourceName, pathAttributeName, "foo/bar/baz/README.md"),
					resource.TestCheckResourceAttr(resourceName, contentsAttributeName, "foo\nbar\nqux"),
				),
			},
		},
	})
}

func TestAccResourceFile_update_cross_branch(t *testing.T) {
	var (
		before file
	)

	resourceName := "githubfile_file.bar"

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
		},
		IDRefreshName: resourceName,
		Providers:     testAccProviders,
		CheckDestroy:  testAccCheckFileDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccFileUpdatueOldBranch,
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckFileExists(resourceName, &before),
					resource.TestCheckResourceAttr(resourceName, repositoryNameAttributeName, "terraform-provider-githubfile-test"),
					resource.TestCheckResourceAttr(resourceName, repositoryOwnerAttributeName, "form3tech-oss"),
					resource.TestCheckResourceAttr(resourceName, branchAttributeName, "master"),
					resource.TestCheckResourceAttr(resourceName, pathAttributeName, "foo/bar/test/README.md"),
					resource.TestCheckResourceAttr(resourceName, contentsAttributeName, "foo\nbar\nqux"),
				),
			},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: testAccFileUpdatueNewBranch,
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckFileExists(resourceName, &before),
					resource.TestCheckResourceAttr(resourceName, repositoryNameAttributeName, "terraform-provider-githubfile-test"),
					resource.TestCheckResourceAttr(resourceName, repositoryOwnerAttributeName, "form3tech-oss"),
					resource.TestCheckResourceAttr(resourceName, branchAttributeName, "main"),
					resource.TestCheckResourceAttr(resourceName, pathAttributeName, "foo/bar/test/README.md"),
					resource.TestCheckResourceAttr(resourceName, contentsAttributeName, "foo\nbar\nbaz"),
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
