// Command patchpilot-agent runs on a Linux server, reports its package state
// to PatchPilot and installs its pending updates when told to.
//
// So far it knows its own version and nothing else.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(stdout, version)
		return 0
	}
	fmt.Fprintln(stderr, "usage: patchpilot-agent --version")
	return 2
}
