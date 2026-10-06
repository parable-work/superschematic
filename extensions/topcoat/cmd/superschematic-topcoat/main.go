// Command superschematic-topcoat is the core build with the Topcoat
// extension linked: every command of superschematic, and outputs.topcoat
// for an API service whose server is Rust. It is the program
// cli.New(cli.Config{Name: "superschematic-topcoat"}, topcoat.Extension{})
// and nothing else, so a project's own binary can link the extension
// beside its others the same way.
package main

import (
	"fmt"
	"os"

	"github.com/parable-work/superschematic/cli"
	"github.com/parable-work/superschematic/extensions/topcoat"
)

func main() {
	if err := cli.New(cli.Config{Name: "superschematic-topcoat"}, topcoat.Extension{}).Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
