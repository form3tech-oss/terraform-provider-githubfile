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
	"strings"
	"time"

	"github.com/form3tech-oss/go-github-utils/pkg/branch"
	"github.com/form3tech-oss/go-github-utils/pkg/commit"
	ghfileutils "github.com/form3tech-oss/go-github-utils/pkg/file"
	"github.com/google/go-github/v54/github"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
)

const (
	branchAttributeName          = "branch"
	contentsAttributeName        = "contents"
	pathAttributeName            = "path"
	repositoryNameAttributeName  = "repository_name"
	repositoryOwnerAttributeName = "repository_owner"
)

func resourceFile() *schema.Resource {
	return &schema.Resource{
		Create: resourceFileCreate,
		Read:   resourceFileRead,
		Update: resourceFileUpdate,
		Delete: resourceFileDelete,
		Importer: &schema.ResourceImporter{
			State: resourceFileImport,
		},
		Schema: map[string]*schema.Schema{
			repositoryNameAttributeName: {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The name of the repository in which to create the file.",
			},
			repositoryOwnerAttributeName: {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The owner of the repository in which to create the file.",
			},
			branchAttributeName: {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The branch in which to create the file.",
			},
			pathAttributeName: {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The path in which to create the file.",
			},
			contentsAttributeName: {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The contents of the file.",
			},
		},
		SchemaVersion: 0,
	}
}

func resourceFileCreate(d *schema.ResourceData, m interface{}) error {
	return resourceFileCreateOrUpdate("Create %q.", d, m)
}

func resourceFileCreateOrUpdate(s string, d *schema.ResourceData, m interface{}) error {
	c := m.(*providerConfiguration)
	f := expandFile(d)

	// Create a commit having the target file's new/updated contents as the single change.
	entries := []*github.TreeEntry{
		{
			Content: github.String(f.contents),
			Mode:    github.String("100644"),
			Path:    github.String(f.path),
			Type:    github.String("blob"),
		},
	}
	if err := commit.CreateCommit(context.Background(), c.githubClient, &commit.CommitOptions{
		RepoOwner:                   f.repositoryOwner,
		RepoName:                    f.repositoryName,
		CommitMessage:               formatCommitMessage(c.commitMessagePrefix, s, f.path),
		GpgPassphrase:               c.gpgPassphrase,
		GpgPrivateKey:               c.gpgSecretKey,
		Username:                    c.githubUsername,
		Email:                       c.githubEmail,
		Changes:                     entries,
		PullRequestSourceBranchName: fmt.Sprintf("terraform-provider-githubfile-%d", time.Now().UnixNano()),
		PullRequestBody:             "",
		MaxRetries:                  3,
		RetryBackoff:                5 * time.Second,
	}); err != nil {
		return fmt.Errorf("failed to create commit: %v", err)
	}
	return resourceFileRead(d, m)
}

func resourceFileDelete(d *schema.ResourceData, m interface{}) error {
	c := m.(*providerConfiguration)
	f := expandFile(d)

	// Check whether the file exists.
	_, err := ghfileutils.GetFile(context.Background(), c.githubClient, f.repositoryOwner, f.repositoryName, f.branch, f.path)
	if err != nil {
		if err == ghfileutils.ErrNotFound {
			return nil
		}
		return err
	}

	// Get the tree that corresponds to the target branch.
	s, err := branch.GetSHAForBranch(context.Background(), c.githubClient, f.repositoryOwner, f.repositoryName, f.branch)
	if err != nil {
		return err
	}
	oldTree, _, err := c.githubClient.Git.GetTree(context.Background(), f.repositoryOwner, f.repositoryName, s, true)
	if err != nil {
		return err
	}

	// Remove the target file from the list of entries for the new tree.
	// NOTE: Entries of type "tree" must be removed as well, otherwise deletion won't take place.
	newTree := make([]*github.TreeEntry, 0, len(oldTree.Entries))
	for _, entry := range oldTree.Entries {
		if *entry.Type != "tree" && *entry.Path != f.path {
			newTree = append(newTree, entry)
		}
	}

	// Create a commit based on the new tree.
	if err := commit.CreateCommit(context.Background(), c.githubClient, &commit.CommitOptions{
		RepoOwner:                   f.repositoryOwner,
		RepoName:                    f.repositoryName,
		CommitMessage:               formatCommitMessage(c.commitMessagePrefix, "Delete %q.", f.path),
		GpgPassphrase:               c.gpgPassphrase,
		GpgPrivateKey:               c.gpgSecretKey,
		Username:                    c.githubUsername,
		Email:                       c.githubEmail,
		Changes:                     newTree,
		BaseTreeOverride:            github.String(""),
		PullRequestSourceBranchName: fmt.Sprintf("terraform-provider-githubfile-%d", time.Now().UnixNano()),
		PullRequestBody:             "",
		MaxRetries:                  3,
		RetryBackoff:                5 * time.Second,
	}); err != nil {
		return fmt.Errorf("failed to create commit: %v", err)
	}
	return nil
}

func resourceFileImport(d *schema.ResourceData, m interface{}) ([]*schema.ResourceData, error) {
	if err := resourceFileRead(d, m); err != nil {
		return []*schema.ResourceData{}, err
	}
	return []*schema.ResourceData{d}, nil
}

func resourceFileRead(d *schema.ResourceData, m interface{}) error {
	c := m.(*providerConfiguration)
	f := expandFile(d)

	h, err := ghfileutils.GetFile(context.Background(), c.githubClient, f.repositoryOwner, f.repositoryName, f.branch, f.path)
	if err == ghfileutils.ErrNotFound {
		d.SetId("")
		return nil
	}
	if err != nil {
		return err
	}
	r, err := h.GetContent()
	if err != nil {
		return err
	}
	f.contents = r

	return flattenFile(f, d)
}

func resourceFileUpdate(d *schema.ResourceData, m interface{}) error {
	if err := resourceFileCreateOrUpdate("Update %q.", d, m); err != nil {
		d.SetId("")
		return err
	}
	return nil
}

func formatCommitMessage(p, m string, args ...interface{}) string {
	if p == "" {
		return fmt.Sprintf(m, args...)
	}
	return fmt.Sprintf(strings.TrimSpace(p)+" "+m, args...)
}
