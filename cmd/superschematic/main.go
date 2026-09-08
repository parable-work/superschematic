// Command psgen is the core schema build system with no extensions linked:
// it reads .schema.{ts,json,yaml} service directories and generates code
// artifacts. A binary that carries extensions is the same call with the
// extensions listed (see utils/parable-schematic/cmd/psgen).
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
