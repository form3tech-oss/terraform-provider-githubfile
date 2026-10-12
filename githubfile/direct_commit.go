package githubfile

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/form3tech-oss/go-github-utils/pkg/branch"
	"github.com/form3tech-oss/go-github-utils/pkg/commit"
	"github.com/google/go-github/v54/github"
)

const branchPrefix = "refs/heads/"

func createCommit(ctx context.Context, c *providerConfiguration, options *commit.CommitOptions) error {
	if c.directCommit {
		return createCommitDirect(ctx, c.githubClient, c.retry, options)
	}

	err := commit.CreateCommit(ctx, c.githubClient, options)
	if err != nil {
		deleteWorkingBranch(ctx, c, options)
	}
	return err
}

func deleteWorkingBranch(ctx context.Context, c *providerConfiguration, options *commit.CommitOptions) {
	name := options.PullRequestSourceBranchName
	if name == "" {
		// The route generated its own name and did not report it; there is
		// nothing reliable to delete.
		return
	}
	if !strings.HasPrefix(name, branchPrefix) {
		name = branchPrefix + name
	}

	// Detached from the caller's context and given its own deadline: a write
	// cancelled midway is one of the ways a branch is left behind, and cleanup
	// inheriting that cancellation would skip exactly those cases.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if _, err := c.githubClient.Git.DeleteRef(ctx, options.RepoOwner, options.RepoName, name); err != nil {
		log.Printf("[WARN] failed to delete working branch %q on %s/%s after a failed write, it may need removing by hand: %v",
			name, options.RepoOwner, options.RepoName, err)
	}
}

func createCommitDirect(ctx context.Context, client *github.Client, retry retryConfig, options *commit.CommitOptions) error {
	maxRetries := options.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	retryBackoff := options.RetryBackoff
	if retryBackoff == 0 {
		retryBackoff = 5 * time.Second
	}

	b := options.Branch
	if b == "" {
		var v string
		if err := withRateLimitRetry(ctx, retry, func() (err error) {
			v, err = branch.GetDefaultBranch(ctx, client, options.RepoOwner, options.RepoName)
			return err
		}); err != nil {
			return err
		}
		b = v
	}

	// Read once, outside the loop: the key is the same on every attempt, and
	// decrypting it is the one step here that does not touch the API.
	var signingKey *openpgp.Entity
	if options.GpgPrivateKey != "" {
		k, err := readGPGPrivateKey(options.GpgPrivateKey, options.GpgPassphrase)
		if err != nil {
			return err
		}
		signingKey = k
	}

	var (
		overrideTracksTip bool
		overrideDecided   bool
	)

	for attempt := 1; ; attempt++ {
		var baseSHA string
		if err := withRateLimitRetry(ctx, retry, func() (err error) {
			baseSHA, err = branch.GetSHAForBranch(ctx, client, options.RepoOwner, options.RepoName, b)
			return err
		}); err != nil {
			return err
		}

		if !overrideDecided {
			overrideTracksTip = options.BaseTreeOverride != nil && *options.BaseTreeOverride == baseSHA
			overrideDecided = true
		}

		baseTree := baseSHA
		if options.BaseTreeOverride != nil && !overrideTracksTip {
			baseTree = *options.BaseTreeOverride
		}

		var tree *github.Tree
		if err := withRateLimitRetry(ctx, retry, func() (err error) {
			tree, _, err = client.Git.CreateTree(ctx, options.RepoOwner, options.RepoName, baseTree, options.Changes)
			return err
		}); err != nil {
			return err
		}

		// The parent is carried by SHA alone - go-github sends only parent.SHA,
		// and signs over that - so the pull request route's separate read of the
		// parent commit is not needed here.
		date := github.Timestamp{Time: time.Now()}
		var newCommit *github.Commit
		if err := withRateLimitRetry(ctx, retry, func() (err error) {
			newCommit, _, err = client.Git.CreateCommit(ctx, options.RepoOwner, options.RepoName, &github.Commit{
				Author: &github.CommitAuthor{
					Date:  &date,
					Name:  github.String(options.Username),
					Email: github.String(options.Email),
				},
				Message:    github.String(options.CommitMessage),
				Tree:       tree,
				Parents:    []*github.Commit{{SHA: github.String(baseSHA)}},
				SigningKey: signingKey,
			})
			return err
		}); err != nil {
			return err
		}

		// force=false makes this a compare-and-swap: GitHub applies it only if
		// the branch still points at the parent we built on.
		err := withRateLimitRetry(ctx, retry, func() (err error) {
			_, _, err = client.Git.UpdateRef(ctx, options.RepoOwner, options.RepoName, &github.Reference{
				Ref:    github.String(branchPrefix + b),
				Object: &github.GitObject{SHA: newCommit.SHA},
			}, false)
			return err
		})
		if err == nil {
			return nil
		}

		if !isBranchMoved(err) {
			return fmt.Errorf("failed to update %q: %v", b, err)
		}
		if attempt >= maxRetries {
			return fmt.Errorf("failed to update %q after %d attempts, the branch moved under each: %v", b, maxRetries, err)
		}

		// Jittered, so that writers rejected by the same push do not come back
		// in step and collide again as a group.
		select {
		case <-time.After(retryBackoff + time.Duration(rand.Int63n(int64(retryBackoff)))):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// isBranchMoved reports whether err is GitHub rejecting a ref update because the
// branch no longer points where it did when we read it. There is no typed error
// for it; it arrives as a 422 whose message names the fast-forward rule.
func isBranchMoved(err error) bool {
	var e *github.ErrorResponse
	if !errors.As(err, &e) || e.Response == nil {
		return false
	}
	if e.Response.StatusCode != http.StatusUnprocessableEntity {
		return false
	}
	m := strings.ToLower(e.Message)
	return strings.Contains(m, "fast forward") || strings.Contains(m, "fast-forward")
}

// readGPGPrivateKey mirrors the unexported helper in go-github-utils, which the
// pull request route uses for the same purpose.
func readGPGPrivateKey(privateKey string, passphrase string) (*openpgp.Entity, error) {
	entityList, err := openpgp.ReadArmoredKeyRing(strings.NewReader(privateKey))
	if err != nil {
		return nil, err
	}

	pk := entityList[0]
	ppb := []byte(passphrase)

	if pk.PrivateKey != nil && pk.PrivateKey.Encrypted {
		if err := pk.PrivateKey.Decrypt(ppb); err != nil {
			return nil, err
		}
	}

	for _, subKey := range pk.Subkeys {
		if subKey.PrivateKey != nil && subKey.PrivateKey.Encrypted {
			if err := subKey.PrivateKey.Decrypt(ppb); err != nil {
				return nil, err
			}
		}
	}
	return pk, nil
}
