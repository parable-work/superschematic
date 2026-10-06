// Command superschematic-core is the core schema build system with no
// extension linked: cli.New(cli.Config{}). It is not the binary a release
// ships or a user installs. That is cmd/superschematic, a Go module of its
// own, which is the same call with the official extensions listed. This
// program stays for the checks that prove the core works with no extension
// linked (make cli-smoke, the examples' scripts) and for the commands that
// need only the core (make behaviors). It sits under internal/ so that no
// one takes it for a second binary to install.
package main

import (
	"fmt"
	"os"

	"github.com/parable-work/superschematic/cli"
)

func main() {
	if err := cli.New(cli.Config{}).Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
