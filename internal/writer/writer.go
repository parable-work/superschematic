// Package writer is the write half of the multi-format schema system: it
// projects the Schema IR back onto the three on-disk formats. Each format
// writer is the inverse of its reader; together they form the closed loop
// the round-trip test suite enforces (read native -> write other formats ->
// re-read -> equal IR, comment metadata included).
package writer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	"github.com/parable-work/superschematic/internal/writer/jsonwriter"
	"github.com/parable-work/superschematic/internal/writer/tswriter"
	"github.com/parable-work/superschematic/internal/writer/yamlwriter"
	ir "github.com/parable-work/superschematic/ir"
)

// Format identifies one of the three on-disk schema formats.
type Format string

const (
	// FormatTS is the TypeScript authoring format (*.schema.ts).
	FormatTS Format = "ts"

	// FormatJSON is the JSON data format (*.schema.json).
	FormatJSON Format = "json"

	// FormatYAML is the YAML data format (*.schema.yaml).
	FormatYAML Format = "yaml"
)

// ParseFormat resolves a format name as given on a CLI flag.
func ParseFormat(name string) (Format, error) {
	switch Format(name) {
	case FormatTS, FormatJSON, FormatYAML:
		return Format(name), nil
	}
	return "", fmt.Errorf("unknown format %q: expected ts, json, or yaml", name)
}

// Extension returns the schema file extension for the format, including the
// ".schema." prefix.
func (f Format) Extension() string {
	switch f {
	case FormatTS:
		return ".schema.ts"
	case FormatJSON:
		return ".schema.json"
	default:
		return ".schema.yaml"
	}
}

// Write renders one schema document in the given format. It is the inverse
// of the per-file readers: the output decodes (or walks) back to a document
// with equal IR content, including comment metadata.
func Write(doc *schemafile.Document, format Format) ([]byte, error) {
	switch format {
	case FormatJSON:
		return jsonwriter.Write(doc)
	case FormatYAML:
		return yamlwriter.Write(doc)
	case FormatTS:
		return tswriter.Write(doc)
	}
	return nil, fmt.Errorf("unknown format %q", format)
}

// schemaFileExtensions are the extensions Owner paths carry, longest first
// so ".schema.yaml" wins over ".yaml".
var schemaFileExtensions = []string{".schema.ts", ".schema.json", ".schema.yaml", ".schema.yml"}

const platformDefaultSuffix = ".platform-default.json"

// ownerBase converts an Owner path ("src/orders.schema.ts") into the
// extension-free schema file base ("src/orders.schema"). It returns false
// when the owner is not a schema file path (enums carry the service name).
func ownerBase(owner string) (string, bool) {
	for _, ext := range schemaFileExtensions {
		if strings.HasSuffix(owner, ext) {
			return strings.TrimSuffix(owner, ext) + ".schema", true
		}
	}
	return "", false
}

