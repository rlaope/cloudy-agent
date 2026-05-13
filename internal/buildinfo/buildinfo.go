// Package buildinfo carries linker-injected metadata about the build.
package buildinfo

// Version is set at build time via -ldflags. Defaults to "dev" for local builds.
var Version = "dev"
