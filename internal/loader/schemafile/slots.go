package schemafile

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// checkSlotNames rejects, by name, an "extensions" key that is not a
// registered extension or a "documents" key that is not a registered
// document, anywhere the decoded form carries such a slot. The JSON Schema
// would reject the same payloads through additionalProperties: false, but
// its message names a JSON pointer; this one names the key and where it sits.
func checkSlotNames(payload map[string]any, f form, reg *registry.Registry) error {
	switch f {
	case formDocument:
		if err := checkExtensionNames(payload, "the document", reg); err != nil {
			return err
		}
		if err := checkDocumentNames(payload, reg); err != nil {
			return err
		}
		if types, ok := payload["types"].(map[string]any); ok {
			for name, raw := range types {
				td, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if err := checkTypeExtensionNames(td, fmt.Sprintf("type %q", name), reg); err != nil {
					return err
				}
			}
		}
		if sets, ok := payload["operationSets"].([]any); ok {
			for _, raw := range sets {
				set, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if err := checkOperationSetExtensionNames(set, reg); err != nil {
					return err
				}
			}
		}
	case formType:
		name, _ := payload["name"].(string)
		return checkTypeExtensionNames(payload, fmt.Sprintf("type %q", name), reg)
	case formOperationSet:
		return checkOperationSetExtensionNames(payload, reg)
	}
	return nil
}

func checkTypeExtensionNames(td map[string]any, where string, reg *registry.Registry) error {
	if err := checkExtensionNames(td, where, reg); err != nil {
		return err
	}
	fields, _ := td["fields"].([]any)
	for _, raw := range fields {
		fd, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		fname, _ := fd["name"].(string)
		if err := checkExtensionNames(fd, fmt.Sprintf("%s field %q", where, fname), reg); err != nil {
			return err
		}
	}
	return nil
}

func checkOperationSetExtensionNames(set map[string]any, reg *registry.Registry) error {
	name, _ := set["name"].(string)
	where := fmt.Sprintf("operation set %q", name)
	if err := checkExtensionNames(set, where, reg); err != nil {
		return err
	}
	ops, _ := set["operations"].([]any)
	for _, raw := range ops {
		op, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		oname, _ := op["name"].(string)
		if err := checkExtensionNames(op, fmt.Sprintf("%s operation %q", where, oname), reg); err != nil {
			return err
		}
	}
	return nil
}

func checkExtensionNames(node map[string]any, where string, reg *registry.Registry) error {
	exts, ok := node["extensions"].(map[string]any)
	if !ok {
		return nil
	}
	for _, name := range sortedKeys(exts) {
		if !slices.Contains(reg.Extensions(), name) {
			return unknownSlotName("extensions", name, where, reg.Extensions())
		}
	}
	return nil
}

func checkDocumentNames(payload map[string]any, reg *registry.Registry) error {
	docs, ok := payload["documents"].(map[string]any)
	if !ok {
		return nil
	}
	registered := make([]string, 0, len(reg.Documents()))
	for _, spec := range reg.Documents() {
		registered = append(registered, spec.Name)
	}
	for _, name := range sortedKeys(docs) {
		if !slices.Contains(registered, name) {
			return unknownSlotName("documents", name, "the document", registered)
		}
	}
	return nil
}

func unknownSlotName(slot, name, where string, registered []string) error {
	known := "none are registered"
	if len(registered) > 0 {
		known = "registered: " + strings.Join(registered, ", ")
	}
	return fmt.Errorf("%s key %q on %s is not a registered %s (%s)", slot, name, where, strings.TrimSuffix(slot, "s"), known)
}

// applyExtensionDecorators runs each extension decorator named under a
// node's "extensions" through the registry, exactly as the TypeScript
// frontend does for @decorator syntax: the key is the decorator name, the
// value its single argument (or true for an argument-less decorator). The
// value is already in the IR slot; Apply validates it and may set derived
// data beside it.
func applyExtensionDecorators(node registry.Node, exts map[string]json.RawMessage, target registry.DecoratorTarget, kind ir.SchemaKind, where, owner string, reg *registry.Registry) []error {
	var errs []error
	for _, ext := range sortedKeys(exts) {
		var decorators map[string]json.RawMessage
		if err := json.Unmarshal(exts[ext], &decorators); err != nil {
			errs = append(errs, fmt.Errorf("%s: %s: extension %q: %w", owner, where, ext, err))
			continue
		}
		for _, name := range sortedKeys(decorators) {
			spec, ok := reg.Decorator(name, target)
			if !ok || spec.Extension != ext {
				errs = append(errs, fmt.Errorf("%s: %s: extension %q has no decorator %q for %s", owner, where, ext, name, target))
				continue
			}
			if !spec.AllowsKind(string(kind)) {
				errs = append(errs, fmt.Errorf("%s: %s: %s", owner, where, spec.KindError(string(kind))))
				continue
			}
			var args []any
			if spec.Args != nil {
				var value any
				if err := json.Unmarshal(decorators[name], &value); err != nil {
					errs = append(errs, fmt.Errorf("%s: %s: extension %q decorator %q: %w", owner, where, ext, name, err))
					continue
				}
				args = []any{value}
			}
			if err := spec.ValidateArgs(args); err != nil {
				errs = append(errs, fmt.Errorf("%s: %s: %w", owner, where, err))
				continue
			}
			if spec.Apply == nil {
				continue
			}
			if err := spec.Apply(node, args, registry.Site{File: owner}); err != nil {
				errs = append(errs, fmt.Errorf("%s: %s: @%s: %w", owner, where, name, err))
			}
		}
	}
	return errs
}
