# terraform-provider-githubfile

[![Build status](https://github.com/form3tech-oss/terraform-provider-githubfile/actions/workflows/ci.yaml/badge.svg?branch=master)](https://github.com/form3tech-oss/terraform-provider-githubfile/actions)
[![License](https://img.shields.io/github/license/form3tech-oss/terraform-provider-githubfile)](LICENSE)

A Terraform provider for managing files in GitHub repositories.

## Use Cases

A few possible use cases for `terraform-provider-githubfile` are:

* Adding a `LICENSE` file to a number of repositories.
* Making sure repositories across an organisation have consistent issue/pull request templates.
* Configuring a tool such as [`golangci-lint`](https://github.com/golangci/golangci-lint) or [`pre-commit`](https://pre-commit.com/) uniformly across a number of repositories.

## Installation

### Manual Installation

Download the relevant binary from [releases](https://github.com/form3tech-oss/terraform-provider-githubfile/releases) and copy it to `$HOME/.terraform.d/plugins/`.

## Configuration

The following provider block variables are available for configuration:

| Name | Required | Environment Variable | Description |
| ---- | :------: | -------------------- | ----------- |
| `github_token` | **Yes** | `GITHUB_TOKEN` | A GitHub authorisation token with permissions to manage files in the target repositories. |
| `github_email` | **Yes** | `GITHUB_EMAIL` | The email address to use for commit messages. If a GPG key is provided, this must match the one which the key corresponds to. |
| `github_username` | **Yes** | `GITHUB_USERNAME` | The username to use for commit messages. |
| `commit_message_prefix` | No | `COMMIT_MESSAGE_PREFIX` | An optional prefix to be added to all commits generated as a result of manipulating files. |
| `gpg_secret_key` | No | `GPG_SECRET_KEY` | The GPG secret key to use for commit signing. Accepts raw or base64-encoded values. If left empty, commits will not be signed. |
| `gpg_passphrase` | No | `GPG_PASSPHRASE` | The passphrase associated with the provided `gpg_secret_key`. |

Each variable can be set either in the provider block or via the corresponding environment variable. Provider block values take precedence over environment variables.

### Example

```hcl
provider "githubfile" {
  github_token            = var.github_token
  github_email            = "ci-bot@example.com"
  github_username         = "ci-bot"
  commit_message_prefix   = "[terraform]"
}
```

## Resources

### `githubfile_file`

The `githubfile_file` resource represents a file in a given branch of a GitHub repository.

#### Attributes

| Name | Type | Required | Description |
| ---- | :--: | :------: | ----------- |
| `id` | String | Computed | The ID of the file resource (format: `owner/repo:branch:path`). |
| `repository_owner` | String | **Yes** | The owner of the repository. Changing this forces a new resource. |
| `repository_name` | String | **Yes** | The name of the repository. Changing this forces a new resource. |
| `branch` | String | **Yes** | The branch in which to create/update the file. Changing this forces a new resource. |
| `path` | String | **Yes** | The path to the file being created/updated. Changing this forces a new resource. |
| `contents` | String | **Yes** | The contents of the file. |

> **Note:** When a managed file is in an archived repository, the provider will gracefully skip deletion and simply remove the resource from state.

#### Example

```hcl
resource "githubfile_file" "issue_template" {
  repository_owner = "form3tech-oss"
  repository_name  = "terraform-provider-githubfile"
  branch           = "main"
  path             = ".github/ISSUE_TEMPLATE.md"
  contents         = <<-EOF
    # Issue Type

    - [ ] Bug report.
    - [ ] Suggestion.

    # Description

    <!-- Please provide a description of the issue. -->
  EOF
}
```

Creating the resource above will result in the `.github/ISSUE_TEMPLATE.md` file being created/updated on the `main` branch of the `form3tech-oss/terraform-provider-githubfile` repository.

#### Import

Existing files can be imported into Terraform state using the following ID format:

```
owner/repo:branch:path
```

For example:

```bash
terraform import githubfile_file.issue_template form3tech-oss/terraform-provider-githubfile:main:.github/ISSUE_TEMPLATE.md
```

## Development

### Requirements

* [Go](https://golang.org/dl/) (see `go.mod` for the required version)
* A GitHub token with `repo` permissions (set as `GITHUB_TOKEN`)

### Building

```bash
make build
```

### Running Tests

Acceptance tests require a valid GitHub token and run against the `form3tech-oss/terraform-provider-githubfile` repository:

```bash
export GITHUB_TOKEN="your-token"
export GITHUB_EMAIL="your-email@example.com"
export GITHUB_USERNAME="your-username"
make test
```

## License

This project is licensed under the [Apache License 2.0](LICENSE).
