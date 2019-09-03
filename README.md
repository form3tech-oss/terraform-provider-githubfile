# terraform-provider-github-file

A Terraform provider for managing files in GitHub repositories.

## Installation

Download the relevant binary from [releases](https://github.com/form3tech-oss/terraform-provider-github-file/releases) and copy it to `$HOME/.terraform.d/plugins/`.

## Configuration

The following provider block variables are available for configuration:

- `github_email`: The email address to use for commit messages. If a GPG key is provided, this must match the one which the key corresponds to.
- `github_token`: A GitHub authorisation token with `repo` permissions and having `admin` access to the target repositories.
- `github_username`: The username to use for commit messages.
- `gpg_passphrase` The passphrase associated with the provided `gpg_secret_key` (see below).
- `gpg_secret_key` The GPG secret key to be use for commit signing.

Alternatively, these values can be read from environment variables.

## Resources

### `githubfile_file`

The `githubfile_file` resource represents a file in a given branch of a GitHub repository:

```hcl
resource "githubfile_file" "form3tech_oss_terraform_provider_github_file_test_readme_md" {
    repository_owner            = "form3tech-oss"
    repository_name             = "terraform-provider-github-file-test"
    branch                      = ""
    path                        = "README.md"
    contents                    = <<EOF
# terraform-provider-github-file-test
Test repository for 'form3tech-oss/terraform-provider-github-file'.
EOF
}
```

Creating the resource above will result in the `README.md` file being created on the _default branch_ of the `form3tech-oss/terraform-provider-github-file-test` repository with the following contents:

```markdown
# terraform-provider-github-file-test
Test repository for 'form3tech-oss/terraform-provider-github-file'.
```
