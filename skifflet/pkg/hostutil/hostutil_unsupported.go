//go:build !linux

package hostutil

import (
	utilpath "k8s.io/utils/path"
)

// HostUtil implements HostUtils for non-Linux platforms.
type HostUtil struct{}

var _ HostUtils = &HostUtil{}

// NewHostUtil returns a struct that implements the HostUtils interface.
func NewHostUtil() *HostUtil {
	return &HostUtil{}
}

// GetFileType checks for file/directory/socket/block/character devices.
func (hu *HostUtil) GetFileType(pathname string) (FileType, error) {
	return getFileType(pathname)
}

// PathExists tests if the given path already exists.
func (hu *HostUtil) PathExists(pathname string) (bool, error) {
	return utilpath.Exists(utilpath.CheckFollowSymlink, pathname)
}
