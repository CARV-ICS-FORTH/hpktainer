// Copyright © 2023 FORTH-ICS
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

package endpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	PodGlobalDirectoryPermissions = os.FileMode(0o777)
	PodSpecJsonFilePermissions    = os.FileMode(0o600)
	ContainerJobPermissions       = os.FileMode(0o777)
)

// Pod-Related Extensions
const (
	// ExtensionCRD describes the file where HPK will write the pod definition.
	ExtensionCRD = ".crd"

	// ExtensionStdout describes the file where container execution will write its stdout.
	ExtensionStdout = ".stdout"

	// ExtensionStderr describes the file where container execution will write its stderr.
	ExtensionStderr = ".stderr"
)

// Container-Related Extensions
const (
	// ExtensionEnvironment describes the file where the environment variables for the container are held.
	ExtensionEnvironment = ".env"

	// ExtensionLogs describes the file where container execution will write its logs.
	ExtensionLogs = ".logs"
)

type HPKPath string

func HPK(rootPath string) HPKPath {
	return HPKPath(filepath.Join(rootPath, ".hpk"))
}

func (p HPKPath) String() string {
	if p == "" {
		panic("HPK path has not been initialized")
	}

	return string(p)
}

func (p HPKPath) ImageDir() string {
	return filepath.Join(string(p), ".images")
}

func (p HPKPath) CorruptedDir() string {
	return filepath.Join(string(p), ".corrupted")
}

func (p HPKPath) ApptainerDir() string {
	return filepath.Join(string(p), ".apptainer")
}

type WalkPodFunc func(path PodPath) error

func (p HPKPath) WalkPodDirectories(f WalkPodFunc) error {
	maxDepth := strings.Count(p.String(), string(os.PathSeparator)) + 2 // expect path .hpk/namespace/pod

	return filepath.WalkDir(p.String(), func(path string, info os.DirEntry, err error) error {
		// check for traversing errors
		if err != nil {
			return fmt.Errorf("Pod traversal error: %w", err)
		}

		// skip files
		if !info.IsDir() {
			return nil
		}

		// skip hidden system paths starting with . (e.g. .certs, .images, .corrupted, .apptainer, .tls)
		if path != p.String() && strings.HasPrefix(info.Name(), ".") {
			return filepath.SkipDir
		}

		// pod directory is found
		depth := strings.Count(path, string(os.PathSeparator))
		switch {
		case depth < maxDepth: // pod's namespace
			return nil
		case depth == maxDepth: // pod directory
			return f(PodPath(path))
		default: // pod contents
			return filepath.SkipDir
		}
	})
}

func (p HPKPath) Pod(podRef client.ObjectKey) PodPath {
	path := filepath.Join(p.String(), podRef.Namespace, podRef.Name)

	return PodPath(path)
}

type PodPath string

func (p PodPath) String() string {
	return string(p)
}

// PodEnvironmentIsOK checks if the pod structure is ok.
func (p PodPath) PodEnvironmentIsOK() (bool, string) {
	if _, err := os.Open(p.EncodedJSONPath()); err != nil {
		return false, "no pod specification was found"
	}

	return true, ""
}

func (p PodPath) JobDir() string {
	return filepath.Join(string(p), "job")
}

func (p PodPath) VolumeDir() string {
	return filepath.Join(string(p), "volumes")
}

func (p PodPath) LogDir() string {
	return filepath.Join(string(p), "logs")
}

func (p PodPath) EncodedJSONPath() string {
	return filepath.Join(p.JobDir(), "pod"+ExtensionCRD)
}

func (p PodPath) CgroupFilePath() string {
	return filepath.Join(p.JobDir(), "cgroup.toml")
}

func (p PodPath) StdoutPath() string {
	return filepath.Join(p.LogDir(), ExtensionStdout)
}

func (p PodPath) StderrPath() string {
	return filepath.Join(p.LogDir(), ExtensionStderr)
}

func (p PodPath) Container(containerName string) ContainerPath {
	return ContainerPath{
		p:             p,
		containerName: containerName,
	}
}

type ContainerPath struct {
	p             PodPath
	containerName string
}

func (c ContainerPath) LogsPath() string {
	return filepath.Join(c.p.LogDir(), c.containerName+ExtensionLogs)
}

func (c ContainerPath) EnvFilePath() string {
	return filepath.Join(c.p.JobDir(), c.containerName+ExtensionEnvironment)
}
