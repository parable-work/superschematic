// Command acme-schematic is the acme build of superschematic: the core with
// the acme extension linked. It knows the Catalog kind, the @shelf
// decorator, the catalog.config document, the acme manifest generator, the
// "apikey" auth provider and the describe subcommand; the core-only
// cmd/superschematic binary knows none of them.
package main

import (
	"fmt"
	"os"

	"github.com/parable-work/superschematic/cli"

	"example.com/acme/schematic/ext"
)

func main() {
	root := cli.New(cli.Config{
		Name:  "acme-schematic",
		Short: "Generate code from the acme schemas",
	}, ext.Extension{})
	if err := root.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
