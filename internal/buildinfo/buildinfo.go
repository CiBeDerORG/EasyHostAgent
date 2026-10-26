// Package buildinfo holds values injected by -ldflags during CI builds.
package buildinfo

import "fmt"

// These values are safe to publish; never put tokens or credentials here.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func String() string {
	return fmt.Sprintf("EasyHostAgent %s (commit=%s, built=%s)", Version, Commit, Date)
}
