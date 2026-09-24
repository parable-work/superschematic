package registry

import (
	"fmt"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// docsDecorators returns the documentation decorators: @docs on an
// operation writes FieldDef.Docs. What values an extension accepts on top
// of the shape checked here (an audience vocabulary, say) is a CheckSpec
// the extension registers.
func docsDecorators() []DecoratorSpec {
	return []DecoratorSpec{{
		Name: "docs", Packages: []string{pkgAPI}, Target: TargetOperation,
		Apply: func(n Node, args []any, _ Site) error {
			if n.Field.Docs != nil {
				return fmt.Errorf("operation %s has more than one @docs decorator", n.Field.Name)
			}
			docs, err := operationDocs(args)
			if err != nil {
				return err
			}
			n.Field.Docs = docs
			return nil
		},
	}}
}

// operationDocs reads @docs({ title, description, capability, lifecycle,
// visibility, audience?, mappingStatus?, replacement?, sunset? }).
// mappingStatus defaults to "mapped". Keys are read in sorted order so the
// first error is the same on every run.
func operationDocs(args []any) (*ir.OperationDocs, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("@docs takes exactly one config object")
	}
	cfg, ok := args[0].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@docs config must be an object literal")
	}
	out := &ir.OperationDocs{MappingStatus: ir.DocsMappingStatusMapped}
	for _, key := range sortedKeys(cfg) {
		value, ok := cfg[key].(string)
		if !ok {
			return nil, ArgErrorf(0, "@docs %s must be a string literal", key)
		}
		switch key {
		case "title":
			out.Title = value
		case "description":
			out.Description = value
		case "capability":
			out.Capability = value
		case "lifecycle":
			out.Lifecycle = ir.DocsLifecycle(value)
		case "visibility":
			out.Visibility = ir.DocsVisibility(value)
		case "audience":
			out.Audience = ir.DocsAudience(value)
		case "mappingStatus":
			out.MappingStatus = ir.DocsMappingStatus(value)
		case "replacement":
			out.Replacement = value
		case "sunset":
			out.Sunset = value
		default:
			return nil, ArgErrorf(0, "@docs config has unknown key %q", key)
		}
	}
	if err := ir.ValidateOperationDocs(out); err != nil {
		return nil, ArgErrorf(0, "invalid @docs config: %s", err)
	}
	return out, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
