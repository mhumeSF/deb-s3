package lock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deb-s3/deb-s3/internal/storage"
)

func TestTwoContendersCannotBothAcquire(t *testing.T) {
	store := storage.NewMemoryStore("prefix")
	start := make(chan struct{})
	results := make(chan *Handle, 2)
	errorsChannel := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, owner := range []Owner{testOwner("one"), testOwner("two")} {
		owner := owner
		go func() {
			ready.Done()
			<-start
			handle, err := (Manager{Store: store, Options: Options{Owner: owner, MaxAttempts: 1}}).Acquire(context.Background(), "stable")
			results <- handle
			errorsChannel <- err
		}()
	}
	ready.Wait()
	close(start)

	var acquired []*Handle
	var failed int
	for range 2 {
		if handle := <-results; handle != nil {
			acquired = append(acquired, handle)
		}
		if err := <-errorsChannel; errors.Is(err, ErrLocked) {
			failed++
		} else if err != nil {
			t.Fatalf("Acquire() unexpected error = %v", err)
		}
	}
	if len(acquired) != 1 || failed != 1 {
		t.Fatalf("acquired=%d failed=%d", len(acquired), failed)
	}
	if err := acquired[0].Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationInterruptsWaiting(t *testing.T) {
	store := storage.NewMemoryStore("")
	holder, err := (Manager{Store: store, Options: Options{Owner: testOwner("holder")}}).Acquire(context.Background(), "stable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Release(context.Background()) })

	reported := make(chan WaitInfo, 1)
	manager := Manager{Store: store, Options: Options{
		Owner: testOwner("waiter"), MaxAttempts: 100,
		InitialBackoff: time.Hour, MaxBackoff: time.Hour,
		Reporter: func(info WaitInfo) { reported <- info },
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := manager.Acquire(ctx, "stable")
		done <- err
	}()
	select {
	case info := <-reported:
		if info.Current.Token != "holder" || info.Attempt != 1 {
			t.Fatalf("WaitInfo = %#v", info)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting contender did not report lock owner")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Acquire() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt lock wait")
	}
}

func TestAcquireRetriesAreBoundedAndReported(t *testing.T) {
	store := storage.NewMemoryStore("")
	holder, err := (Manager{Store: store, Options: Options{Owner: testOwner("holder")}}).Acquire(context.Background(), "stable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Release(context.Background()) })
	var reports atomic.Int32
	_, err = (Manager{Store: store, Options: Options{
		Owner: testOwner("waiter"), MaxAttempts: 3,
		InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
		Reporter: func(WaitInfo) { reports.Add(1) },
	}}).Acquire(context.Background(), "stable")
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Acquire() error = %v", err)
	}
	if reports.Load() != 3 {
		t.Fatalf("reports = %d, want 3", reports.Load())
	}
}

func TestReleaseCannotDeleteReplacementOwner(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("")
	first, err := (Manager{Store: store, Options: Options{Owner: testOwner("first")}}).Acquire(ctx, "stable")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, LockPath("stable")); err != nil {
		t.Fatal(err)
	}
	second, err := (Manager{Store: store, Options: Options{Owner: testOwner("second")}}).Acquire(ctx, "stable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Release(context.Background()) })

	if err := first.Release(ctx); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("first Release() error = %v", err)
	}
	current, err := (Manager{Store: store}).Current(ctx, "stable")
	if err != nil {
		t.Fatal(err)
	}
	if current.Token != "second" {
		t.Fatalf("current owner = %#v", current)
	}
}

func TestConditionalReleaseClosesReplacementRace(t *testing.T) {
	ctx := context.Background()
	base := storage.NewMemoryStore("")
	store := &replaceDuringDeleteStore{MemoryStore: base, replacement: testOwner("replacement")}
	handle, err := (Manager{Store: store, Options: Options{Owner: testOwner("first")}}).Acquire(ctx, "stable")
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Release(ctx); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("Release() error = %v", err)
	}
	current, err := (Manager{Store: store}).Current(ctx, "stable")
	if err != nil {
		t.Fatal(err)
	}
	if current.Token != "replacement" {
		t.Fatalf("replacement lock was deleted: %#v", current)
	}
}

func TestAcquireRejectsStoreWithoutConditionalDelete(t *testing.T) {
	store := struct{ storage.Store }{Store: storage.NewMemoryStore("")}
	_, err := (Manager{Store: store, Options: Options{Owner: testOwner("owner")}}).Acquire(context.Background(), "stable")
	if !errors.Is(err, ErrConditionalDeleteUnsupported) {
		t.Fatalf("Acquire() error = %v", err)
	}
}

func TestReleaseAllowsSubsequentAcquire(t *testing.T) {
	store := storage.NewMemoryStore("")
	handle, err := (Manager{Store: store, Options: Options{Owner: testOwner("owner")}}).Acquire(context.Background(), "stable")
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := (Manager{Store: store, Options: Options{Owner: testOwner("second")}}).Acquire(context.Background(), "stable")
	if err != nil {
		t.Fatalf("second Acquire() = %v", err)
	}
	if err := second.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func testOwner(token string) Owner {
	return Owner{Token: token, User: token + "-user", Host: token + "-host", PID: 42}
}

type replaceDuringDeleteStore struct {
	*storage.MemoryStore
	replacement Owner
}

func (s *replaceDuringDeleteStore) DeleteIfMatch(ctx context.Context, key, etag string) error {
	body, err := json.Marshal(s.replacement)
	if err != nil {
		return err
	}
	if err := s.MemoryStore.Delete(ctx, key); err != nil {
		return err
	}
	if err := s.MemoryStore.Put(ctx, key, bytes.NewReader(body), storage.PutOptions{CreateOnly: true}); err != nil {
		return err
	}
	return s.MemoryStore.DeleteIfMatch(ctx, key, etag)
}
