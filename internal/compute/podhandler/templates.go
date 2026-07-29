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

package podhandler

import (
	"fmt"
	"strings"
	"text/template"

	"hpk/internal/compute"

	"al.essio.dev/pkg/shellescape"
	"github.com/Masterminds/sprig"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

var genericMap = map[string]interface{}{
	"param":    EscapeSingleQuote,
	"truncate": truncate,
}

// ParseTemplate returns a custom 'text/template' enhanced with functions for processing HPK templates.
func ParseTemplate(text string) (*template.Template, error) {
	return template.New("").
		Funcs(sprig.TxtFuncMap()).
		Funcs(genericMap).
		Option("missingkey=error").Parse(text)
}

func EscapeSingleQuote(str ...interface{}) string {
	out := make([]string, 0, len(str))
	for _, s := range str {
		if s != nil {
			// wrap fields into single quotes, but escape any single quotes from the payload.
			// escaped := strings.ReplaceAll(strval(s), "'", "\\'")
			escaped := shellescape.Quote(strval(s))
			out = append(out, fmt.Sprintf("%v", escaped))
		}
	}
	return strings.Join(out, " ")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func strval(v interface{}) string {
	switch v := v.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case error:
		return v.Error()
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

const HostScriptTemplate = `#!/bin/bash

#### BEGIN SECTION: Host Environment ####
# Description
# 	Stuff to run outside the virtual environment

# exit when any command fails
#set -um pipeline
set -u

export workdir=/tmp/{{.Pod.Namespace}}_{{.Pod.Name}}
echo "[Host] Creating workdir: ${workdir} "
mkdir -p ${workdir}

cleanup() {
    rm -rf "${workdir}"
}
trap cleanup EXIT

echo $$ > "${workdir}/.pid"

export APPTAINERENV_KUBEDNS_IP={{.HostEnv.KubeDNS}}

{{$.HostEnv.ApptainerBin}} exec --nv --scratch /scratch --workdir ${workdir} \
{{- if .HostEnv.EnableCgroupV2}}
--apply-cgroups {{.VirtualEnv.CgroupFilePath}} 		\
{{- end}}
--env PARENT=${PPID}								\
--bind /var/lib/hpk:/k8s-data			\
--bind /etc/apptainer/apptainer.conf				\
--bind $HOME,/tmp									\
--hostname {{truncate .Pod.Name 63}}							\
{{$.PauseImageFilePath}} /entrypoint.sh /usr/local/bin/hpk-pause -namespace {{.Pod.Namespace}} -pod {{.Pod.Name}} ||
echo "[HOST] **SYSTEMERROR** hpk-pause exited with code $?" | tee {{.VirtualEnv.SysErrorFilePath}}

#### END SECTION: Host Environment ####
`

// JobFields provide the inputs to HostScriptTemplate.
type JobFields struct {
	Pod types.NamespacedName

	// PauseImageFilePath contains the name of the image for the pause container.
	PauseImageFilePath string

	// VirtualEnv is the equivalent of a Pod.
	VirtualEnv compute.VirtualEnvironment

	HostEnv compute.HostEnvironment

	// Containers is a list of container requests to be executed.
	Containers []Container
}

// The Container creates new within the Pod and resemble the "Container" semantics.
type Container struct {
	// needed for apptainer start.
	InstanceName string // instance://podName_containerName

	// The UID to run the entrypoint of the container process.
	// May also be set in PodSecurityContext.  If set in both SecurityContext and
	// PodSecurityContext, the value specified in SecurityContext takes precedence.
	RunAsUser int64

	// The GID to run the entrypoint of the container process.
	// May also be set in PodSecurityContext.  If set in both SecurityContext and
	// PodSecurityContext, the value specified in SecurityContext takes precedence.
	RunAsGroup int64

	ImageFilePath string // format: REGISTRY://image:tag

	EnvFilePath string

	Binds []string

	Command []string

	Args []string // space separated args

	ExecutionMode string // exec or run

	// LogsPath instructs process to write stdout and stderr into the specified path.
	LogsPath string

	// JobIDPath points to the file where the process id of the container is stored.
	// This is used to know when the container has started.
	JobIDPath string

	// ExitCodePath is the path where the embedded Container command will write its exit code
	ExitCodePath string
}

// GenerateEnvTemplate is used to generate environment variables.
// This is needed for variables that consume information from the downward API (like .status.podIP)
const GenerateEnvTemplate = `#!/bin/bash

{{- range $index, $variable := .Variables}}
{{- if eq $variable.Value ".status.podIP"}}
printf '%s=%s\0' {{$variable.Name | param}} "$(ip route get 1 | sed -n 's/.*src \([0-9.]\+\).*/\1/p')"
{{ else }}
printf '%s=%s\0' {{$variable.Name | param}} {{$variable.Value | param}}
{{- end}}
{{- end}}
`

// GenerateEnvFields provide the inputs to GenerateEnvTemplate.
type GenerateEnvFields = struct {
	Variables []corev1.EnvVar
}
