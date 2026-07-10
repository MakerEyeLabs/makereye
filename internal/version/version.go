// Package version holds build-time version information, set via
// -ldflags at build time (see Makefile). Defaults are used for
// unreleased/development builds.
package version

var (
	// Version is the MakerEye release version, e.g. "0.1.0". Set with
	// -X github.com/MakerEyeLabs/makereye/internal/version.Version=...
	Version = "dev"
	// Commit is the short git commit hash MakerEye was built from.
	Commit = "unknown"
	// BuildDate is the UTC build timestamp in RFC3339 form.
	BuildDate = "unknown"
)

// String returns a single-line human-readable version string.
func String() string {
	return Version + " (commit " + Commit + ", built " + BuildDate + ")"
}
