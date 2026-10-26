package engineclientgen

import (
	"fmt"
	"sort"

	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/registry"
)

// namespaceModel is a behavior with namespace operations (D77) that a type
// of the module composes. Its operations run on the behavior's tables
// across every schema of a namespace that composes it, called by the
// behavior's name, so the module types them once, named for the behavior,
// whichever types compose it: <B><Op>Params and <B><Op>Result,
// <B>NamespaceOperations, <B>VetoCode, <B>NamespaceClient with its factory,
// and is<B>NamespaceVeto. No config narrows them.
type namespaceModel struct {
	declaration registry.Behavior
	// name is the behavior's name, which the engine's route carries.
	name string
	// prefix names the exports: the behavior's name as a member name
	// (Comments; AcmeChatter for acme.Chatter).
	prefix string
	// client names the wrapper: CommentsNamespaceClient.
	client string
	ops    []namespaceOperation
}

// namespaceOperation is one namespace operation of a behavior, in
// declaration order.
type namespaceOperation struct {
	declaration registry.BehaviorOperation
	params      any
	result      any
	paramsType  string
	resultType  string
}

// coreNamespaceItems name the item types of a core behavior's namespace
// operations' results, by <Behavior>.<operation>: the schema of each
// member of the result's items, which a caller holds one at a time.
var coreNamespaceItems = map[string]string{
	"Comments.inbox":            "CommentsInboxItem",
	"Comments.claimDeliveries":  "CommentsDelivery",
	"Comments.settleDeliveries": "CommentsSettledDelivery",
}

// coreNamespaceItemDocs document those item types, by the same key.
var coreNamespaceItemDocs = map[string]string{
	"Comments.inbox":            "An item of Comments' inbox: a message that mentions the caller, a thread it follows by its latest reply of another, or a conversation it has read by its latest top-level message of another, with how many of them it has not read. One with none unread is done.",
	"Comments.claimDeliveries":  "A delivery claimDeliveries claimed: one message and one subject that should hear of it, with why and what a notifier needs to send it with no other read. Settle it by its id under the claim's token.",
	"Comments.settleDeliveries": "A delivery as settleDeliveries left it: its state, its claims so far, and when a failed one is due again.",
}

