// Package buildinfo identifies a product build independently of protocol versions.
package buildinfo

import "fmt"

// Set by scripts/build-go.sh, including for local installs and deployments.
var Version = "dev"
var Commit = "unknown"

func String() string { return fmt.Sprintf("%s (%s)", Version, Commit) }
