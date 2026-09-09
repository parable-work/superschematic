package schemafile

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// canonicalizeDocument rewrites every extension and document value in the
// decoded document into the form the IR persists (ir.CanonicalJSON), so the
// JSON and YAML readers, which hand values over with different whitespace
// and key order, produce the same IR. It covers the four holders the IR
// defines: the root, types, fields and operation sets (operations are
// fields).
func canonicalizeDocument(doc *Document) error {
	if err := ir.CanonicalizeExtensions(doc.Extensions); err != nil {
		return err
	}
	if err := ir.CanonicalizeDocuments(doc.Documents); err != nil {
		return err
	}
	for name, def := range doc.Types {
		if err := ir.CanonicalizeExtensions(def.Extensions); err != nil {
			return fmt.Errorf("type %q: %w", name, err)
		}
		for _, field := range def.Fields {
			if err := ir.CanonicalizeExtensions(field.Extensions); err != nil {
				return fmt.Errorf("type %q field %q: %w", name, field.Name, err)
			}
		}
	}
	for _, set := range doc.OperationSets {
		if err := ir.CanonicalizeExtensions(set.Extensions); err != nil {
			return fmt.Errorf("operation set %q: %w", set.Name, err)
		}
		for _, op := range set.Operations {
			if err := ir.CanonicalizeExtensions(op.Extensions); err != nil {
				return fmt.Errorf("operation set %q operation %q: %w", set.Name, op.Name, err)
			}
		}
	}
	return nil
}
