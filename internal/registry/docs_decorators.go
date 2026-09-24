package registry

import (
	"fmt"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// docsDecorators returns the documentation decorators: @docs on an
// operation writes FieldDef.Docs; on a field, @docs({ title }), @purpose
// and @icon write FieldDef.Title, Purpose and Icon. What values an
// extension accepts on top of the shape checked here (an audience
// vocabulary, an icon set) is a CheckSpec the extension registers.
func docsDecorators() []DecoratorSpec {
	return []DecoratorSpec{{
		Name: "docs", Packages: []string{pkgSchema}, Target: TargetField,
		Apply: func(n Node, args []any, _ Site) error {
			if n.Field.Title != "" {
				return fmt.Errorf("field %s has more than one @docs decorator", n.Field.Name)
			}
			title, err := fieldDocsTitle(args)
			if err != nil {
				return err
			}
			n.Field.Title = title
			return nil
		},
	}, {
		Name: "purpose", Packages: []string{pkgSchema}, Target: TargetField,
		Apply: func(n Node, args []any, _ Site) error {
			return setFieldText(n.Field, "purpose", &n.Field.Purpose, args)
		},
	}, {
		Name: "icon", Packages: []string{pkgSchema}, Target: TargetField,
		Apply: func(n Node, args []any, _ Site) error {
			return setFieldText(n.Field, "icon", &n.Field.Icon, args)
		},
	}, {
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
// visibility, audience?, mappingStatus?, replacement?, sunset?, useWhen?,
// doNotUseWhen?, success?, errors? }). mappingStatus defaults to "mapped".
// Keys are read in sorted order so the first error is the same on every run.
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
		if key == "errors" {
			docErrors, err := operationDocsErrors(cfg[key])
			if err != nil {
				return nil, err
			}
			out.Errors = docErrors
			continue
		}
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
		case "useWhen":
			out.UseWhen = value
		case "doNotUseWhen":
			out.DoNotUseWhen = value
		case "success":
			out.Success = value
		default:
			return nil, ArgErrorf(0, "@docs config has unknown key %q", key)
		}
	}
	if err := ir.ValidateOperationDocs(out); err != nil {
		return nil, ArgErrorf(0, "invalid @docs config: %s", err)
	}
	return out, nil
}

// operationDocsErrors reads @docs errors: a non-empty array of
// { code, description, commonCorrection } objects.
func operationDocsErrors(value any) ([]ir.OperationDocsError, error) {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return nil, ArgErrorf(0, "@docs errors must be a non-empty array of object literals")
	}
	out := make([]ir.OperationDocsError, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, ArgErrorf(0, "@docs errors must be a non-empty array of object literals")
		}
		var docError ir.OperationDocsError
		for _, key := range sortedKeys(entry) {
			text, ok := entry[key].(string)
			if !ok {
				return nil, ArgErrorf(0, "@docs errors %s must be a string literal", key)
			}
			switch key {
			case "code":
				docError.Code = text
			case "description":
				docError.Description = text
			case "commonCorrection":
				docError.CommonCorrection = text
			default:
				return nil, ArgErrorf(0, "@docs errors has unknown key %q", key)
			}
		}
		out = append(out, docError)
	}
	return out, nil
}

// fieldDocsTitle reads a field's @docs({ title }): the object holds the
// title and nothing else.
func fieldDocsTitle(args []any) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("field @docs takes exactly one config object")
	}
	cfg, ok := args[0].(map[string]any)
	if !ok || len(cfg) != 1 {
		return "", ArgErrorf(0, "field @docs config must contain only title")
	}
	title, ok := cfg["title"].(string)
	if !ok || strings.TrimSpace(title) == "" {
		return "", ArgErrorf(0, "field @docs title must be a non-empty string literal")
	}
	return title, nil
}

// setFieldText applies a single-string field decorator (@purpose, @icon)
// once per field.
func setFieldText(field *ir.FieldDef, name string, target *string, args []any) error {
	if *target != "" {
		return fmt.Errorf("field %s has more than one @%s decorator", field.Name, name)
	}
	if len(args) != 1 {
		return fmt.Errorf("@%s takes exactly one string argument", name)
	}
	text, ok := args[0].(string)
	if !ok || strings.TrimSpace(text) == "" {
		return ArgErrorf(0, "@%s takes a non-empty string literal", name)
	}
	*target = text
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
