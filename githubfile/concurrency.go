package githubfile

import (
	"context"
	"sync"
)

type branchLocks struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func newBranchLocks() *branchLocks {
	return &branchLocks{m: make(map[string]*sync.Mutex)}
}

// acquire blocks until the branch is free and returns the release function.
func (b *branchLocks) acquire(owner, repo, branch string) func() {
	if b == nil {
		return func() {}
	}

	key := owner + "/" + repo + "@" + branch

	b.mu.Lock()
	l, ok := b.m[key]
	if !ok {
		l = &sync.Mutex{}
		b.m[key] = l
	}
	b.mu.Unlock()

	l.Lock()
	return l.Unlock
}

// semaphore caps how many file operations this provider has in flight at once.
type semaphore chan struct{}

func newSemaphore(n int) semaphore {
	if n <= 0 {
		return nil
	}
	return make(semaphore, n)
}

// acquire blocks for a slot, returning the release function. It gives up only if
// the context is cancelled first, in which case the release function is a no-op
// and must still be safe to call.
func (s semaphore) acquire(ctx context.Context) (func(), error) {
	if s == nil {
		return func() {}, nil
	}

	select {
	case s <- struct{}{}:
		return func() { <-s }, nil
	case <-ctx.Done():
		return func() {}, ctx.Err()
	}
}

// withWriteLimits runs fn under both limits: the branch is held for the whole
// operation, and the operation occupies one of the concurrency slots.
func withWriteLimits(ctx context.Context, c *providerConfiguration, f *file, fn func() error) error {
	releaseBranch := c.branchLocks.acquire(f.repositoryOwner, f.repositoryName, f.branch)
	defer releaseBranch()

	releaseSlot, err := c.writeSlots.acquire(ctx)
	if err != nil {
		return err
	}
	defer releaseSlot()

	return fn()
}
