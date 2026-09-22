package version

var (
	// Version is the current version of the application.
	// It is set at build time via -ldflags.
	Version = "N/A"

	// BuildTime is the time when the application was built.
	// It is set at build time via -ldflags.
	BuildTime = "N/A"

	// K8sVersion is the version of the kubernetes libraries used.
	// It is set at build time via -ldflags.
	K8sVersion = "N/A"
)
