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
	return LoadDocuments([]SourcedDocument{{Source: source, Document: doc}}, opts...)
}

// SourcedDocument is one schema file's document and the name its errors
// give it: a file's path, or its place in a request.
type SourcedDocument struct {
	Source   string
	Document *schemafile.Document
}

// LoadDocuments builds the Schema IR of one schema from the documents of
// several schema files, as LoadDocument builds it from one: the documents a
// publish carries of a database's schemas (D117), as format --to=json
// writes them. Each document names the schema and its kind, and every one
// names the same. The documents merge in order, as a service load merges
// its data files, and the checks run once on the merged schema. A
// database's documents are loaded for its dialect (WithDialects).
func LoadDocuments(docs []SourcedDocument, opts ...Option) (*ir.Schema, error) {
	var o loadOptions
	for _, opt := range opts {
		opt(&o)
	}
	reg := o.registryOrCore()
	if len(docs) == 0 {
		return nil, errors.New("no schema documents to load")
	}
	first := docs[0]
	for _, sourced := range docs {
		doc, source := sourced.Document, sourced.Source
		if doc.Name == "" {
			return nil, fmt.Errorf("%s: the document names no schema (name)", source)
		}
		if doc.Kind == "" {
			return nil, fmt.Errorf("%s: the document names no kind (kind)", source)
		}
		if doc.Name != first.Document.Name {
			return nil, fmt.Errorf("%s is a document of %s, and %s of %s: the documents are one schema's", first.Source, first.Document.Name, source, doc.Name)
		}
		if doc.Kind != first.Document.Kind {
			return nil, fmt.Errorf("%s names the kind %s, and %s the kind %s: the documents are one schema's", first.Source, first.Document.Kind, source, doc.Kind)
		}
	}
	schema := ir.NewSchema(first.Document.Name, first.Document.Kind)
	var errs []error
	for _, sourced := range docs {
		errs = append(errs, schemafile.MergeWith(sourced.Document, schema, sourced.Source, reg)...)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if err := hydrateScalarsFromRegistry(schema, reg.Scalars()); err != nil {
		return nil, err
	}
	externals := schemafile.ImportedTypeNames(schema)
	for _, name := range languagePrimitiveNames {
		externals[name] = true
	}
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
	return o.runVerify(schema, verify.Input{Naming: o.naming, Registry: reg, Dialects: o.dialects})
}
