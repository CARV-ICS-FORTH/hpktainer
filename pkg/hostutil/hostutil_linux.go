/*
Copyright 2014 The Kubernetes Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hostutil

import (
	utilpath "k8s.io/utils/path"
)

// HostUtil implements HostUtils for Linux platforms.
type HostUtil struct{}

var _ HostUtils = &HostUtil{}

// NewHostUtil returns a struct that implements the HostUtils interface on
// linux platforms
func NewHostUtil() *HostUtil {
	return &HostUtil{}
}

// GetFileType checks for file/directory/socket/block/character devices.
func (hu *HostUtil) GetFileType(pathname string) (FileType, error) {
	return getFileType(pathname)
}

// PathExists tests if the given path already exists
// Error is returned on any other error than "file not found".
func (hu *HostUtil) PathExists(pathname string) (bool, error) {
	return utilpath.Exists(utilpath.CheckFollowSymlink, pathname)
}
