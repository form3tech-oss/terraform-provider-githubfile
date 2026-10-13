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
	"encoding/base64"
	"os"
	"strconv"
	"time"

	"github.com/google/go-github/v54/github"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"golang.org/x/oauth2"
)

var _ provider.Provider = &githubfileProvider{}

type providerConfiguration struct {
	branchLocks         *branchLocks
	commitMessagePrefix string
	directCommit        bool
	githubClient        *github.Client
	githubEmail         string
	githubUsername      string
	gpgPassphrase       string
	gpgSecretKey        string
	retry               retryConfig
	writeSlots          semaphore
}

type githubfileProvider struct{}

type githubfileProviderModel struct {
	CommitMessagePrefix types.String `tfsdk:"commit_message_prefix"`
	DirectCommit        types.Bool   `tfsdk:"direct_commit"`
	GithubEmail         types.String `tfsdk:"github_email"`
	GithubToken         types.String `tfsdk:"github_token"`
	GithubUsername      types.String `tfsdk:"github_username"`
	GpgPassphrase       types.String `tfsdk:"gpg_passphrase"`
	GpgSecretKey        types.String `tfsdk:"gpg_secret_key"`
	MaxConcurrency      types.Int64  `tfsdk:"max_concurrency"`
	MaxRetries          types.Int64  `tfsdk:"max_retries"`
	RetryWaitMaxSeconds types.Int64  `tfsdk:"retry_wait_max_seconds"`
	RetryWaitMinSeconds types.Int64  `tfsdk:"retry_wait_min_seconds"`
}

// New returns a new instance of the githubfile provider.
func New() provider.Provider {
	return &githubfileProvider{}
}

func (p *githubfileProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "githubfile"
}

func (p *githubfileProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"commit_message_prefix": schema.StringAttribute{
				Optional:    true,
				Description: "An optional prefix to be added to all commits created as a result of manipulating files. Can also be set via the COMMIT_MESSAGE_PREFIX environment variable.",
			},
			"direct_commit": schema.BoolAttribute{
				Optional:    true,
				Description: "Write files by committing straight to the target branch, rather than by opening a pull request and merging it. Four API calls per file instead of eight, and a collision with another writer is retried by rebuilding on the new branch tip rather than failing. Requires an identity that can push to the target branch, which on a protected branch means one that bypasses protection for direct pushes and not only for pull requests. Defaults to false. Can also be set via the DIRECT_COMMIT environment variable.",
			},
			"github_email": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "The email address to use for commit messages. If a GPG key is provided, this must match the one which the key corresponds to. Can also be set via the GITHUB_EMAIL environment variable.",
			},
			"github_token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "A GitHub authorisation token with permissions to manage CRUD files in the target repositories. Can also be set via the GITHUB_TOKEN environment variable.",
			},
			"github_username": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "The username to use for commit messages. Can also be set via the GITHUB_USERNAME environment variable.",
			},
			"gpg_passphrase": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "The passphrase associated with the provided \"gpg_secret_key\". Can also be set via the GPG_PASSPHRASE environment variable.",
			},
			"gpg_secret_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "The GPG secret key to be use for commit signing. Can also be set via the GPG_SECRET_KEY environment variable.",
			},
			"max_concurrency": schema.Int64Attribute{
				Optional:    true,
				Description: "How many file writes this provider may have in flight at once. A write is four API calls on the direct route and eight on the pull request route, so Terraform's default parallelism of ten can leave eighty requests outstanding against an hourly allowance shared with every other workspace. Unlike Terraform's own -parallelism this caps only this provider, leaving the rest of the graph alone. Reads are not counted. Defaults to 0, meaning no limit. Can also be set via the MAX_CONCURRENCY environment variable.",
			},
			"max_retries": schema.Int64Attribute{
				Optional:    true,
				Description: "How many times to retry a request GitHub has asked us to come back for - a rate limit, a 429, or a server error. Defaults to 5. Can also be set via the MAX_RETRIES environment variable.",
			},
			"retry_wait_min_seconds": schema.Int64Attribute{
				Optional:    true,
				Description: "Shortest wait between retries, in seconds. Defaults to 1. Can also be set via the RETRY_WAIT_MIN_SECONDS environment variable.",
			},
			"retry_wait_max_seconds": schema.Int64Attribute{
				Optional:    true,
				Description: "Longest wait between retries, in seconds. Defaults to 60. This is the ceiling on waiting out a rate limit: a primary limit can reset up to an hour away, and waiting that out inside an apply may outlast the run itself, so past this the request is failed and reported rather than slept on. Raise it to trade a longer apply for fewer failed ones. Can also be set via the RETRY_WAIT_MAX_SECONDS environment variable.",
			},
		},
	}
}

