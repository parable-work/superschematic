// Command scalarcatalog writes the scalar catalogs the TypeScript and Python
// schema runtimes ship, from the superscalar Go package this repository
// links. The Go runtime reads scalars.ScalarMetadataByCanonical directly;
// the other two runtimes cannot import Go, so the same rows are written out
// once here and committed:
//
//	runtime/schema/typescript/src/runtime/builtin-scalars.generated.ts
//	runtime/schema/python/superschematic_schema_runtime/_generated_default_registry.py
//
// The TypeScript catalog also gives each builtin scalar its value class
// (D19, D32): the class the graph descriptor gives a single field of the
// scalar, computed by graphdesc.ScalarClass over the scalar as the loader
// hydrates it from the core catalog, so the engine classifies a field
// with the compiler's rule. The Python catalog holds no scalar rows, only
// the parse, normalize and validate functions, and gains nothing.
//
// Run from the repository root: go run ./internal/tools/scalarcatalog
// With -check the command exits 1 when a committed file differs from what it
// would write, which is how CI keeps the catalogs in step with the pinned
// superscalar commit.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	scalars "github.com/parable-work/superscalar/go"

	"github.com/parable-work/superschematic/internal/generator/graphdesc"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

const (
	tsPath = "runtime/schema/typescript/src/runtime/builtin-scalars.generated.ts"
	pyPath = "runtime/schema/python/superschematic_schema_runtime/_generated_default_registry.py"
)

// entry is the runtime ScalarDef shape (ir/typescript/index.d.ts ScalarDef).
// Every key is present so the emitted literal satisfies the TypeScript
// interface without optional fields.
type entry struct {
	Name                         string            `json:"name"`
	Description                  string            `json:"description"`
	Primitive                    string            `json:"primitive"`
	MinLength                    int               `json:"minLength"`
	MaxLength                    int               `json:"maxLength"`
	Pattern                      string            `json:"pattern"`
	Format                       string            `json:"format"`
	ReservedWords                []string          `json:"reservedWords"`
	CaseInsensitive              bool              `json:"caseInsensitive"`
	ReservedWordsCaseInsensitive bool              `json:"reservedWordsCaseInsensitive"`
	ReservedWordsMatchPartial    bool              `json:"reservedWordsMatchPartial"`
	Minimum                      *int64            `json:"minimum"`
	Maximum                      *int64            `json:"maximum"`
	Example                      string            `json:"example"`
	FileUpload                   *struct{}         `json:"fileUpload"`
	ImageConstraints             *struct{}         `json:"imageConstraints"`
	HasCustomNormalize           bool              `json:"hasCustomNormalize"`
	HasCustomValidate            bool              `json:"hasCustomValidate"`
	HasCustomParse               bool              `json:"hasCustomParse"`
	TypeMappings                 map[string]string `json:"typeMappings"`
}

