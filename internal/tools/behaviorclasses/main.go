// Command behaviorclasses derives the declarations of the core's behaviors
// that a class declares (D103) from their classes:
//
//	internal/registry/behaviors/<name>.behavior.ts   the class, the source
//	internal/registry/behaviors/<name>.behavior.json the declaration the
//	                                                 registry embeds and
//	                                                 `make behaviors` copies
//	                                                 into each crate
//
// A class is read by the TypeScript reader (tsreader.ReadBehaviorClasses)
// and its declaration compared with the committed one in their canonical
// form (registry.CanonicalDeclaration): the same declaration leaves the
// file as it is, whatever its layout, and another is written in the
// canonical form. A behavior no class declares keeps its file.
//
// Run from the repository root, with superscalar's checkout in
// third_party (the classes' tsconfig.json resolves superscalar there):
// go run ./internal/tools/behaviorclasses. With -check nothing is written:
// the command exits 1 when a class derives a declaration its file does not
// hold, which is how CI keeps the two in step. `make behaviors` runs it
// before it writes the crates' copies.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/loader/tsreader"
	"github.com/parable-work/superschematic/internal/registry"
)

const behaviorsDir = "internal/registry/behaviors"

func main() {
	check := flag.Bool("check", false, "exit 1 when a class derives a declaration its file does not hold")
	flag.Parse()
	classes, err := tsreader.ReadBehaviorClasses(behaviorsDir, nil)
	if err != nil {
		fail(err)
	}
	stale := 0
	for _, class := range classes {
		derived, err := registry.CanonicalDeclaration(class.Declaration)
		if err != nil {
			fail(fmt.Errorf("%s: %w", class.File, err))
		}
		file := filepath.Join(behaviorsDir, strings.TrimSuffix(class.File, tsreader.BehaviorClassSuffix)+".behavior.json")
		committed, err := canonicalFile(file)
		if err != nil && !os.IsNotExist(err) {
			fail(err)
		}
		if bytes.Equal(derived, committed) {
			continue
		}
		stale++
		if *check {
			fmt.Fprintf(os.Stderr, "behaviorclasses: %s derives a declaration %s does not hold; run: go run ./internal/tools/behaviorclasses\n", filepath.Join(behaviorsDir, class.File), file)
			continue
		}
		if err := os.WriteFile(file, derived, 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("behaviorclasses: wrote %s\n", file)
	}
	if *check && stale > 0 {
		os.Exit(1)
	}
	fmt.Printf("behaviorclasses: %d class(es) in %s derive their declarations\n", len(classes), behaviorsDir)
}

// canonicalFile is the canonical form of the declaration file name.
func canonicalFile(name string) ([]byte, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var decl registry.BehaviorDeclaration
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decl); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return registry.CanonicalDeclaration(decl)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "behaviorclasses:", err)
	os.Exit(1)
}