func (p *githubfileProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config githubfileProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	token := stringValueOrEnv(config.GithubToken, "GITHUB_TOKEN")
	if token == "" {
		resp.Diagnostics.AddError(
			"Missing GitHub Token",
			"github_token must be configured or the GITHUB_TOKEN environment variable must be set.",
		)
		return
	}

	email := stringValueOrEnv(config.GithubEmail, "GITHUB_EMAIL")
	if email == "" {
		resp.Diagnostics.AddError(
			"Missing GitHub Email",
			"github_email must be configured or the GITHUB_EMAIL environment variable must be set.",
		)
		return
	}

	username := stringValueOrEnv(config.GithubUsername, "GITHUB_USERNAME")
	if username == "" {
		resp.Diagnostics.AddError(
			"Missing GitHub Username",
			"github_username must be configured or the GITHUB_USERNAME environment variable must be set.",
		)
		return
	}

	retry := retryConfig{
		maxRetries:   int(int64ValueOrEnv(config.MaxRetries, "MAX_RETRIES", defaultMaxRetries)),
		retryWaitMin: time.Duration(int64ValueOrEnv(config.RetryWaitMinSeconds, "RETRY_WAIT_MIN_SECONDS", int64(defaultRetryWaitMin/time.Second))) * time.Second,
		retryWaitMax: time.Duration(int64ValueOrEnv(config.RetryWaitMaxSeconds, "RETRY_WAIT_MAX_SECONDS", int64(defaultRetryWaitMax/time.Second))) * time.Second,
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	gc := github.NewClient(newRetryableClient(&oauth2.Transport{Source: ts}, retry))

	sk := stringValueOrEnv(config.GpgSecretKey, "GPG_SECRET_KEY")
	if v, err := base64.StdEncoding.DecodeString(sk); err == nil {
		sk = string(v)
	}

	providerConfig := &providerConfiguration{
		branchLocks:         newBranchLocks(),
		commitMessagePrefix: stringValueOrEnv(config.CommitMessagePrefix, "COMMIT_MESSAGE_PREFIX"),
		directCommit:        boolValueOrEnv(config.DirectCommit, "DIRECT_COMMIT"),
		githubClient:        gc,
		githubEmail:         email,
		githubUsername:      username,
		gpgSecretKey:        sk,
		gpgPassphrase:       stringValueOrEnv(config.GpgPassphrase, "GPG_PASSPHRASE"),
		retry:               retry,
		writeSlots:          newSemaphore(int(int64ValueOrEnv(config.MaxConcurrency, "MAX_CONCURRENCY", 0))),
	}

	resp.DataSourceData = providerConfig
	resp.ResourceData = providerConfig
}

func (p *githubfileProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewFileResource,
	}
}

func (p *githubfileProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}

func stringValueOrEnv(v types.String, envKey string) string {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueString()
	}
	return os.Getenv(envKey)
}

func int64ValueOrEnv(v types.Int64, envKey string, fallback int64) int64 {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueInt64()
	}
	n, err := strconv.ParseInt(os.Getenv(envKey), 10, 64)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

func boolValueOrEnv(v types.Bool, envKey string) bool {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueBool()
	}
	b, err := strconv.ParseBool(os.Getenv(envKey))
	if err != nil {
		return false
	}
	return b
}
