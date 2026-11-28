// Command behaviorconfigtypes writes the config schema of each behavior an
// engine runs as a TypeScript type, into the @superschematic/schema
// package:
//
//	packages/schema/src/behavior-config-schemas.ts
//
// Each <Name>ConfigSchema is the behavior's configSchema
// (internal/registry/behaviors/<name>.behavior.json) as
// behaviortypes.TSRenderer writes it, readonly as the authoring types are,
// and BehaviorConfigSchemas keys them by name. They are the table's form:
// every member the schema accepts, a table's alone among them. The types a
// schema's author writes against stay in behavior.ts, by hand: they are an
// engine type's form, which leaves out what an engine type refuses, and
// say in TypeScript what the schema says with if, prefixItems and the
// engine's checks. behavior-config.typecheck.ts holds the two to each
// other, so `bun run typecheck` in packages fails when a behavior's
// configSchema moves and behavior.ts does not.
//
// The behaviors are the core's that an engine implements
// (registry.EngineImplements), less those D103 folds into a schema feature
// (verify.FoldedBehaviors): the names BehaviorConfigs lists.
//
// Run from the repository root: go run ./internal/tools/behaviorconfigtypes
// With -check the command exits 1 when the committed file differs from what
// it would write, which is how CI keeps it in step with the declarations.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/registry"
)

const outputPath = "packages/schema/src/behavior-config-schemas.ts"

func main() {
	check := flag.Bool("check", false, "exit 1 when the committed file differs from the generated one")
	flag.Parse()

	root, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	out, err := render()
	if err != nil {
		fail(err)
	}
	path := filepath.Join(root, outputPath)
	if *check {
		have, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(have, out) {
			fmt.Fprintf(os.Stderr, "%s is out of date; run: go run ./internal/tools/behaviorconfigtypes\n", outputPath)
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		fail(err)
	}
}

// engineBehaviors are the core's behaviors BehaviorConfigs lists, in the
// registry's order.
func engineBehaviors() []registry.Behavior {
	var out []registry.Behavior
	for _, b := range registry.New(naming.Naming{}).Behaviors() {
		if b.Extension != "" || !registry.EngineImplements(b) || verify.IsFoldedBehavior(b.Name) {
			continue
		}
		out = append(out, b)
	}
	return out
}

// render returns the file's content.
func render() ([]byte, error) {
	behaviors := engineBehaviors()
	r := behaviortypes.TSRenderer{Readonly: true}
	var types strings.Builder
	for _, b := range behaviors {
		rendered := "undefined"
		if len(b.ConfigSchema) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(b.ConfigSchema))
			decoder.UseNumber()
			var schema any
			if err := decoder.Decode(&schema); err != nil {
				return nil, fmt.Errorf("behavior %s: configSchema: %w", b.Name, err)
			}
			rendered = r.Render(schema, "")
		}
		fmt.Fprintf(&types, "\n/** %s configSchema. */\nexport type %sConfigSchema = %s;\n", possessive(b.Name), b.Name, rendered)
	}

	var b strings.Builder
	b.WriteString(`// @generated; do not edit
// The config schema of each behavior an engine runs, as TypeScript: the
// configSchema of its declaration (internal/registry/behaviors in the
// superschematic repository), every member it accepts, a table's alone
// among them. behavior.ts holds the types an engine type's author writes
// against, and behavior-config.typecheck.ts holds them to these.
// Regenerate with:
//   go run ./internal/tools/behaviorconfigtypes
`)
	if r.UsesJSONObject {
		b.WriteString("\n/** A JSON object whose members the schema does not name. */\n")
		fmt.Fprintf(&b, "export type %s = { readonly [key: string]: unknown };\n", behaviortypes.JSONObjectType)
	}
	b.WriteString(types.String())
	b.WriteString("\n/** Each behavior's config schema, by the behavior's name. */\nexport interface BehaviorConfigSchemas {\n")
	for _, behavior := range behaviors {
		fmt.Fprintf(&b, "  %s: %sConfigSchema;\n", behaviortypes.TSPropertyKey(behavior.Name), behavior.Name)
	}
	b.WriteString("}\n")
	return []byte(b.String()), nil
}

// possessive writes a name's possessive: Comments', Workflow's.
func possessive(name string) string {
	if strings.HasSuffix(name, "s") {
		return name + "'"
	}
	return name + "'s"
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
