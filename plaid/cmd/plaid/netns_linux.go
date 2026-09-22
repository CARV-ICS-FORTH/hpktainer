//go:build linux

package main

import (
	"fmt"
	"os"
	"runtime"
	"syscall"

	"plaid/pkg/tap"
)

func createTapInNetNS(netnsPath string, cfg tap.TapConfig) (*os.File, error) {
	if netnsPath == "" {
		return tap.CreateAndConfigureTap(cfg)
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	curNS, err := os.Open("/proc/self/ns/net")
	if err != nil {
		return nil, fmt.Errorf("failed to open current netns: %w", err)
	}
	defer curNS.Close()

	targetNS, err := os.Open(netnsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open target netns %s: %w", netnsPath, err)
	}
	defer targetNS.Close()

	_, _, errno := syscall.Syscall(syscall.SYS_SETNS, targetNS.Fd(), uintptr(syscall.CLONE_NEWNET), 0)
	if errno != 0 {
		return nil, fmt.Errorf("setns to target netns failed: %w", errno)
	}

	tapFile, tapErr := tap.CreateAndConfigureTap(cfg)

	// Revert to original host netns
	_, _, _ = syscall.Syscall(syscall.SYS_SETNS, curNS.Fd(), uintptr(syscall.CLONE_NEWNET), 0)

	return tapFile, tapErr
}
