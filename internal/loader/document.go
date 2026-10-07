package loader

import (
	"errors"
	"fmt"

	"github.com/parable-work/superschematic/internal/loader/schemafile"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// LoadDocument builds the Schema IR of one schema file's document on its
// own, the way an engine takes a schema (D16): the document names its
// schema and kind, and there is no service config, sidecar file or
// dependency. It runs the checks a service load runs on its data files:
// the merge with the registry's decorators and behaviors, the scalar
// catalog, the IR checks and the verification pass, which holds each
// type's behaviors to their declarations. source names the file in errors.
func LoadDocument(doc *schemafile.Document, source string, opts ...Option) (*ir.Schema, error) {
	var o loadOptions
	for _, opt := range opts {
		opt(&o)
	}
	reg := o.registryOrCore()
	if doc.Name == "" {
		return nil, fmt.Errorf("%s: the document names no schema (name)", source)
	}
	if doc.Kind == "" {
		return nil, fmt.Errorf("%s: the document names no kind (kind)", source)
	}
	schema := ir.NewSchema(doc.Name, doc.Kind)
	if errs := schemafile.MergeWith(doc, schema, source, reg); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if err := hydrateScalarsFromRegistry(schema, reg.Scalars()); err != nil {
		return nil, err
	}
	externals := schemafile.ImportedTypeNames(schema)
	for _, name := range languagePrimitiveNames {
		externals[name] = true
	}
	var errs []error
	for _, e := range schema.Validate(ir.WithKnownExternals(externals)) {
		errs = append(errs, fmt.Errorf("%s: %s", schema.Name, e.Error()))
	}
	if err := validateHydrated(schema); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if err := validateDefaultValues(schema, nil); err != nil {
		return nil, err
	}
	return runVerify(schema, verify.Input{Naming: o.naming, Registry: reg})
}
