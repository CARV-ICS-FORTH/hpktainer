package podhandler_test

import (
	"os"
	"testing"

	"hpk/internal/compute"
	"hpk/internal/compute/runtime"
)

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "randomuser")
	if err != nil {
		panic(err)
	}

	setup(tmpDir)
	code := m.Run()
	shutdown(tmpDir)
	os.Exit(code)
}

func setup(tmpDir string) {
	compute.Environment = compute.HostEnvironment{
		KubeMasterHost:    "",
		ContainerRegistry: "",
		ApptainerBin:      "apptainer",
		EnableCgroupV2:    false,
		WorkingDirectory:  tmpDir,
		KubeDNS:           "",
	}

	if err := runtime.Initialize("docker.io/chazapis/hpk-pause:latest"); err != nil {
		compute.DefaultLogger.Info("runtime.Initialize non-fatal warning", "err", err)
	}
}

func shutdown(tmpDir string) {
	os.RemoveAll(tmpDir)
}
