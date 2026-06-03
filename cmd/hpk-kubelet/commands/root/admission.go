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

package root

import (
	"context"
	"fmt"
	"net/http"

	"hpk/internal/provider"

	"errors"

	"github.com/sirupsen/logrus"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
)

func StartAPIServer(c Opts, virtualk8s *provider.VirtualK8S) {
	mux := http.NewServeMux()

	mux.Handle("/hello", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Write([]byte("Hi there! I 'm HPK-Kubelet. My job is to run your Kubernetes stuff on Slurm.\n"))
	}))

	/*---------------------------------------------------
	 * Add handlers for Logs and Statistics
	 *---------------------------------------------------*/
	api.AttachPodRoutes(api.PodHandlerConfig{
		RunInContainer:   virtualk8s.RunInContainer,
		GetContainerLogs: virtualk8s.GetContainerLogs,
		GetPods:          virtualk8s.GetPods,
		PortForward:      virtualk8s.PortForward,
		// AttachToContainer: ,
		// GetStatsSummary:       virtualk8s.GetStatsSummary,
		// StreamIdleTimeout:     0,
		// StreamCreationTimeout: 0,
	}, mux, true)

	/*---------------------------------------------------
	 * Start the Webhook on the background
	 *---------------------------------------------------*/
	allAddr := fmt.Sprintf(":%d", c.KubeletPort)
	advertisedAddr := fmt.Sprintf("%s:%d", c.KubeletAddress, c.KubeletPort)

	go func() {
		if err := http.ListenAndServeTLS(
			allAddr,
			c.K8sAPICertFilepath,
			c.K8sAPIKeyFilepath,
			mux,
		); err != nil && !errors.Is(err, context.Canceled) {
			logrus.Fatal("API Server has failed. Err:", err)
			// handle error
		}
	}()

	DefaultLogger.Info("HTTPS server is ready",
		"Address", advertisedAddr,
		"cert", c.K8sAPICertFilepath,
		"key", c.K8sAPIKeyFilepath,
	)
}
