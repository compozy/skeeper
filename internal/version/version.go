package version

import "fmt"

var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

// Info captures the build metadata in a typed form.
type Info struct {
	Version   string
	Commit    string
	BuildDate string
}

// Get returns the current build metadata.
func Get() Info {
	return Info{Version: Version, Commit: Commit, BuildDate: BuildDate}
}

// String formats the build metadata for human-readable output (e.g. `app version`).
func String() string {
	return fmt.Sprintf("%s (commit=%s date=%s)", Version, Commit, BuildDate)
}
