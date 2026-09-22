//go:build !linux

package hostutil

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/utils/mount"
)

// SafeMakeDir creates a directory for non-linux platforms.
func SafeMakeDir(subdir string, base string, perm os.FileMode) error {
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return fmt.Errorf("error resolving symlinks in %s: %w", base, err)
	}

	fullPath := filepath.Join(realBase, subdir)
	if !mount.PathWithinBase(fullPath, realBase) {
		return fmt.Errorf("path %s is outside allowed base %s", fullPath, realBase)
	}

	return os.MkdirAll(fullPath, perm)
}
