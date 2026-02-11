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
	commitMessagePrefix string
	githubClient        *github.Client
	githubEmail         string
	githubUsername      string
	gpgPassphrase       string
	gpgSecretKey        string
}

type githubfileProvider struct{}

type githubfileProviderModel struct {
	CommitMessagePrefix types.String `tfsdk:"commit_message_prefix"`
	GithubEmail         types.String `tfsdk:"github_email"`
	GithubToken         types.String `tfsdk:"github_token"`
	GithubUsername      types.String `tfsdk:"github_username"`
	GpgPassphrase       types.String `tfsdk:"gpg_passphrase"`
	GpgSecretKey        types.String `tfsdk:"gpg_secret_key"`
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

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	gc := github.NewClient(tc)

	sk := stringValueOrEnv(config.GpgSecretKey, "GPG_SECRET_KEY")
	if v, err := base64.StdEncoding.DecodeString(sk); err == nil {
		sk = string(v)
	}

	providerConfig := &providerConfiguration{
		commitMessagePrefix: stringValueOrEnv(config.CommitMessagePrefix, "COMMIT_MESSAGE_PREFIX"),
		githubClient:        gc,
		githubEmail:         email,
		githubUsername:      username,
		gpgSecretKey:        sk,
		gpgPassphrase:       stringValueOrEnv(config.GpgPassphrase, "GPG_PASSPHRASE"),
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
