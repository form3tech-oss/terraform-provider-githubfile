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
	"fmt"
	"strings"

	"github.com/google/go-github/v42/github"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	"golang.org/x/oauth2"
)

const (
	resourceFileName = "githubfile_file"
)

const (
	commitMessagePrefixKey = "commit_message_prefix"
	githubEmailKey         = "github_email"
	githubTokenKey         = "github_token"
	githubUsernameKey      = "github_username"
	gpgPassphraseKey       = "gpg_passphrase"
	gpgSecretKeyKey        = "gpg_secret_key"
)

type providerConfiguration struct {
	commitMessagePrefix string
	githubClient        *github.Client
	githubEmail         string
	githubUsername      string
	gpgPassphrase       string
	gpgSecretKey        string
}

func Provider() *schema.Provider {
	return &schema.Provider{
		ConfigureFunc: func(d *schema.ResourceData) (interface{}, error) {
			ts := oauth2.StaticTokenSource(
				&oauth2.Token{
					AccessToken: d.Get(githubTokenKey).(string),
				},
			)
			tc := oauth2.NewClient(context.Background(), ts)
			// Support reading a base64-encoded GPG secret key.
			sk := d.Get(gpgSecretKeyKey).(string)
			if v, err := base64.StdEncoding.DecodeString(sk); err == nil {
				sk = string(v)
			}
			return &providerConfiguration{
				commitMessagePrefix: d.Get(commitMessagePrefixKey).(string),
				githubClient:        github.NewClient(tc),
				githubEmail:         d.Get(githubEmailKey).(string),
				githubUsername:      d.Get(githubUsernameKey).(string),
				gpgSecretKey:        sk,
				gpgPassphrase:       d.Get(gpgPassphraseKey).(string),
			}, nil
		},
		ResourcesMap: map[string]*schema.Resource{
			resourceFileName: resourceFile(),
		},
		Schema: map[string]*schema.Schema{
			commitMessagePrefixKey: {
				Type:        schema.TypeString,
				DefaultFunc: defaultFuncForKey(commitMessagePrefixKey),
				Optional:    true,
				Sensitive:   false,
				Description: "An optional prefix to be added to all commits created as a result of manipulating files.",
			},
			githubEmailKey: {
				Type:        schema.TypeString,
				DefaultFunc: defaultFuncForKey(githubEmailKey),
				Required:    true,
				Sensitive:   true,
				Description: "The email address to use for commit messages. If a GPG key is provided, this must match the one which the key corresponds to.",
			},
			githubTokenKey: {
				Type:        schema.TypeString,
				DefaultFunc: defaultFuncForKey(githubTokenKey),
				Required:    true,
				Sensitive:   true,
				Description: "A GitHub authorisation token with permissions to manage CRUD files in the target repositories.",
			},
			githubUsernameKey: {
				Type:        schema.TypeString,
				DefaultFunc: defaultFuncForKey(githubUsernameKey),
				Required:    true,
				Sensitive:   true,
				Description: "The username to use for commit messages.",
			},
			gpgPassphraseKey: {
				Type:        schema.TypeString,
				DefaultFunc: defaultFuncForKey(gpgPassphraseKey),
				Optional:    true,
				Sensitive:   true,
				Description: fmt.Sprintf("The passphrase associated with the provided %q.", gpgSecretKeyKey),
			},
			gpgSecretKeyKey: {
				Type:        schema.TypeString,
				DefaultFunc: defaultFuncForKey(gpgSecretKeyKey),
				Optional:    true,
				Sensitive:   true,
				Description: "The GPG secret key to be use for commit signing.",
			},
		},
	}
}

func defaultFuncForKey(v string) schema.SchemaDefaultFunc {
	return schema.EnvDefaultFunc(strings.ToUpper(v), "")
}
