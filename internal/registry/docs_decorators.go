package registry

import (
	"fmt"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	ir "github.com/parable-work/superschematic/ir"
)

// docsDecorators returns the documentation decorators: @docs on an
// operation writes FieldDef.Docs, @mcp writes FieldDef.MCP and @icon writes
// FieldDef.Icon; on a field, @docs({ title }), @purpose and @icon write
// FieldDef.Title, Purpose and Icon. What values an extension accepts on top
// of the shape checked here (an audience vocabulary, an icon set, which
// operations must declare @mcp) is a CheckSpec the extension registers.
// invocation returns the registry's tool invocation policy, which decides
// the key and values @mcp accepts for a visible tool's policy and the
// default it fills in.
func docsDecorators(invocation func() ToolInvocationPolicy) []DecoratorSpec {
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
	}, {
		Name: "mcp", Packages: []string{pkgAPI}, Target: TargetOperation,
		Apply: func(n Node, args []any, _ Site) error {
			if n.Field.MCP != nil {
				return fmt.Errorf("operation %s has more than one @mcp decorator", n.Field.Name)
			}
			mcp, err := operationMCP(args, invocation())
			if err != nil {
				return err
			}
			n.Field.MCP = mcp
			return nil
		},
	}, {
		Name: "icon", Packages: []string{pkgAPI}, Target: TargetOperation,
		Apply: func(n Node, args []any, _ Site) error {
			if n.Field.Icon != "" {
				return fmt.Errorf("operation %s has more than one @icon decorator", n.Field.Name)
			}
			if len(args) != 1 {
				return fmt.Errorf("@icon takes exactly one string argument")
			}
			name, ok := args[0].(string)
			if !ok {
				return ArgErrorf(0, "@icon takes a string literal")
			}
			if err := ir.ValidateOperationIcon(name); err != nil {
				return ArgErrorf(0, "invalid @icon: %s", err)
			}
			n.Field.Icon = name
			return nil
		},
	}}
}

// operationMCP reads @mcp({ handle, <policy key>?, _meta? }) for a visible
// tool or @mcp({ hidden: true, reason }) for an operation that is not one.
// The policy key and its values are the invocation policy's; a visible tool
// without the key gets the policy's default.
func operationMCP(args []any, invocation ToolInvocationPolicy) (*ir.OperationMCP, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("@mcp takes exactly one config object")
	}
	cfg, ok := args[0].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@mcp config must be an object literal")
	}
	out := &ir.OperationMCP{}
	for _, key := range sortedKeys(cfg) {
		value := cfg[key]
		switch key {
		case "handle", "reason":
			text, ok := value.(string)
			if !ok {
				return nil, ArgErrorf(0, "@mcp %s must be a string literal", key)
			}
			if key == "handle" {
				out.Handle = text
			} else {
				out.HiddenReason = text
			}
		case "hidden":
			hidden, ok := value.(bool)
			if !ok {
				return nil, ArgErrorf(0, "@mcp hidden must be a boolean literal")
			}
			out.Hidden = hidden
		case "_meta":
			meta, ok := value.(map[string]any)
			if !ok {
				return nil, ArgErrorf(0, "@mcp _meta must be an object literal")
			}
			out.Meta = meta
		case invocation.Key:
			text, ok := value.(string)
			if !ok {
				return nil, ArgErrorf(0, "@mcp %s must be a string literal", key)
			}
			if err := invocation.CheckValue(text); err != nil {
				return nil, ArgErrorf(0, "invalid @mcp config: %s", err)
			}
			out.Invocation = ir.MCPInvocation{Key: key, Value: text}
		default:
			if key == apigen.DefaultToolInvocationKey {
				return nil, ArgErrorf(0, "@mcp config has unknown key %q; this build's invocation policy key is %q", key, invocation.Key)
			}
			return nil, ArgErrorf(0, "@mcp config has unknown key %q", key)
		}
	}
	if !out.Hidden && out.Invocation.IsZero() {
		out.Invocation = ir.MCPInvocation{Key: invocation.Key, Value: invocation.Default}
	}
	if err := ir.ValidateOperationMCP(out); err != nil {
		return nil, ArgErrorf(0, "invalid @mcp config: %s", err)
	}
	return out, nil
}

// operationDocs reads @docs({ title, description, capability, lifecycle,
// visibility, audience?, mappingStatus?, replacement?, sunset?,
// replayMode?, idempotencyKeyPointers?, expectedRevisionPointers?,
// useWhen?, doNotUseWhen?, success?, errors? }). mappingStatus defaults to
// "mapped".
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
		switch key {
		case "errors":
			docErrors, err := operationDocsErrors(cfg[key])
			if err != nil {
				return nil, err
			}
			out.Errors = docErrors
			continue
		case "idempotencyKeyPointers", "expectedRevisionPointers":
			pointers, err := operationDocsPointers(key, cfg[key])
			if err != nil {
				return nil, err
			}
			if key == "idempotencyKeyPointers" {
				out.IdempotencyKeyPointers = pointers
			} else {
				out.ExpectedRevisionPointers = pointers
			}
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
		case "replayMode":
			out.ReplayMode = ir.DocsReplayMode(value)
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

// operationDocsPointers reads a @docs replay pointer list: a non-empty
// array of string literals.
func operationDocsPointers(key string, value any) ([]string, error) {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return nil, ArgErrorf(0, "@docs %s must be a non-empty array of string literals", key)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		pointer, ok := item.(string)
		if !ok {
			return nil, ArgErrorf(0, "@docs %s must be a non-empty array of string literals", key)
		}
		out = append(out, pointer)
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