// SplitSchema is the inverse of schemafile.Merge: it splits a service's
// assembled IR into per-file documents keyed by the extension-free schema
// file base ("src/orders.schema"). Types group by their Owner
// path; definitions with no file attribution (scalars, enums, unions,
// and operation sets) land in the first document
// in sorted order, or in "src/<service>.schema" when the service has no
// attributed definitions at all.
//
// Owner fields are cleared on the emitted copies: a definition's location is
// implicit in the file it is written to and is re-stamped at read time.
func SplitSchema(schema *ir.Schema) map[string]*schemafile.Document {
	docs := make(map[string]*schemafile.Document)
	docFor := func(base string) *schemafile.Document {
		if doc, ok := docs[base]; ok {
			return doc
		}
		doc := &schemafile.Document{Name: schema.Name, Kind: schema.Kind}
		docs[base] = doc
		return doc
	}

	for _, name := range sortedKeys(schema.Types) {
		def := schema.Types[name]
		base, ok := ownerBase(def.Owner)
		if !ok {
			continue
		}
		copied := *def
		copied.Owner = ""
		doc := docFor(base)
		if doc.Types == nil {
			doc.Types = make(map[string]*ir.TypeDef)
		}
		doc.Types[name] = &copied
	}

	// Everything else has no file attribution; it goes in the first document.
	first := func() *schemafile.Document {
		keys := sortedKeys(docs)
		if len(keys) > 0 {
			return docs[keys[0]]
		}
		return docFor("src/" + schema.Name + ".schema")
	}

	if len(schema.Types) > 0 {
		// Types whose Owner is not a file path (authored docs can leave
		// Owner unset until Merge stamps it) also fall through to here.
		for _, name := range sortedKeys(schema.Types) {
			def := schema.Types[name]
			if _, ok := ownerBase(def.Owner); ok {
				continue
			}
			copied := *def
			copied.Owner = ""
			doc := first()
			if doc.Types == nil {
				doc.Types = make(map[string]*ir.TypeDef)
			}
			doc.Types[name] = &copied
		}
	}

	for _, name := range sortedKeys(schema.Scalars) {
		doc := first()
		if doc.Scalars == nil {
			doc.Scalars = make(map[string]*ir.ScalarDef)
		}
		doc.Scalars[name] = schema.Scalars[name]
	}
	for _, name := range sortedKeys(schema.Enums) {
		def := schema.Enums[name]
		copied := *def
		copied.Owner = ""
		doc := first()
		if doc.Enums == nil {
			doc.Enums = make(map[string]*ir.EnumDef)
		}
		doc.Enums[name] = &copied
	}
	for _, name := range sortedKeys(schema.Unions) {
		doc := first()
		if doc.Unions == nil {
			doc.Unions = make(map[string]*ir.UnionDef)
		}
		doc.Unions[name] = schema.Unions[name]
	}
	if len(schema.OperationSets) > 0 {
		doc := first()
		doc.OperationSets = append(doc.OperationSets, schema.OperationSets...)
	}

	// Imports are service-level in the IR; restate them on every document so
	// any document alone names its cross-service references.
	for base := range docs {
		docs[base].Imports = append([]ir.Import(nil), schema.Imports...)
	}

	// Schema-level description, comment, extensions and documents go on the
	// first document only; Merge rejects documents restating them with
	// different content, and restating identical content on every file is
	// noise.
	keys := sortedKeys(docs)
	if len(keys) > 0 {
		docs[keys[0]].Description = schema.Description
		docs[keys[0]].Comment = schema.Comment
		docs[keys[0]].Extensions = schema.Extensions
		docs[keys[0]].Documents = schema.Documents
	}

	return docs
}

// WriteService projects a service's IR into schema files under outDir, one
// per SplitSchema document, and returns the written paths relative to
// outDir in sorted order.
//
// TypeScript output carries service-level context: the full enum set (for
// Default<Enum, Member> rendering) and the location of every definition (so
// cross-file references emit relative imports).
//
// JSON/YAML documents also receive SourceRef lineage imports: @source is
// compile-time only in TypeScript (absent from schema.Imports), but the
// data formats have no compiler and need the imports-block escape hatch
// so verification can stand the authored SourceRef without ExternalTypes.
func WriteService(schema *ir.Schema, format Format, outDir string) ([]string, error) {
	docs := SplitSchema(schema)
	if format == FormatJSON || format == FormatYAML {
		// Materialize @source lineage into each document's imports block so
		// data-format reload can stand the SourceRef without ExternalTypes.
		lineageSchema := withSourceLineageImports(schema)
		for base := range docs {
			docs[base].Imports = append([]ir.Import(nil), lineageSchema.Imports...)
		}
	}

	defLocations := make(map[string]string)
	for base, doc := range docs {
		for name := range doc.Types {
			defLocations[name] = base
		}
		for name := range doc.Enums {
			defLocations[name] = base
		}
		for name := range doc.Unions {
			defLocations[name] = base
		}
	}

	var written []string
	for _, base := range sortedKeys(docs) {
		var data []byte
		var err error
		if format == FormatTS {
			data, err = tswriter.WriteContext(docs[base], &tswriter.Context{
				Enums:        schema.Enums,
				DefLocations: defLocations,
				Base:         base,
			})
		} else {
			data, err = Write(docs[base], format)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", base, err)
		}
		rel := strings.TrimSuffix(base, ".schema") + format.Extension()
		path := filepath.Join(outDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, err
		}
		written = append(written, rel)
	}
	for _, name := range sortedKeys(schema.CompositeDefaults) {
		def := schema.CompositeDefaults[name]
		if def == nil {
			continue
		}
		rel := def.Owner
		if !strings.HasSuffix(rel, platformDefaultSuffix) {
			rel = filepath.ToSlash(filepath.Join("src", "defaults", strings.ToLower(name)+platformDefaultSuffix))
		}
		data, err := json.MarshalIndent(struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		}{
			Type:  def.Type,
			Value: json.RawMessage(def.CanonicalJSON),
		}, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("%s: render platform default: %w", rel, err)
		}
		data = append(data, '\n')
		path := filepath.Join(outDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, err
		}
		written = append(written, rel)
	}
	sort.Strings(written)
	return written, nil
}

