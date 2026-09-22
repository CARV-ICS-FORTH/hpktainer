//go:build !linux

package main

import (
	"os"

	"plaid/pkg/tap"
)

func createTapInNetNS(netnsPath string, cfg tap.TapConfig) (*os.File, error) {
	return tap.CreateAndConfigureTap(cfg)
}