// namespaceBehaviors collects the behaviors with namespace operations the
// models' types compose, once each, in the order of their names.
func namespaceBehaviors(models []*schemaModel) ([]*namespaceModel, error) {
	seen := map[string]bool{}
	var out []*namespaceModel
	for _, model := range models {
		for _, behavior := range model.behaviors {
			declaration := behavior.Declaration
			if seen[declaration.Name] {
				continue
			}
			seen[declaration.Name] = true
			prefix := behaviortypes.MemberName(declaration.Name)
			nb := &namespaceModel{declaration: declaration, name: declaration.Name, prefix: prefix, client: prefix + "NamespaceClient"}
			for _, declared := range declaration.Operations {
				if !namespaceScope(declared) {
					continue
				}
				params, err := behaviortypes.DecodeSchema(declared.ParamsSchema)
				if err != nil {
					return nil, fmt.Errorf("engine-client: behavior %s operation %s paramsSchema: %w", declaration.Name, declared.Name, err)
				}
				result, err := behaviortypes.DecodeSchema(declared.ResultSchema)
				if err != nil {
					return nil, fmt.Errorf("engine-client: behavior %s operation %s resultSchema: %w", declaration.Name, declared.Name, err)
				}
				pascal := codegen.ToPascalCase(declared.Name)
				nb.ops = append(nb.ops, namespaceOperation{
					declaration: declared,
					params:      params,
					result:      result,
					paramsType:  prefix + pascal + "Params",
					resultType:  prefix + pascal + "Result",
				})
			}
			if len(nb.ops) > 0 {
				out = append(out, nb)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// namespaceBehavior writes a behavior's namespace operations' types, its
// namespace client and its veto guard.
func (m *module) namespaceBehavior(nb *namespaceModel) error {
	owner := "behavior " + nb.name
	core := nb.declaration.Extension == ""
	m.line("")
	m.line("// The %s behavior's namespace operations.", nb.name)

	for i := range nb.ops {
		op := &nb.ops[i]
		key := nb.name + "." + op.declaration.Name
		if item := coreNamespaceItems[key]; core && item != "" {
			result, err := m.namespaceItem(nb, op, item, coreNamespaceItemDocs[key])
			if err != nil {
				return err
			}
			op.result = result
		}
		doc := fmt.Sprintf("%s %s, across the schemas of a namespace that compose it", possessive(nb.name), op.declaration.Name)
		if op.declaration.Description != "" {
			doc += ": " + op.declaration.Description
		} else {
			doc += "."
		}
		if err := m.typeAlias(op.paramsType, owner+" operation "+op.declaration.Name, "The parameters of "+doc, op.params); err != nil {
			return err
		}
		resultDoc := behaviortypes.DescriptionOf(op.result)
		if resultDoc == "" {
			resultDoc = fmt.Sprintf("What %s %s returns.", possessive(nb.name), op.declaration.Name)
		}
		if err := m.typeAlias(op.resultType, owner+" operation "+op.declaration.Name, resultDoc, behaviortypes.WithoutDescription(op.result)); err != nil {
			return err
		}
	}

	operations := nb.prefix + "NamespaceOperations"
	if err := m.export(operations, owner); err != nil {
		return err
	}
	m.line("")
	m.doc("", fmt.Sprintf("Each namespace operation %s adds: its scope, parameters and result. None is a type's: %sNamespaceClient calls them.", nb.name, nb.prefix))
	m.line("export type %s = {", operations)
	for _, op := range nb.ops {
		m.line("  %s: { scope: %s; params: %s; result: %s };", op.declaration.Name, behaviortypes.TSQuote(registry.OperationScopeNamespace), op.paramsType, op.resultType)
	}
	m.line("};")

	vetoCode := nb.prefix + "VetoCode"
	if err := m.typeAlias(vetoCode, owner, fmt.Sprintf("The codes %s vetoes carry: what a refused call's details.code holds.", possessive(nb.name)), vetoCodes(nb.declaration)); err != nil {
		return err
	}
	guard := "is" + nb.prefix + "NamespaceVeto"
	if err := m.export(guard, owner); err != nil {
		return err
	}
	m.uses["isVeto"] = true
	m.uses["EngineVeto"] = true
	m.line("")
	m.doc("", fmt.Sprintf("%s is isVeto with %s and its codes, for a refusal of one of its namespace operations.", guard, nb.name))
	m.line("export function %s(error: unknown, codes?: %s | ReadonlyArray<%s>): error is EngineVeto {", guard, vetoCode, vetoCode)
	m.line("  return isVeto(error, %s, codes);", behaviortypes.TSQuote(nb.name))
	m.line("}")

	return m.namespaceClient(nb)
}

// vetoCodes is the schema of a behavior's veto codes: never when it
// declares none.
func vetoCodes(declaration registry.Behavior) any {
	if len(declaration.Vetoes) == 0 {
		return false
	}
	codes := make([]string, 0, len(declaration.Vetoes))
	for _, veto := range declaration.Vetoes {
		codes = append(codes, veto.Code)
	}
	return behaviortypes.EnumOf(codes)
}

// namespaceItem writes the type of a member of an operation's result's
// items, and returns the result with its items referring to it. The result
// must hold an array of objects at items, so the name cannot drift from the
// declaration unnoticed.
func (m *module) namespaceItem(nb *namespaceModel, op *namespaceOperation, name, doc string) (any, error) {
	result, ok := op.result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("engine-client: behavior %s operation %s has no result schema to name %s in", nb.name, op.declaration.Name, name)
	}
	properties, _ := result["properties"].(map[string]any)
	items, _ := properties["items"].(map[string]any)
	item, _ := items["items"].(map[string]any)
	if item == nil || item["type"] != "object" {
		return nil, fmt.Errorf("engine-client: behavior %s operation %s's result holds no items of objects to name %s", nb.name, op.declaration.Name, name)
	}
	if doc == "" {
		doc = behaviortypes.DescriptionOf(item)
	}
	if err := m.typeAlias(name, "behavior "+nb.name+" operation "+op.declaration.Name, doc, behaviortypes.WithoutDescription(item)); err != nil {
		return nil, err
	}
	out := cloneMap(result)
	outProperties := cloneMap(properties)
	outItems := cloneMap(items)
	outItems["items"] = map[string]any{behaviortypes.RefKey: name}
	outProperties["items"] = outItems
	out["properties"] = outProperties
	return out, nil
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// namespaceClient writes a behavior's namespace client: its interface, one
// method per namespace operation, and its factory, which binds the engine
// client to a namespace.
func (m *module) namespaceClient(nb *namespaceModel) error {
	owner := "behavior " + nb.name
	factory := codegen.ToCamelCase(nb.client)
	if err := m.export(nb.client, owner); err != nil {
		return err
	}
	if err := m.export(factory, owner); err != nil {
		return err
	}
	m.uses["EngineClient"] = true
	m.uses["CallOptions"] = true
	options := "Omit<CallOptions, 'namespace'>"

	m.line("")
	m.doc("", fmt.Sprintf("%s namespace operations in one namespace, typed: each runs on its tables across the schemas of the namespace that compose it and that the caller may read, and the policy is asked of the behavior and the operation, not of a schema (D77).", possessive(nb.name)))
	m.line("export interface %s {", nb.client)
	for _, op := range nb.ops {
		m.doc("  ", fmt.Sprintf("%s %s: %s", possessive(nb.name), op.declaration.Name, op.declaration.Description))
		optional := ""
		if requiresNothing(op.params) {
			optional = "?"
		}
		m.line("  %s(params%s: %s, options?: %s): Promise<%s>;", op.declaration.Name, optional, op.paramsType, options, op.resultType)
	}
	m.line("}")

	m.line("")
	m.doc("", fmt.Sprintf("%s binds an EngineClient to a namespace, the client's own when none is given, for %s namespace operations.", factory, possessive(nb.name)))
	m.line("export function %s(client: EngineClient, namespace?: string): %s {", factory, nb.client)
	m.line("  return {")
	for _, op := range nb.ops {
		params := "params"
		if requiresNothing(op.params) {
			params = "params ?? {}"
		}
		m.line("    %s(params, options) {", op.declaration.Name)
		m.line("      return client.behaviors.invoke<%s>(%s, %s, %s, namespace === undefined ? options : { ...options, namespace });",
			op.resultType, behaviortypes.TSQuote(nb.name), behaviortypes.TSQuote(op.declaration.Name), params)
		m.line("    },")
	}
	m.line("  };")
	m.line("}")
	return nil
}
