// Command upool keeps a per-project pool of pre-warmed Unity worktrees and leases them to agents.
package main

import "fmt"

// version is set at build time with -ldflags "-X main.version=<version>".
var version = "dev"

func main() {
	fmt.Println("upool", version)
}
