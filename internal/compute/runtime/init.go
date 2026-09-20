package runtime

import (
	"fmt"
	"os"

	"hpk/internal/compute"
	"hpk/internal/compute/endpoint"
	"hpk/internal/compute/image"
)

var (
	// DefaultPauseImage is an actionable object of the pause container.
	DefaultPauseImage *image.Image
)

func Initialize(pauseImage string) error {
	if compute.Environment.PodsDirectory != "" {
		compute.HPK = endpoint.HPKWithPods(compute.Environment.WorkingDirectory, compute.Environment.PodsDirectory)
	} else {
		compute.HPK = endpoint.HPK(compute.Environment.WorkingDirectory)
	}

	// create the ~/.hpk directory, if it does not exist.
	if err := os.MkdirAll(compute.HPK.String(), endpoint.PodGlobalDirectoryPermissions); err != nil {
		return fmt.Errorf("Failed to create RuntimeDir '%s': %w", compute.HPK.String(), err)
	}

	// create the ~/.hpk/image directory, if it does not exist.
	if err := os.MkdirAll(compute.HPK.ImageDir(), endpoint.PodGlobalDirectoryPermissions); err != nil {
		return fmt.Errorf("Failed to create ImageDir '%s': %w", compute.HPK.ImageDir(), err)
	}

	// create the /tmp/.hpk/.pods directory, if it does not exist.
	if err := os.MkdirAll(compute.HPK.PodsDir(), endpoint.PodGlobalDirectoryPermissions); err != nil {
		return fmt.Errorf("Failed to create PodsDir '%s': %w", compute.HPK.PodsDir(), err)
	}

	// Sweep leftover temp image files from prior crashed pulls
	if err := image.CleanupTempFiles(compute.HPK.ImageDir()); err != nil {
		compute.DefaultLogger.Error(err, "failed to cleanup temp image files")
	}

	img, err := image.Pull(compute.HPK.ImageDir(), image.Docker, pauseImage)
	if err != nil {
		return fmt.Errorf("failed to get pause container image: %w", err)
	}

	DefaultPauseImage = img

	compute.DefaultLogger.Info("Runtime info",
		"WorkingDirectory", compute.HPK.String(),
		"PodsDirectory", compute.HPK.PodsDir(),
		"PauseImagePath", DefaultPauseImage,
	)

	return nil
}
