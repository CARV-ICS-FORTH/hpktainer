package image_test

import (
	"os"
	"path/filepath"
	"testing"

	"hpk/internal/compute"
	"hpk/internal/compute/runtime"
)

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "randomuser")
	if err != nil {
		panic(err)
	}

	if err := setup(tmpDir); err != nil {
		compute.DefaultLogger.Info("Skipping image package tests: runtime.Initialize failed", "err", err)
		shutdown(tmpDir)
		os.Exit(0)
	}
	code := m.Run()
	shutdown(tmpDir)
	os.Exit(code)
}

func setup(tmpDir string) error {
	compute.Environment = compute.HostEnvironment{
		KubeMasterHost:    "",
		ContainerRegistry: "",
		ApptainerBin:      "apptainer",
		WorkingDirectory:  tmpDir,
		PodsDirectory:     filepath.Join(tmpDir, ".hpk", ".pods"),
		KubeDNS:           "",
	}

	return runtime.Initialize("docker.io/chazapis/hpk-pause:latest")
}

func shutdown(tmpDir string) {
	os.RemoveAll(tmpDir)
}
