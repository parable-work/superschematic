// Package verify is the format-agnostic validation pass on the assembled
// Schema IR. It runs after every reader (TypeScript, JSON, YAML), before
// generation, and owns the checks that are properties of the IR rather than
// of any one authoring syntax: schema-kind / import-path compatibility,
// cross-kind type-reference rules, full @source structural verification,
// trait shape checks, and the kind's own KindSpec.Verify rules.
//
// The readers stay responsible for syntax-level invariants (wildcard
// imports, decorator argument shapes); this pass is what makes those rules
// hold for every format, including the data forms that have no compiler.
package verify

import (
	"errors"
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Severity classifies a diagnostic.
type Severity string

const (
	// SeverityError marks a diagnostic that fails the build.
	SeverityError Severity = "error"

	// SeverityWarning marks a diagnostic that is reported but does not fail
	// the build (e.g. an omitted @sourceMustProject field).
	SeverityWarning Severity = "warning"
)

// Diagnostic is one verification finding with an optional source location.
type Diagnostic struct {
	// Severity classifies the finding.
	Severity Severity

	// File is the schema source file the finding points at ("" when the
	// finding has no file-level anchor).
	File string

	// Line and Col are the 1-based source location, when known (0 when the
	// finding is file-level or location-free).
	Line int
	Col  int

	// Msg is the human-readable message.
	Msg string
}

// Error formats the diagnostic as file:line:col: message, degrading
// gracefully when location detail is missing.
func (d *Diagnostic) Error() string {
	switch {
	case d.File != "" && d.Line > 0:
		return fmt.Sprintf("%s:%d:%d: %s", d.File, d.Line, d.Col, d.Msg)
	case d.File != "":
		return fmt.Sprintf("%s: %s", d.File, d.Msg)
	}
	return d.Msg
}

// ImportSite is one import occurrence in a schema source file. The readers
// collect sites so kind/import violations carry the import statement's
// source location; the data formats supply file-level locations.
type ImportSite struct {
	// Package is the imported package name (e.g. "@superschematic/db",
	// "@schemas/web-db").
	Package string

	// File is the source file containing the import.
	File string

	// Line and Col are the 1-based location of the import statement (0 when
	// the format cannot provide one).
	Line int
	Col  int
}

// Input is the reader-supplied context the pass needs beyond the schema
// itself. Every field is optional; missing context narrows what the pass
// can check rather than failing it.
type Input struct {
	// Dependencies maps dependency service names to their schema kinds, from
	// the service's schema.config. The cross-kind type-reference rules key
	// off this.
	Dependencies map[string]ir.SchemaKind

	// ImportSites lists every package import occurrence with its location.
	ImportSites []ImportSite

	// ExternalTypes maps service-qualified type names (e.g. "web-db.User")
	// to their flattened definitions, for @source targets declared in other
	// services. The TypeScript frontend resolves these through the compiler;
	// the data formats have no resolver, so a cross-service @source target
	// without an entry here is a hard error.
	ExternalTypes map[string]*ir.TypeDef

	// ExternalEnums contains compiler-resolved enum definitions referenced by
	// TypeScript fields. The composite-default validator uses their serialized
	// values without treating dependency enums as local declarations.
	ExternalEnums map[string]*ir.EnumDef

	// Naming supplies the npm scope that marks an import as a schema
	// service reference. The loader fills it from loader.WithNaming; empty
	// fields fall back to naming.Default().
	Naming naming.Naming

	// Registry supplies the per-kind import and cross-kind reference rules
	// (KindSpec) and the authoring package set. The loader fills it from
	// loader.WithRegistry; nil falls back to a core registry built from
	// Naming.
	Registry *registry.Registry
}

// registry returns in.Registry or the core fallback.
func (in Input) registry() *registry.Registry {
	if in.Registry != nil {
		return in.Registry
	}
	return registry.New(in.Naming)
}

// Result aggregates the diagnostics of one verification run.
type Result struct {
	// Errors lists the findings that fail the build.
	Errors []*Diagnostic

	// Warnings lists the findings that are reported without failing the build.
	Warnings []*Diagnostic
}

// Err joins all error diagnostics into a single error, or nil when the
// schema verified clean.
func (r *Result) Err() error {
	if len(r.Errors) == 0 {
		return nil
	}
	errs := make([]error, len(r.Errors))
	for i, d := range r.Errors {
		errs[i] = d
	}
	return errors.Join(errs...)
}

func (r *Result) errorAt(file string, line, col int, format string, args ...any) {
	r.Errors = append(r.Errors, &Diagnostic{
		Severity: SeverityError,
		File:     file, Line: line, Col: col,
		Msg: fmt.Sprintf(format, args...),
	})
}

func (r *Result) errorf(file string, format string, args ...any) {
	r.errorAt(file, 0, 0, format, args...)
}

// Errorf records a file-level error; it is the registry.VerifyReporter
// surface a KindSpec.Verify reports through.
func (r *Result) Errorf(file string, format string, args ...any) {
	r.errorf(file, format, args...)
}

// Warnf records a file-level warning; see Errorf.
func (r *Result) Warnf(file string, format string, args ...any) {
	r.warnf(file, format, args...)
}

func (r *Result) warnf(file string, format string, args ...any) {
	r.Warnings = append(r.Warnings, &Diagnostic{
		Severity: SeverityWarning,
		File:     file,
		Msg:      fmt.Sprintf(format, args...),
	})
}

// Run executes the full verification pass on an assembled schema. The pass
// canonicalizes derived @source metadata (Virtual, OmittedFromSource) as it
// verifies, so the IR a generator sees is exactly what verification proved.
// The kind's own KindSpec.Verify, when it has one, runs after the core
// checks, then every registered CheckSpec for the kind.
func Run(schema *ir.Schema, in Input) *Result {
	r := &Result{}
	checkImports(schema, in, r)
	checkSourceProjections(schema, in, r)
	checkTraits(schema, r)
	checkVersioned(schema, r)
	reg := in.registry()
	if kind, ok := reg.Kind(string(schema.Kind)); ok && kind.Verify != nil {
		kind.Verify(schema, r)
	}
	for _, check := range reg.Checks(string(schema.Kind)) {
		check.Verify(schema, r)
	}
	return r
}
