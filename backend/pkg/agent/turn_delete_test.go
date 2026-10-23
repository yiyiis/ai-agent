package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeleteWaitsForPersistenceAndRejectsStaleTurns(t *testing.T) {
	r := NewTurnRegistry()
	cancelled, finish := make(chan struct{}), make(chan struct{})
	var persisted atomic.Bool
	_, err := r.Start("delete-root", func(ctx context.Context, emit func(Event)) error {
		<-ctx.Done()
		close(cancelled)
		<-finish // final WithoutCancel persistence still in progress
		persisted.Store(true)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- r.DeleteWhenStopped(context.Background(), "delete-root", func() error {
			if !persisted.Load() {
				return errors.New("deleted before final persistence")
			}
			return nil
		})
	}()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("turn not cancelled")
	}
	if _, err := r.Start("delete-root", emitAll()); err == nil {
		t.Fatal("new turn admitted during deletion")
	}
	select {
	case <-done:
		t.Fatal("delete did not wait")
	default:
	}
	close(finish)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("delete did not finish")
	}
	if _, err := r.Start("delete-root", emitAll()); err == nil {
		t.Fatal("stale request resurrected deleted session")
	}
}

func TestFailedDeleteReleasesFence(t *testing.T) {
	r := NewTurnRegistry()
	err := r.DeleteWhenStopped(context.Background(), "retry", func() error { return errors.New("database unavailable") })
	if err == nil {
		t.Fatal("expected deletion error")
	}
	if _, err := r.Start("retry", emitAll()); err != nil {
		t.Fatal(err)
	}
	waitTurnDone(t, r, "retry")
}
