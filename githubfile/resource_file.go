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
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/form3tech-oss/go-github-utils/pkg/branch"
	"github.com/form3tech-oss/go-github-utils/pkg/commit"
	ghfileutils "github.com/form3tech-oss/go-github-utils/pkg/file"
	"github.com/google/go-github/v54/github"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &fileResource{}
	_ resource.ResourceWithConfigure   = &fileResource{}
	_ resource.ResourceWithImportState = &fileResource{}
)

type fileResource struct {
	config *providerConfiguration
}

type fileResourceModel struct {
	ID              types.String `tfsdk:"id"`
	RepositoryOwner types.String `tfsdk:"repository_owner"`
	RepositoryName  types.String `tfsdk:"repository_name"`
	Branch          types.String `tfsdk:"branch"`
	Path            types.String `tfsdk:"path"`
	Contents        types.String `tfsdk:"contents"`
}

// NewFileResource returns a new file resource.
func NewFileResource() resource.Resource {
	return &fileResource{}
}

func (r *fileResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_file"
}

func (r *fileResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "The ID of the file resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"repository_owner": schema.StringAttribute{
				Required:    true,
				Description: "The owner of the repository in which to create the file.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"repository_name": schema.StringAttribute{
				Required:    true,
				Description: "The name of the repository in which to create the file.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"branch": schema.StringAttribute{
				Required:    true,
				Description: "The branch in which to create the file.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"path": schema.StringAttribute{
				Required:    true,
				Description: "The path in which to create the file.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"contents": schema.StringAttribute{
				Required:    true,
				Description: "The contents of the file.",
			},
		},
	}
}

func (r *fileResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	config, ok := req.ProviderData.(*providerConfiguration)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Provider Data",
			fmt.Sprintf("Expected *providerConfiguration, got: %T", req.ProviderData),
		)
		return
	}
	r.config = config
}

