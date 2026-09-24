package version

var (
	// Version is the current version of Plaid. Set at build time via -ldflags.
	Version = "0.1.0"

	// BuildTime is the UTC timestamp when the binary was built. Set at build time via -ldflags.
	BuildTime = "N/A"

	// GitCommit is the git commit hash at build time. Set at build time via -ldflags.
	GitCommit = "N/A"
)