// sortedKeys returns map keys in sorted order for deterministic output.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sourceLineageImports returns the cross-service imports implied by @source
// targets on the schema's types. Same-service (or unqualified) targets are
// omitted — they resolve locally without an imports block entry.
func sourceLineageImports(schema *ir.Schema) []ir.Import {
	byPkg := map[string]map[string]bool{}
	for _, name := range sortedKeys(schema.Types) {
		td := schema.Types[name]
		if td.Source == nil {
			continue
		}
		service, typeName := splitSourceTarget(td.Source.Target)
		if service == "" || service == schema.Name || typeName == "" {
			continue
		}
		// The writer has no Options to thread naming through; it reads the
		// process-wide value the CLI set (naming.Active).
		pkg := naming.Active().NpmServicePackage(service)
		if byPkg[pkg] == nil {
			byPkg[pkg] = map[string]bool{}
		}
		byPkg[pkg][typeName] = true
	}
	if len(byPkg) == 0 {
		return nil
	}
	out := make([]ir.Import, 0, len(byPkg))
	for _, pkg := range sortedKeys(byPkg) {
		types := sortedKeys(byPkg[pkg])
		out = append(out, ir.Import{Package: pkg, Types: types})
	}
	return out
}

// withSourceLineageImports returns a shallow copy of schema whose Imports
// union in SourceRef lineage packages. Used by the data-format round-trip
// comparison and by writeServiceConfig so reloaded JSON/YAML schemas that
// declare those imports also have matching schema.config dependencies.
func withSourceLineageImports(schema *ir.Schema) *ir.Schema {
	cp := *schema
	cp.Imports = mergeImportLists(schema.Imports, sourceLineageImports(schema))
	return &cp
}

// mergeImportLists unions two import lists by package, sorting packages and
// type names for stable IR comparisons.
func mergeImportLists(base, extra []ir.Import) []ir.Import {
	byPkg := map[string]map[string]bool{}
	for _, list := range [][]ir.Import{base, extra} {
		for _, imp := range list {
			if byPkg[imp.Package] == nil {
				byPkg[imp.Package] = map[string]bool{}
			}
			for _, t := range imp.Types {
				byPkg[imp.Package][t] = true
			}
		}
	}
	if len(byPkg) == 0 {
		return nil
	}
	out := make([]ir.Import, 0, len(byPkg))
	for _, pkg := range sortedKeys(byPkg) {
		out = append(out, ir.Import{Package: pkg, Types: sortedKeys(byPkg[pkg])})
	}
	return out
}

// splitSourceTarget splits "service.Type" into its parts. An unqualified
// target returns an empty service name.
func splitSourceTarget(target string) (service, typeName string) {
	if i := strings.LastIndex(target, "."); i >= 0 {
		return target[:i], target[i+1:]
	}
	return "", target
}
