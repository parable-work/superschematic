// Command scalarcatalog writes the scalar catalogs the TypeScript and Python
// schema runtimes ship, from the superscalar Go package this repository
// links. The Go runtime reads scalars.ScalarMetadataByCanonical directly;
// the other two runtimes cannot import Go, so the same rows are written out
// once here and committed:
//
//	runtime/schema/typescript/src/runtime/builtin-scalars.generated.ts
//	runtime/schema/python/superschematic_schema_runtime/_generated_default_registry.py
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

	outputs := map[string][]byte{
		tsPath: renderTS(names),
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

func renderTS(names []string) []byte {
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
	b.WriteString("from superscalar import SCALAR_ID_BY_CANONICAL, ValidationError, _native\n")
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
	b.WriteString("    scalar_id = SCALAR_ID_BY_CANONICAL[canonical_name]\n\n")
	b.WriteString("    def validate(value: str) -> list:\n")
	b.WriteString("        try:\n")
	b.WriteString("            _native.validate(scalar_id, value)\n")
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
