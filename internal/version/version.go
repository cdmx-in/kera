// Package version holds build metadata set with -ldflags.
package version

// Version is overridden at build time: -ldflags "-X github.com/cdmx-in/kera/internal/version.Version=v0.1.0".
var Version = "dev"
