package schemadeps_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/schemadeps"
)

// Example_pinCommand is what an extension's pin command does with the graph
// build-all wrote, using only this package: fail when the committed copy is
// stale, take a consumer's closure, and write the consumer's pin file.
func Example_pinCommand() {
	repo, err := os.MkdirTemp("", "schemadeps-example")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = os.RemoveAll(repo) }()

	// What build-all leaves behind: <dist>/.deps.json and the [deps] copy.
	dist := filepath.Join(repo, "schemas", "dist")
	copyPath := filepath.Join(repo, "schemas", "deps.json")
	built := &schemadeps.Graph{Packages: []schemadeps.Package{
		{ID: "enums-types", Language: "go", Kind: "types", Name: "example.com/schemas/types/go/enums", Path: "types/go/enums", Service: "enums"},
		{ID: "orders-types", Language: "go", Kind: "types", Name: "example.com/schemas/types/go/orders", Path: "types/go/orders", Service: "orders", Deps: []string{"enums-types"}},
		{ID: "orders-sdk", Language: "go", Kind: "sdk", Name: "example.com/schemas/sdk/go/orders", Path: "sdk/go/orders", Service: "orders", Deps: []string{"orders-types"}},
	}}
	for _, path := range []string{schemadeps.DepsPath(dist), copyPath} {
		if err := schemadeps.Write(path, built); err != nil {
			fmt.Println(err)
			return
		}
	}

	// pin --check: the committed copy must match what was built.
	if err := schemadeps.SyncCopy(schemadeps.DepsPath(dist), copyPath, true); err != nil {
		fmt.Println(err)
		return
	}

	// pin: a Go consumer that imports the orders SDK gets a replace line for
	// every package in its closure, dependencies first.
	graph, err := schemadeps.Read(schemadeps.DepsPath(dist))
	if err != nil {
		fmt.Println(err)
		return
	}
	ids, err := graph.Closure("go", []string{"orders-sdk"})
	if err != nil {
		fmt.Println(err)
		return
	}
	consumer := filepath.Join(repo, "services", "checkout")
	byID := graph.ByLanguage("go")
	var pins strings.Builder
	for _, id := range ids {
		pkg := byID[id]
		rel, err := filepath.Rel(consumer, filepath.Join(dist, filepath.FromSlash(pkg.Path)))
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Fprintf(&pins, "replace %s => %s // from %s\n", pkg.Name, filepath.ToSlash(rel), pkg.Service)
	}
	pinPath := filepath.Join(consumer, "schema-deps.gomod")
	if err := os.MkdirAll(consumer, 0o755); err != nil {
		fmt.Println(err)
		return
	}
	if err := schemadeps.WriteFileAtomic(pinPath, []byte(pins.String()), 0o644); err != nil {
		fmt.Println(err)
		return
	}
	written, err := os.ReadFile(pinPath)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Print(string(written))
	// Output:
	// replace example.com/schemas/types/go/enums => ../../schemas/dist/types/go/enums // from enums
	// replace example.com/schemas/types/go/orders => ../../schemas/dist/types/go/orders // from orders
	// replace example.com/schemas/sdk/go/orders => ../../schemas/dist/sdk/go/orders // from orders
}
