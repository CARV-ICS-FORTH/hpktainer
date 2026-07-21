package events

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"hpk/internal/compute"
	"hpk/internal/compute/endpoint"

	"github.com/fsnotify/fsnotify"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestEventHandlerPushDoesNotDrop(t *testing.T) {
	eh := NewEventHandler(Options{
		MaxWorkers:   1,
		MaxQueueSize: 2,
	})

	ctx := context.Background()

	evt1 := fsnotify.Event{Name: "test1", Op: fsnotify.Create}
	evt2 := fsnotify.Event{Name: "test2", Op: fsnotify.Create}

	eh.Push(ctx, evt1)
	eh.Push(ctx, evt2)

	if len(eh.Queue) != 2 {
		t.Fatalf("expected 2 items in queue, got %d", len(eh.Queue))
	}

	done := make(chan bool)
	evt3 := fsnotify.Event{Name: "test3", Op: fsnotify.Create}

	go func() {
		eh.Push(ctx, evt3)
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

func TestEventHandlerWorkerPanicsRecover(t *testing.T) {
	compute.HPK = endpoint.HPK(t.TempDir())

	eh := NewEventHandler(Options{
		MaxWorkers:   1,
		MaxQueueSize: 10,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var processedCount int32

	control := PodControl{
		UpdateStatus: func(pod *corev1.Pod) {
			val := atomic.AddInt32(&processedCount, 1)
			if val == 1 {
				panic("simulated worker panic")
			}
		},
		LoadFromDisk: func(podRef client.ObjectKey) (*corev1.Pod, error) {
			return &corev1.Pod{}, nil
		},
		NotifyVirtualKubelet: func(pod *corev1.Pod) {},
	}

	listenDone := make(chan struct{})
	go func() {
		eh.Listen(ctx, control)
		close(listenDone)
	}()

	pod1Key := client.ObjectKey{Namespace: "default", Name: "pod1"}
	pod2Key := client.ObjectKey{Namespace: "default", Name: "pod2"}

	// Event 1 will trigger panic in UpdateStatus
	evt1 := fsnotify.Event{Name: compute.HPK.Pod(pod1Key).IPAddressPath(), Op: fsnotify.Create}
	eh.Push(ctx, evt1)

	// Event 2 should still be processed by the worker after recovering
	evt2 := fsnotify.Event{Name: compute.HPK.Pod(pod2Key).IPAddressPath(), Op: fsnotify.Create}
	eh.Push(ctx, evt2)

	// Wait for worker to process both
	for i := 0; i < 50; i++ {
		if atomic.LoadInt32(&processedCount) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if atomic.LoadInt32(&processedCount) < 2 {
		t.Fatalf("expected at least 2 processed events after panic, got %d", atomic.LoadInt32(&processedCount))
	}

	// Cancel context and ensure Listen finishes cleanly without deadlock (WaitGroup.Done hoisted)
	cancel()

	select {
	case <-listenDone:
		// Passed
	case <-time.After(1 * time.Second):
		t.Fatal("Listen deadlocked or failed to return after context cancellation")
	}
}

func TestEventHandlerPushCancelledByContext(t *testing.T) {
	eh := NewEventHandler(Options{
		MaxWorkers:   1,
		MaxQueueSize: 1,
	})

	ctx, cancel := context.WithCancel(context.Background())

	// Fill queue
	eh.Push(ctx, fsnotify.Event{Name: "evt1"})

	pushDone := make(chan struct{})
	go func() {
		eh.Push(ctx, fsnotify.Event{Name: "evt2"})
		close(pushDone)
	}()

	select {
	case <-pushDone:
		t.Fatal("Push should block when queue is full")
	case <-time.After(50 * time.Millisecond):
	}

	// Cancel context
	cancel()

	select {
	case <-pushDone:
		// Unblocked on context cancellation
	case <-time.After(1 * time.Second):
		t.Fatal("Push failed to unblock after context cancellation")
	}
}
