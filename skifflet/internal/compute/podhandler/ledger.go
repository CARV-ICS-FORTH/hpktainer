package podhandler

import (
	"sync"
)

// CleanupTask represents a cleanup action for an acquired resource.
type CleanupTask struct {
	Name string
	Run  func() error
}

// ResourceLedger maintains a LIFO stack of cleanup tasks for resources acquired during a Pod's lifetime.
type ResourceLedger struct {
	mu    sync.Mutex
	tasks []CleanupTask
}

// NewResourceLedger creates an empty ResourceLedger.
func NewResourceLedger() *ResourceLedger {
	return &ResourceLedger{}
}

// Register registers an acquired resource with its cleanup action.
func (l *ResourceLedger) Register(name string, run func() error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tasks = append(l.tasks, CleanupTask{Name: name, Run: run})
}

// CleanupAll executes all registered cleanup tasks in reverse acquisition (LIFO) order.
func (l *ResourceLedger) CleanupAll() []error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var errs []error
	for i := len(l.tasks) - 1; i >= 0; i-- {
		task := l.tasks[i]
		if err := task.Run(); err != nil {
			errs = append(errs, err)
		}
	}
	l.tasks = nil
	return errs
}
