// Copyright © 2022 FORTH-ICS
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package events

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"hpk/internal/compute"
	"hpk/internal/compute/endpoint"

	"github.com/fsnotify/fsnotify"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

/************************************************************

			Listen for pod events

************************************************************/

// Options represent options for EventHandler.
type Options struct {
	MaxWorkers   int // Number of workers to spawn.
	MaxQueueSize int // Maximum length for the queue to hold events.
}

func NewEventHandler(opts Options) *EventHandler {
	return &EventHandler{
		Opts:     opts,
		Queue:    make(chan fsnotify.Event, opts.MaxQueueSize),
		locker:   sync.RWMutex{},
		Finished: false,
	}
}

// EventHandler represents the datastructure for an
// EventHandler instance. This struct satisfies the
// EventHandler interface.
type EventHandler struct {
	Opts  Options
	Queue chan fsnotify.Event

	locker   sync.RWMutex
	Finished bool
}

// Push adds a new event payload to the queue.
func (h *EventHandler) Push(ctx context.Context, event fsnotify.Event) {
	h.locker.RLock()
	defer h.locker.RUnlock()

	if h.Finished {
		compute.DefaultLogger.Info("drop event due to queue being closed.",
			"event", event.String(),
		)
		return
	}

	if ctx == nil {
		h.Queue <- event
		return
	}

	select {
	case <-ctx.Done():
		compute.DefaultLogger.Info("drop event due to context done",
			"event", event.String(),
		)
	case h.Queue <- event:
	}
}

type PodControl struct {
	UpdateStatus         func(pod *corev1.Pod)
	LoadFromDisk         func(podRef client.ObjectKey) (*corev1.Pod, error)
	NotifyVirtualKubelet func(pod *corev1.Pod)
}

// Listen spawns workers and listens to the queue
// It's a blocking function and waits for a cancellation
// invocation from the Client.
func (h *EventHandler) Listen(ctx context.Context, control PodControl) {
	waitGroup := sync.WaitGroup{}
	for i := 0; i < h.Opts.MaxWorkers; i++ {
		waitGroup.Add(1)

		go func() {
			defer waitGroup.Done()
			for {
				select {
				case <-ctx.Done():
					compute.DefaultLogger.Info("Shutting down the event listener", "err", ctx.Err())

					// Ensure no new messages are added.
					h.locker.Lock()
					h.Finished = true
					h.locker.Unlock()

					return
				case event, ok := <-h.Queue:
					if !ok {
						return
					}
					h.processEvent(event, control)
				}
			}
		}()
	}

	waitGroup.Wait()
}

func (h *EventHandler) processEvent(event fsnotify.Event, control PodControl) {
	defer func() {
		if r := recover(); r != nil {
			compute.DefaultLogger.Error(fmt.Errorf("%v", r), "Recovered panic in event listener worker")
		}
	}()

	// ensure that the file is a control file.
	podkey, file, invalid := compute.HPK.ParseControlFilePath(event.Name)
	if invalid {
		compute.DefaultLogger.Info("Event: omit unexpected event", "details", event)
		return
	}

	logger := compute.DefaultLogger.WithValues("pod", podkey)

	/*-- Skip events that are not related to pod changes --*/
	/* In previous versions, this condition was fsnotify.Write, with the goal to avoid race
	conditions between creating a file and writing a file. However, this does not seem to work
	with the Polling watcher, and we can only capture Create events. In turn, that means that
	file readers must retry if there are no contents in the file.
	*/
	if !(event.Op.Has(fsnotify.Create) || event.Op.Has(fsnotify.Write)) {
		logger.Info("Event: omit known event", "op", event.Op, "file", file)
		return
	}

	/*---------------------------------------------------
	 * Declare events that warrant Pod reconciliation
	 *---------------------------------------------------*/
	ext := filepath.Ext(file)
	switch ext {
	case endpoint.ExtensionSysError:
		/*-- Pod failed. Pod should fail immediately without other checks --*/
		logger.Info("[Runtime] -> Pod initialization error", "op", event.Op, "file", file)

		// load local pod
		pod, err := control.LoadFromDisk(podkey)
		if err != nil {
			logger.Error(err, "pod does not exist for syserror event", "pod", podkey)
			return
		}

		// get failure reason
		sysErrFile := compute.HPK.Pod(podkey).SysErrorFilePath()

		reason, err := os.ReadFile(sysErrFile)
		if err != nil {
			logger.Error(err, "failed to read syserror file", "file", sysErrFile)
			reason = []byte("Pod creation failed with system error")
		}

		// FIXME: print only the last few lined
		logger.Info("[SYSERROR]", "details", string(reason))

		// set the pod as failed
		compute.PodError(pod, "SYSERROR", "Pod creation has failed: %s", string(reason))

		// update the remote copy
		control.NotifyVirtualKubelet(pod)

		return

	case endpoint.ExtensionIP: // Pod started
		logger.Info("[Runtime] -> Pod Started", "op", event.Op, "file", file)

	case endpoint.ExtensionJobID: // Container Started
		if file == "pause"+string(endpoint.ExtensionJobID) {
			logger.Info("[Runtime] -> Pause Process Started", "op", event.Op, "file", file)
			return
		}
		logger.Info("[Runtime] -> Container Started", "op", event.Op, "file", file)

	case endpoint.ExtensionExitCode: // Container Terminated
		logger.Info("[Runtime] -> Container Terminated", "op", event.Op, "file", file)

	default:
		/*-- Any other file is ignored --*/
		compute.DefaultLogger.Info("Ignore event", "details", event)

		return
	}

	compute.DefaultLogger.Info("New event", "details", event)

	/*---------------------------------------------------
	 * Reconcile Pod and Notify Virtual Kubelet
	 *---------------------------------------------------*/

	/*-- Load Pod from reference --*/
	pod, err := control.LoadFromDisk(podkey)
	if err != nil {
		// Race conditions may exist between the deletion of a pod and events.
		logger.Info("Omit event",
			"reason", "pod was not found. this is probably a conflict",
			"pod", podkey,
		)

		return
	}

	if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
		// TODO: Should I remove the watcher now, or when the pod is deleted ?

		logger.Info("Ignore event since Pod is in terminal phase",
			"event", event,
			"phase", pod.Status.Phase,
		)

		return
	}

	/*-- Recalculate the Pod status from locally stored containers --*/
	control.UpdateStatus(pod)

	/*-- Update the remote Copy --*/
	control.NotifyVirtualKubelet(pod)

	logger.Info("[Runtime] <- Listen for events")
}
