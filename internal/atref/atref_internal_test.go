package atref

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestCallGroupRecoversPanicAndUnwedges proves the MED fix: a panic inside
// fn is recovered and returned as an error (not re-raised), the waiters
// blocked on the same key all unblock with that error (not deadlock), and
// — critically — a SUBSEQUENT call for the SAME key is not permanently
// wedged: the entry was deleted, so the next call runs fresh and succeeds.
func TestCallGroupRecoversPanicAndUnwedges(t *testing.T) {
	g := newCallGroup()

	// Concurrent callers for one key: the first runs fn (which panics mid-
	// flight after the others have had a chance to join and block on it).
	const waiters = 4
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, waiters)
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, errs[idx] = g.Do("same-key", func() ([]byte, error) {
				// Give the other goroutines time to join as waiters before
				// the panic, so we exercise the waiter-unblock path too.
				time.Sleep(50 * time.Millisecond)
				panic("boom")
			})
		}(i)
	}
	close(start)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("callGroup.Do deadlocked after a panicking build (waiters never unblocked)")
	}
	for i, err := range errs {
		if err == nil {
			t.Fatalf("caller %d: expected an error from the panicking build, got nil", i)
		}
	}

	// The key must be usable again — a fresh build runs and succeeds.
	got, err := g.Do("same-key", func() ([]byte, error) {
		return []byte("ok"), nil
	})
	if err != nil {
		t.Fatalf("subsequent call after panic should succeed, got: %v", err)
	}
	if string(got) != "ok" {
		t.Fatalf("subsequent call returned %q, want \"ok\"", got)
	}
}

// TestCallGroupDedupsConcurrentCalls confirms fn runs ONCE across
// concurrent callers of the same key, and all callers get that one result.
func TestCallGroupDedupsConcurrentCalls(t *testing.T) {
	g := newCallGroup()
	var runs int32
	var mu sync.Mutex
	inc := func() { mu.Lock(); runs++; mu.Unlock() }

	const callers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			b, err := g.Do("k", func() ([]byte, error) {
				inc()
				time.Sleep(30 * time.Millisecond)
				return []byte(fmt.Sprintf("v")), nil
			})
			if err == nil {
				results[idx] = string(b)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if runs != 1 {
		t.Fatalf("expected fn to run exactly once, ran %d times", runs)
	}
	for i, r := range results {
		if r != "v" {
			t.Fatalf("caller %d got %q, want \"v\"", i, r)
		}
	}
}
