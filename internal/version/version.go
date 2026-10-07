// Package version exposes build information set via -ldflags.
package version

// Set at build time: -ldflags "-X zoom-checker/internal/version.Version=1.0.0 -X zoom-checker/internal/version.Commit=abc123".
var (
	Version = "dev"
	Commit  = ""
)

// String returns "1.0.0 (abc123)" or just the version.
func String() string {
	if Commit == "" {
		return Version
	}
	return Version + " (" + Commit + ")"
}
