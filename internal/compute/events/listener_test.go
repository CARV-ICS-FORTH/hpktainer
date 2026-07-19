package events

import (
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestEventHandlerPushDoesNotDrop(t *testing.T) {
	eh := NewEventHandler(Options{
		MaxWorkers:   1,
		MaxQueueSize: 2,
	})

	evt1 := fsnotify.Event{Name: "test1", Op: fsnotify.Create}
	evt2 := fsnotify.Event{Name: "test2", Op: fsnotify.Create}

	eh.Push(evt1)
	eh.Push(evt2)

	if len(eh.Queue) != 2 {
		t.Fatalf("expected 2 items in queue, got %d", len(eh.Queue))
	}

	done := make(chan bool)
	evt3 := fsnotify.Event{Name: "test3", Op: fsnotify.Create}

	go func() {
		eh.Push(evt3)
		done <- true
	}()

	select {
	case <-done:
		t.Fatal("Push should have blocked when queue was full")
	case <-time.After(50 * time.Millisecond):
		// Expected to block
	}

	// Drain one item
	<-eh.Queue

	select {
	case <-done:
		// Successfully unblocked after queue space became available
	case <-time.After(1 * time.Second):
		t.Fatal("Push failed to unblock after queue space was freed")
	}

	if len(eh.Queue) != 2 {
		t.Fatalf("expected 2 items in queue after push unblocked, got %d", len(eh.Queue))
	}
}
