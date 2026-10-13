package githubfile

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBranchLocks_SerialisesTheSameBranch(t *testing.T) {
	b := newBranchLocks()

	var inFlight, maxInFlight int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := b.acquire("form3tech", "infrastructure-github", "master")
			defer release()

			n := atomic.AddInt32(&inFlight, 1)
			for {
				m := atomic.LoadInt32(&maxInFlight)
				if n <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
		}()
	}
	wg.Wait()

	if maxInFlight != 1 {
		t.Errorf("expected writes to one branch to be serialised, saw %d at once", maxInFlight)
	}
}

func TestBranchLocks_DoNotBlockOtherBranches(t *testing.T) {
	b := newBranchLocks()

	release := b.acquire("form3tech", "infrastructure-github", "master")
	defer release()

	for _, target := range [][3]string{
		{"form3tech", "infrastructure-github", "main"},
		{"form3tech", "change-management-configuration", "master"},
		{"form3tech-oss", "infrastructure-github", "master"},
	} {
		done := make(chan struct{})
		go func(owner, repo, branch string) {
			b.acquire(owner, repo, branch)()
			close(done)
		}(target[0], target[1], target[2])

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("%s/%s@%s waited on an unrelated branch's lock", target[0], target[1], target[2])
		}
	}
}

func TestBranchLocks_NilIsUsable(t *testing.T) {
	var b *branchLocks
	release := b.acquire("form3tech", "infrastructure-github", "master")
	release()
}

func TestSemaphore_CapsConcurrency(t *testing.T) {
	const limit = 3
	s := newSemaphore(limit)

	var inFlight, maxInFlight int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := s.acquire(context.Background())
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			defer release()

			n := atomic.AddInt32(&inFlight, 1)
			for {
				m := atomic.LoadInt32(&maxInFlight)
				if n <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
		}()
	}
	wg.Wait()

	if maxInFlight > limit {
		t.Errorf("expected at most %d in flight, saw %d", limit, maxInFlight)
	}
	if maxInFlight < 2 {
		t.Errorf("expected the limit to still allow parallelism, saw only %d at once", maxInFlight)
	}
}

func TestSemaphore_ZeroIsUnlimited(t *testing.T) {
	s := newSemaphore(0)
	if s != nil {
		t.Fatal("expected a zero limit to mean no semaphore at all")
	}

	// All of these must be held at once without blocking.
	var releases []func()
	for i := 0; i < 50; i++ {
		release, err := s.acquire(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		releases = append(releases, release)
	}
	for _, r := range releases {
		r()
	}
}

func TestSemaphore_StopsOnContextCancellation(t *testing.T) {
	s := newSemaphore(1)

	release, err := s.acquire(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()

	blocked, err := s.acquire(ctx)
	if err != context.Canceled {
		t.Errorf("expected the cancellation to be returned, got: %v", err)
	}
	// The release function has to be safe to call even when nothing was taken.
	blocked()
}

func TestWithWriteLimits_HoldsTheBranchForTheWholeOperation(t *testing.T) {
	c := &providerConfiguration{branchLocks: newBranchLocks()}
	f := &file{repositoryOwner: "form3tech", repositoryName: "infrastructure-github", branch: "master"}

	var inFlight, maxInFlight int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = withWriteLimits(context.Background(), c, f, func() error {
				n := atomic.AddInt32(&inFlight, 1)
				for {
					m := atomic.LoadInt32(&maxInFlight)
					if n <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, n) {
						break
					}
				}
				time.Sleep(time.Millisecond)
				atomic.AddInt32(&inFlight, -1)
				return nil
			})
		}()
	}
	wg.Wait()

	if maxInFlight != 1 {
		t.Errorf("expected one write at a time per branch, saw %d", maxInFlight)
	}
}

// Taking the branch lock before the slot means a writer queued behind another
// writer's branch is not also occupying a slot meant for work in flight.
func TestWithWriteLimits_QueuedWritersDoNotHoldSlots(t *testing.T) {
	c := &providerConfiguration{branchLocks: newBranchLocks(), writeSlots: newSemaphore(1)}
	busy := &file{repositoryOwner: "form3tech", repositoryName: "busy", branch: "master"}
	other := &file{repositoryOwner: "form3tech", repositoryName: "other", branch: "master"}

	started := make(chan struct{})
	finish := make(chan struct{})
	go func() {
		_ = withWriteLimits(context.Background(), c, busy, func() error {
			close(started)
			<-finish
			return nil
		})
	}()
	<-started

	// A second writer to the same branch queues on the lock, not on the slot.
	queued := make(chan struct{})
	go func() {
		_ = withWriteLimits(context.Background(), c, busy, func() error { return nil })
		close(queued)
	}()

	select {
	case <-queued:
		t.Fatal("expected the second writer to the same branch to wait")
	case <-time.After(20 * time.Millisecond):
	}

	close(finish)
	select {
	case <-queued:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the queued writer to proceed once the branch was free")
	}

	// And the single slot is free again for an unrelated repository.
	done := make(chan struct{})
	go func() {
		_ = withWriteLimits(context.Background(), c, other, func() error { return nil })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the slot to have been released")
	}
}

func TestWithWriteLimits_ReturnsCancellationWithoutRunning(t *testing.T) {
	c := &providerConfiguration{branchLocks: newBranchLocks(), writeSlots: newSemaphore(1)}
	f := &file{repositoryOwner: "form3tech", repositoryName: "infrastructure-github", branch: "master"}

	release, err := c.writeSlots.acquire(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()

	ran := false
	err = withWriteLimits(ctx, c, f, func() error { ran = true; return nil })
	if err != context.Canceled {
		t.Errorf("expected the cancellation to be returned, got: %v", err)
	}
	if ran {
		t.Error("expected the operation not to run once cancelled")
	}
}
