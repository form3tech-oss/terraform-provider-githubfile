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
| `direct_commit` | No | `DIRECT_COMMIT` | Commit straight to the target branch instead of opening and merging a pull request. Defaults to `false`. See [Write routes](#write-routes). |
| `max_concurrency` | No | `MAX_CONCURRENCY` | How many file writes may be in flight at once. Defaults to `0`, meaning no limit. See [Concurrency](#concurrency). |
| `max_retries` | No | `MAX_RETRIES` | How many times to retry a request GitHub has asked us to come back for. Defaults to `5`. See [Retries](#retries). |
| `retry_wait_min_seconds` | No | `RETRY_WAIT_MIN_SECONDS` | Shortest wait between retries, in seconds. Defaults to `1`. |
| `retry_wait_max_seconds` | No | `RETRY_WAIT_MAX_SECONDS` | Longest wait between retries, in seconds. Defaults to `60`. |
| `gpg_secret_key` | No | `GPG_SECRET_KEY` | The GPG secret key to use for commit signing. Accepts raw or base64-encoded values. If left empty, commits will not be signed. |
| `gpg_passphrase` | No | `GPG_PASSPHRASE` | The passphrase associated with the provided `gpg_secret_key`. |

Each variable can be set either in the provider block or via the corresponding environment variable. Provider block values take precedence over environment variables.

### Write routes

By default a file write is performed by committing to a new branch, opening a pull
request from it and merging that pull request. Setting `direct_commit = true`
writes the commit to the target branch instead, moving the branch ref to it.

The routes differ in two ways that matter at scale.

**Cost.** The pull request route is eight API calls per file written — read ref,
create tree, read parent commit, create commit, create ref, create pull request,
merge, delete ref — and every file pays it. Direct commit is four. On a token
with a fixed hourly allowance, writing one file into several hundred
repositories is the difference between fitting inside the budget and exhausting
it.

**Behaviour under collision.** The pull request route reads the base SHA at the
start and merges at the end. If another writer lands on the branch in between,
GitHub rejects the merge with `405 Base branch was modified`, and retrying
cannot help: the commit underneath still has the parent that was read before the
branch moved. Direct commit finishes with a compare-and-swap on the ref, which
GitHub rejects with `422` if the branch has moved — and the retry re-reads the
branch, rebuilds the tree and commit on the new tip, and swaps again. It
converges rather than re-attempting something that can no longer succeed.
Retries are jittered so that writers rejected by the same push do not return in
lockstep.

When the pull request route fails, the branch it pushed its commit to is
deleted. The route itself only deletes that branch once the merge has succeeded,
so every other ending leaves one behind — the merge exhausting its retries,
which closes the pull request but not the branch, or any failure between
creating the branch and merging it, which leaves a branch with no pull request
at all. They accumulate and nothing collects them, since each name carries a
timestamp and is never reused. The direct route creates no branch and so leaves
nothing to collect.

Direct commit needs an identity that can push to the target branch. Where the
branch is protected, that means one that bypasses protection **for direct
pushes**, not only for pull requests — a repository that enforces protection on
administrators, or whose ruleset grants bypass in `pull_request` mode rather
than `always`, can accept the pull request route and reject this one. That is
why it is opt-in and why the pull request route remains the default.

Note that `commit_message_prefix` behaves differently between the two. On the
pull request route the prefix lands on a commit that is then merged, so the
merge commit at the branch tip does not carry it; CI that honours markers such
as `[skip ci]` still runs. With `direct_commit`, the prefixed commit *is* the
branch tip, so such a marker takes effect.

### Example

```hcl
provider "githubfile" {
  github_token            = var.github_token
  github_email            = "ci-bot@example.com"
  github_username         = "ci-bot"
  commit_message_prefix   = "[terraform]"
  direct_commit           = true
}
```

### Retries

Requests are retried at two levels, because GitHub's rate limits arrive by two
routes and neither layer can observe the other's.

**At the transport.** Every request either write route makes — four or eight per
file, rather than only the merge — is retried on a `429`, a `5xx`, a connection
failure, and a `403` that carries rate-limit headers. A `403` without them is
returned immediately: GitHub uses that status for authorisation failures too,
and retrying one only spends more budget.

The wait prefers whatever the response asks for. `Retry-After` is honoured when
present; otherwise, if the primary budget is spent, the wait runs to the
`X-RateLimit-Reset` timestamp; otherwise it grows exponentially from
`retry_wait_min_seconds`. Every branch is jittered, so that callers rejected by
the same exhausted token do not wake together and spend the new budget in the
same instant they spent the last one.

`retry_wait_max_seconds` caps any single wait. A primary rate limit can reset up
to an hour out, and sleeping that off inside an apply may outlast the run
itself, so beyond the cap the request fails and is reported rather than slept
on. Raise it to trade longer applies for fewer failed ones.

**Above the client.** `go-github` remembers the rate-limit headers from the last
response and, once it has seen the budget exhausted, refuses subsequent calls
locally — synthesising a `403` ("API rate limit of 5000 still exceeded ... not
making remote request") without making a request at all. That error is produced
above the transport, so no transport-level retry can observe it. It is handled
separately, by waiting for the reset the error carries.

The two overlap deliberately: absorbing a real `403` at the transport means
`go-github` never records an exhausted budget, so the local refusal mostly does
not arise. When a wait exceeds the cap, it does, and the outer layer is what
remains.

Note that only the direct route gets the second layer. Its requests are wrapped
individually, so a refused one resumes where it stopped. The pull request route
is a sequence of mutations inside a vendored dependency, where re-running from
the start after a branch and pull request already exist would leave them behind,
so it gets transport-level retries only.

### Concurrency

Terraform applies resources in parallel — ten at a time by default — and every
resource using one provider block shares the client configured for it. Two
limits are imposed on that, because the parallelism causes two distinct problems.

**Writes to the same branch are serialised.** Always, with no setting to change
it. Two concurrent writes to one branch are the collision both write routes are
built to survive, but surviving one still costs every API call that preceded it,
and on the pull request route a `405 Base branch was modified` may not be
survivable within its retries at all. Serialising removes the collision rather
than paying to recover from it, and costs nothing in the common case: writes to
different repositories never share a lock, and a repository written to once
never contends. The lock is keyed on the branch, not the repository, so an
unprotected branch does not wait on a protected one.

**`max_concurrency` caps writes in flight.** This is about the rate the token is
spent rather than about collisions. A write is four API calls on the direct
route and eight on the pull request route, so the default parallelism of ten can
leave eighty requests outstanding against an allowance that is fixed per hour
and shared with every other workspace. Terraform's own `-parallelism` would also
cap this, but it caps the whole graph; this caps only the provider with the
budget problem. Reads are not counted — a refresh reads every resource in state,
it is one call rather than four or eight, and nothing races it.

Neither limit reaches beyond the Terraform process. Where one token is shared
across workspaces that apply as separate runs, a lock here does nothing about a
second workspace applying at the same time; what it covers is one apply
colliding with itself, which is the usual case when a change to a shared module
re-applies every workspace at once.

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
