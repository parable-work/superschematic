// Command tooldigest is the core superschematic binary with
// cli.Config.ToolDigest read from SUPERSCHEMATIC_TEST_TOOL_DIGEST. The tool
// digest test links it twice with different -X main.checkout values, so the
// two executables differ byte for byte the way builds from two checkouts do.
package main

import (
	"fmt"
	"os"

	"github.com/parable-work/superschematic/cli"
)

// checkout names the build; -X sets it so each build's bytes differ.
var checkout = "none"

func main() {
	root := cli.New(cli.Config{
		Short:      "superschematic built in checkout " + checkout,
		ToolDigest: os.Getenv("SUPERSCHEMATIC_TEST_TOOL_DIGEST"),
	})
	if err := root.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
