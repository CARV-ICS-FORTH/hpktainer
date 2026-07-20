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
	compute.HPK = endpoint.HPK(compute.Environment.WorkingDirectory)

	// create the ~/.hpk directory, if it does not exist.
	if err := os.MkdirAll(compute.HPK.String(), endpoint.PodGlobalDirectoryPermissions); err != nil {
		return fmt.Errorf("Failed to create RuntimeDir '%s': %w", compute.HPK.String(), err)
	}

	// create the ~/.hpk/image directory, if it does not exist.
	if err := os.MkdirAll(compute.HPK.ImageDir(), endpoint.PodGlobalDirectoryPermissions); err != nil {
		return fmt.Errorf("Failed to create ImageDir '%s': %w", compute.HPK.ImageDir(), err)
	}

	// Sweep leftover temp image files from prior crashed pulls
	if err := image.CleanupTempFiles(compute.HPK.ImageDir()); err != nil {
		compute.DefaultLogger.Error(err, "failed to cleanup temp image files")
	}

	// create the ~/.hpk/corrupted directory, if it does not exist.
	if err := os.MkdirAll(compute.HPK.CorruptedDir(), endpoint.PodGlobalDirectoryPermissions); err != nil {
		return fmt.Errorf("Failed to create CorruptedDir '%s': %w", compute.HPK.CorruptedDir(), err)
	}

	img, err := image.Pull(compute.HPK.ImageDir(), image.Docker, pauseImage)
	if err != nil {
		return fmt.Errorf("failed to get pause container image: %w", err)
	}

	DefaultPauseImage = img

	compute.DefaultLogger.Info("Runtime info",
		"WorkingDirectory", compute.HPK.String(),
		"PauseImagePath", DefaultPauseImage,
	)

	return nil
}