func (r *fileResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan fileResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	f := modelToFile(&plan)
	if err := createOrUpdateFile(ctx, r.config, f, "Create %q."); err != nil {
		resp.Diagnostics.AddError("Failed to create file", err.Error())
		return
	}

	if err := readFile(ctx, r.config, f); err != nil {
		resp.Diagnostics.AddError("Failed to read file after create", err.Error())
		return
	}

	fileToModel(f, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *fileResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state fileResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	f := modelToFile(&state)
	if err := readFile(ctx, r.config, f); err != nil {
		if errors.Is(err, errFileNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read file", err.Error())
		return
	}

	fileToModel(f, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *fileResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan fileResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	f := modelToFile(&plan)
	if err := createOrUpdateFile(ctx, r.config, f, "Update %q."); err != nil {
		resp.Diagnostics.AddError("Failed to update file", err.Error())
		return
	}

	if err := readFile(ctx, r.config, f); err != nil {
		resp.Diagnostics.AddError("Failed to read file after update", err.Error())
		return
	}

	fileToModel(f, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *fileResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state fileResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	f := modelToFile(&state)
	if err := deleteFile(ctx, r.config, f); err != nil {
		resp.Diagnostics.AddError("Failed to delete file", err.Error())
		return
	}
}

func (r *fileResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ro, rn, b, p, err := parseFileID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}

	f := &file{
		repositoryOwner: ro,
		repositoryName:  rn,
		branch:          b,
		path:            p,
	}

	if err := readFile(ctx, r.config, f); err != nil {
		resp.Diagnostics.AddError("Failed to read file during import", err.Error())
		return
	}

	var model fileResourceModel
	fileToModel(f, &model)
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

// --- Business logic functions (testable independently) ---

var errFileNotFound = errors.New("file not found")

func createOrUpdateFile(ctx context.Context, c *providerConfiguration, f *file, s string) error {
	entries := []*github.TreeEntry{
		{
			Content: github.String(f.contents),
			Mode:    github.String("100644"),
			Path:    github.String(f.path),
			Type:    github.String("blob"),
		},
	}
	if err := commit.CreateCommit(ctx, c.githubClient, &commit.CommitOptions{
		RepoOwner:                   f.repositoryOwner,
		RepoName:                    f.repositoryName,
		Branch:                      f.branch,
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
	return nil
}

func readFile(ctx context.Context, c *providerConfiguration, f *file) error {
	h, err := ghfileutils.GetFile(ctx,
		c.githubClient,
		f.repositoryOwner,
		f.repositoryName,
		f.branch,
		f.path)
	if err == ghfileutils.ErrNotFound {
		return errFileNotFound
	}
	if err != nil {
		return err
	}
	r, err := h.GetContent()
	if err != nil {
		return err
	}
	f.contents = r
	return nil
}

func deleteFile(ctx context.Context, c *providerConfiguration, f *file) error {
	// Check if the repository is archived. If so, skip the delete operation
	// and just remove the resource from state, since archived repositories
	// cannot be modified.
	repo, _, err := c.githubClient.Repositories.Get(ctx, f.repositoryOwner, f.repositoryName)
	if err != nil {
		return fmt.Errorf("failed to retrieve repository %s/%s: %v", f.repositoryOwner, f.repositoryName, err)
	}
	if repo.GetArchived() {
		log.Printf("[WARN] Repository %s/%s is archived, skipping file deletion and removing %q from state", f.repositoryOwner, f.repositoryName, f.path)
		return nil
	}

	// Check whether the file exists.
	fileContent, err := ghfileutils.GetFile(ctx,
		c.githubClient,
		f.repositoryOwner,
		f.repositoryName,
		f.branch,
		f.path,
	)
	if err != nil {
		if err == ghfileutils.ErrNotFound {
			return nil
		}
		return err
	}

	// Get the tree that corresponds to the target branch.
	s, err := branch.GetSHAForBranch(ctx,
		c.githubClient,
		f.repositoryOwner,
		f.repositoryName,
		f.branch)
	if err != nil {
		return err
	}

	newTree := []*github.TreeEntry{{
		SHA:  nil, // delete the file
		Path: fileContent.Path,
		Mode: github.String("100644"),
		Type: github.String("blob"),
	}}
	// Create a commit based on the new tree.
	if err := commit.CreateCommit(ctx, c.githubClient, &commit.CommitOptions{
		RepoOwner:                   f.repositoryOwner,
		RepoName:                    f.repositoryName,
		Branch:                      f.branch,
		CommitMessage:               formatCommitMessage(c.commitMessagePrefix, "Delete %q.", f.path),
		GpgPassphrase:               c.gpgPassphrase,
		GpgPrivateKey:               c.gpgSecretKey,
		Username:                    c.githubUsername,
		Email:                       c.githubEmail,
		Changes:                     newTree,
		BaseTreeOverride:            &s,
		PullRequestSourceBranchName: fmt.Sprintf("terraform-provider-githubfile-%d", time.Now().UnixNano()),
		PullRequestBody:             "",
		MaxRetries:                  3,
		RetryBackoff:                5 * time.Second,
	},
	); err != nil {
		return fmt.Errorf("failed to create commit: %v", err)
	}
	return nil
}

// --- Helper functions ---

func modelToFile(m *fileResourceModel) *file {
	return &file{
		repositoryOwner: m.RepositoryOwner.ValueString(),
		repositoryName:  m.RepositoryName.ValueString(),
		branch:          m.Branch.ValueString(),
		path:            m.Path.ValueString(),
		contents:        m.Contents.ValueString(),
	}
}

func fileToModel(f *file, m *fileResourceModel) {
	m.ID = types.StringValue(fmt.Sprintf("%s/%s:%s:%s", f.repositoryOwner, f.repositoryName, f.branch, f.path))
	m.RepositoryOwner = types.StringValue(f.repositoryOwner)
	m.RepositoryName = types.StringValue(f.repositoryName)
	m.Branch = types.StringValue(f.branch)
	m.Path = types.StringValue(f.path)
	m.Contents = types.StringValue(f.contents)
}

func formatCommitMessage(p, m string, args ...interface{}) string {
	if p == "" {
		return fmt.Sprintf(m, args...)
	}
	return fmt.Sprintf(strings.TrimSpace(p)+" "+m, args...)
}