func main() {
	check := flag.Bool("check", false, "exit 1 when a committed catalog differs from the generated one")
	flag.Parse()

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	names := make([]string, 0, len(scalars.ScalarMetadataByCanonical))
	for name := range scalars.ScalarMetadataByCanonical {
		names = append(names, name)
	}
	sort.Strings(names)
	classes, err := valueClasses(names)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	outputs := map[string][]byte{
		tsPath: renderTS(names, classes),
		pyPath: renderPython(names),
	}
	failed := false
	for rel, want := range outputs {
		path := filepath.Join(root, rel)
		if *check {
			have, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(have, want) {
				fmt.Fprintf(os.Stderr, "%s is out of date; run: go run ./internal/tools/scalarcatalog\n", rel)
				failed = true
			}
			continue
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if failed {
		os.Exit(1)
	}
}

// valueClasses is the value class graphdesc gives a single field of each
// named scalar (graphdesc.ScalarClass), each hydrated from the core catalog
// as a load hydrates a scalar a schema names (loader.HydrateScalars). A
// scalar no class reads is left out.
func valueClasses(names []string) (map[string]string, error) {
	schema := &ir.Schema{Scalars: make(map[string]*ir.ScalarDef, len(names))}
	for _, name := range names {
		schema.Scalars[name] = &ir.ScalarDef{Name: name}
	}
	if err := loader.HydrateScalars(schema, registry.CoreScalars()); err != nil {
		return nil, fmt.Errorf("hydrate the builtin scalars: %w", err)
	}
	classes := make(map[string]string, len(names))
	for _, name := range names {
		class, err := graphdesc.ScalarClass(schema, name)
		if err != nil {
			return nil, fmt.Errorf("the value class of %s: %w", name, err)
		}
		if class != "" {
			classes[name] = class
		}
	}
	return classes, nil
}

func renderTS(names []string, classes map[string]string) []byte {
	var b bytes.Buffer
	b.WriteString("// @generated; do not edit\n")
	b.WriteString("// Builtin scalar catalog: one row per scalar in the superscalar Go package\n")
	b.WriteString("// this repository pins (superscalar.pin). Regenerate with:\n")
	b.WriteString("//   go run ./internal/tools/scalarcatalog\n")
	b.WriteString("import type { ScalarDef } from './validation/types';\n\n")
	b.WriteString("export const BUILTIN_SCALARS: Record<string, ScalarDef> = {\n")
	for i, name := range names {
		meta := scalars.ScalarMetadataByCanonical[name]
		row := entry{
			Name:               strings.ReplaceAll(name, ".", "_"),
			Description:        meta.Description,
			Primitive:          meta.Primitive,
			MinLength:          meta.MinLength,
			MaxLength:          meta.MaxLength,
			Pattern:            meta.Pattern,
			Format:             meta.Format,
			ReservedWords:      []string{},
			Minimum:            meta.Minimum,
			Maximum:            meta.Maximum,
			HasCustomNormalize: meta.HasCustomNormalize,
			HasCustomValidate:  meta.HasCustomValidate,
			HasCustomParse:     meta.HasCustomParse,
			TypeMappings:       map[string]string{},
		}
		if len(meta.Examples) > 0 {
			row.Example = meta.Examples[0]
		}
		if meta.Symbol != "" {
			row.TypeMappings["go"] = meta.Symbol
		}
		if isObjectPrimitive(meta.Primitive) && meta.GoType != "" {
			row.TypeMappings["typescript"] = meta.GoType
		}
		if meta.SQLType != "" {
			row.TypeMappings["sql"] = meta.SQLType
		}
		if meta.JSONSchemaType != "" {
			row.TypeMappings["json_schema"] = meta.JSONSchemaType
		}
		encoded, err := json.MarshalIndent(row, "  ", "  ")
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(&b, "  %q: %s", row.Name, encoded)
		if i < len(names)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("};\n")
	b.WriteString("\n")
	b.WriteString("// The value class (D19) the graph descriptor gives a single field of each\n")
	b.WriteString("// builtin scalar: the class of one value of it stored in a column of its\n")
	b.WriteString("// own (graphdesc.ScalarClass), keyed as BUILTIN_SCALARS is. A scalar no\n")
	b.WriteString("// class reads is not here.\n")
	b.WriteString("export const BUILTIN_SCALAR_VALUE_CLASSES: Record<string, string> = {\n")
	classified := make([]string, 0, len(classes))
	for _, name := range names {
		if _, ok := classes[name]; ok {
			classified = append(classified, name)
		}
	}
	for i, name := range classified {
		fmt.Fprintf(&b, "  %q: %q", strings.ReplaceAll(name, ".", "_"), classes[name])
		if i < len(classified)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("};\n")
	return b.Bytes()
}

// isObjectPrimitive mirrors the loader's languagePrimitiveFromScalarMetadata
// for the one case that matters here: object-valued scalars carry their Go
// type as the TypeScript mapping.
func isObjectPrimitive(primitive string) bool {
	switch strings.ToLower(strings.TrimSpace(primitive)) {
	case "string", "str", "number", "float", "float64", "int", "int32", "int64", "integer", "bool", "boolean":
		return false
	}
	return true
}

func renderPython(names []string) []byte {
	var parse, normalize []string
	for _, name := range names {
		meta := scalars.ScalarMetadataByCanonical[name]
		if meta.HasCustomParse {
			parse = append(parse, name)
		}
		if meta.HasCustomNormalize {
			normalize = append(normalize, name)
		}
	}
	var b bytes.Buffer
	b.WriteString("# @generated; do not edit\n")
	b.WriteString("# Default scalar registries for the schema runtime: one row per scalar in\n")
	b.WriteString("# the superscalar Go package this repository pins (superscalar.pin).\n")
	b.WriteString("# Regenerate with: go run ./internal/tools/scalarcatalog\n\n")
	b.WriteString("from __future__ import annotations\n\n")
	b.WriteString("from typing import Callable\n\n")
	b.WriteString("from superscalar import VALID_SCALARS, ValidationError, _native\n")
	imports := map[string]bool{}
	for _, name := range parse {
		imports["parse_"+snake(scalars.ScalarMetadataByCanonical[name].Symbol)] = true
	}
	for _, name := range normalize {
		imports["normalize_"+snake(scalars.ScalarMetadataByCanonical[name].Symbol)] = true
	}
	sortedImports := make([]string, 0, len(imports))
	for name := range imports {
		sortedImports = append(sortedImports, name)
	}
	sort.Strings(sortedImports)
	b.WriteString("from superscalar import (\n")
	for _, name := range sortedImports {
		fmt.Fprintf(&b, "    %s,\n", name)
	}
	b.WriteString(")\n\n\n")
	b.WriteString("def _validator(canonical_name: str) -> Callable[[str], list]:\n")
	b.WriteString("    if canonical_name not in VALID_SCALARS:\n")
	b.WriteString("        raise KeyError(canonical_name)\n\n")
	b.WriteString("    def validate(value: str) -> list:\n")
	b.WriteString("        try:\n")
	b.WriteString("            _native.validate(canonical_name, value)\n")
	b.WriteString("        except ValueError as exc:\n")
	b.WriteString("            return [ValidationError(validator=\"custom\", message=str(exc))]\n")
	b.WriteString("        return []\n\n")
	b.WriteString("    return validate\n\n\n")
	b.WriteString("DEFAULT_PARSE_FUNCTIONS: dict[str, Callable[[str], str]] = {\n")
	for _, name := range parse {
		fmt.Fprintf(&b, "    %q: parse_%s,\n", name, snake(scalars.ScalarMetadataByCanonical[name].Symbol))
	}
	b.WriteString("}\n\n\n")
	b.WriteString("DEFAULT_NORMALIZE_FUNCTIONS: dict[str, Callable[[str], str]] = {\n")
	for _, name := range normalize {
		fmt.Fprintf(&b, "    %q: normalize_%s,\n", name, snake(scalars.ScalarMetadataByCanonical[name].Symbol))
	}
	b.WriteString("}\n\n\n")
	b.WriteString("DEFAULT_VALIDATE_FUNCTIONS: dict[str, Callable[[str], list]] = {\n")
	for _, name := range names {
		fmt.Fprintf(&b, "    %q: _validator(%q),\n", name, name)
	}
	b.WriteString("}\n\n\n")
	b.WriteString("__all__ = [\n")
	b.WriteString("    \"DEFAULT_PARSE_FUNCTIONS\",\n")
	b.WriteString("    \"DEFAULT_NORMALIZE_FUNCTIONS\",\n")
	b.WriteString("    \"DEFAULT_VALIDATE_FUNCTIONS\",\n")
	b.WriteString("]\n")
	return b.Bytes()
}

// snake is superscalar's symbol-to-module rule (crates/codegen to_snake):
// TemporalDateTime -> temporal_date_time, CryptoRSAPrivateKey ->
// crypto_rsa_private_key, IdentityUUID -> identity_uuid.
func snake(symbol string) string {
	runes := []rune(symbol)
	var out strings.Builder
	for i, ch := range runes {
		if ch >= 'A' && ch <= 'Z' {
			var prev, next rune
			if i > 0 {
				prev = runes[i-1]
			}
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			afterLower := (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9')
			beforeWord := prev >= 'A' && prev <= 'Z' && next >= 'a' && next <= 'z'
			if i != 0 && (afterLower || beforeWord) {
				out.WriteByte('_')
			}
			out.WriteRune(ch + ('a' - 'A'))
			continue
		}
		out.WriteRune(ch)
	}
	return out.String()
}
