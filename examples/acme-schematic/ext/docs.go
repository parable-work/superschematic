package ext

import (
	"maps"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// Audiences is acme's closed set of @docs audiences. The core accepts any
// audience; acme requires one of these on every documented operation.
var Audiences = []string{"shoppers", "staff"}

// Icons is acme's icon set for the core @icon field decorator. The core
// accepts any name; acme's UI draws only these.
var Icons = []string{"box", "globe", "key", "receipt", "tag"}

// DocsKey is the vendor-extension key acme's OpenAPI documents carry an
// operation's @docs record under, in place of the core's key.
const DocsKey = "x-acme-docs"

// registerDocsPolicy lays acme's policy over the core documentation
// decorators: checks that restrict @docs audiences and @icon names, and an
// OpenAPI hook that moves the @docs record to acme's vendor key. None needs
// a core option.
func registerDocsPolicy(r *registry.Registry) error {
	if err := r.RegisterCheck(registry.CheckSpec{
		Name:      "acmeIcons",
		Extension: Name,
		Verify: func(schema *ir.Schema, rep registry.VerifyReporter) {
			check := func(typeName string, fields []*ir.FieldDef) {
				for _, f := range fields {
					if f.Icon != "" && !slices.Contains(Icons, f.Icon) {
						rep.Errorf("", "%s.%s: @icon %q is not in the acme icon set (%s)",
							typeName, f.Name, f.Icon, strings.Join(Icons, ", "))
					}
				}
			}
			for _, name := range slices.Sorted(maps.Keys(schema.Types)) {
				check(name, schema.Types[name].Fields)
			}
		},
	}); err != nil {
		return err
	}
	if err := r.RegisterCheck(registry.CheckSpec{
		Name:      "acmeDocsAudience",
		Extension: Name,
		Verify: func(schema *ir.Schema, rep registry.VerifyReporter) {
			for _, set := range schema.OperationSets {
				for _, op := range set.Operations {
					if op.Docs == nil || slices.Contains(Audiences, string(op.Docs.Audience)) {
						continue
					}
					rep.Errorf("", "%s.%s: @docs audience %q is not an acme audience (%s)",
						set.Name, op.Name, op.Docs.Audience, strings.Join(Audiences, ", "))
				}
			}
		},
	}); err != nil {
		return err
	}
	return r.RegisterOpenAPIHook(registry.OpenAPIHook{
		Name:      "acmeDocsKey",
		Extension: Name,
		Edit: func(_ *ir.Schema, doc map[string]any) error {
			paths, _ := doc["paths"].(map[string]any)
			for _, item := range paths {
				methods, _ := item.(map[string]any)
				for _, op := range methods {
					operation, ok := op.(map[string]any)
					if !ok {
						continue
					}
					if record, ok := operation[registry.OpenAPIDocsKey]; ok {
						delete(operation, registry.OpenAPIDocsKey)
						operation[DocsKey] = record
					}
				}
			}
			return nil
		},
	})
}
