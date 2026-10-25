// Command superschematic-topcoat is the installed binary with the Topcoat
// extension linked: every command of superschematic, every official
// extension it links (cmd/superschematic/official), and outputs.topcoat
// for an API service whose server is Rust. It builds a stack whose app
// names the topcoat extension (D78), which superschematic refuses to
// build, wherever that stack deploys, and so a project's whole services
// root, the generated CI's included. A project's own binary links the
// extension beside its others the same way:
// cli.New(cli.Config{Name: "superschematic-topcoat"}, append(official.Extensions(), topcoat.Extension{})...).
package main

import (
	"github.com/parable-work/superschematic/cli"
	"github.com/parable-work/superschematic/cmd/superschematic/official"
	"github.com/parable-work/superschematic/extensions/topcoat"
)

func main() {
	if err := cli.New(cli.Config{Name: "superschematic-topcoat"}, append(official.Extensions(), topcoat.Extension{})...).Execute(); err != nil {
		cli.Exit(err)
	}
}
