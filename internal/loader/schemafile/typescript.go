package schemafile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// TypeScriptDeclarations returns the TypeScript types of the schema-file
// data form: the root union, the Document and every IR node type under
// $defs of the core registry's JSON Schema (Definition), written from that
// schema. A registry closes six parts of it, which the types leave open so
// a document any registry accepts type-checks: the extensions slots, the
// documents, the schema kinds, the MCP invocation policy key, and a
// behavior's name and config.
//
// The emitter reads the subset of JSON Schema the reflection produces,
// and the value constraints a registered schema adds to it (@display's
// argument, D48), which no TypeScript type carries. Any other keyword or
// shape is an error that names where it sits, so a change to the
// reflection that the types cannot express fails here rather than writing
// a looser type.
func TypeScriptDeclarations() ([]byte, error) {
	reg := core()
	data, err := DefinitionFor(reg)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("decoding the schema-file JSON Schema: %w", err)
	}
	return renderDeclarations(root, reg.Kinds(), reg.ToolInvocationPolicy().Key, reg.BehaviorNames())
}

// declarationsHeader opens schema-file.d.ts; the open types follow it.
const declarationsHeader = `// @generated; do not edit
// TypeScript types for the schema-file data form: the JSON a *.schema.json
// file holds and ` + "`superschematic format --to=json`" + ` writes, either a
// multi-definition Document or one definition in its file form. They are
// written from the JSON Schema ` + "`superschematic json-schema`" + ` emits for the
// core registry (schema-file.json in this package), which is reflected from
// the Go IR structs. A registry closes six parts of that schema, which
// these types leave open: the extensions slots, the documents, the schema
// kinds, the MCP invocation policy key, and a behavior's name and config.
// Regenerate with:
//   go run ./internal/tools/schemafiletypes

/** A schema kind: one of the core's, or one a registry adds. */
export type SchemaKind = %s | (string & {});

/**
 * Extension data, keyed by extension name. On a type, a field, an operation
 * or an operation set, an extension's object is keyed by its decorator
 * names, each holding the decorator's argument (true for a decorator that
 * takes none). On the document root the object is the extension's own. A
 * registry admits the extensions and decorators it links.
 */
export type Extensions = { [extension: string]: { [key: string]: unknown } };

/**
 * Sidecar documents, keyed by document name. A registry admits the
 * documents it registers, each with its own schema.
 */
export type Documents = { [document: string]: unknown };
`

// declarations renders one JSON Schema into TypeScript.
type declarations struct {
	defs      map[string]any
	kinds     []string
	policyKey string
	behaviors []string
	out       bytes.Buffer

	// The parts a registry closes, recorded as they are written so a
	// schema that lost one fails instead of writing a closed type.
	sawKind, sawDocuments, sawPolicy                    bool
	sawBehaviorList, sawBehaviorName, sawBehaviorConfig bool
	sawExtensions                                       map[string]bool
}

func renderDeclarations(root map[string]any, kinds []string, policyKey string, behaviors []string) ([]byte, error) {
	if err := onlyKeys(root, "the root", "$schema", "$id", "$defs", "oneOf", "title", "description"); err != nil {
		return nil, err
	}
	defs, ok := root["$defs"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the root has no $defs object")
	}
	d := &declarations{defs: defs, kinds: kinds, policyKey: policyKey, behaviors: behaviors, sawExtensions: map[string]bool{}}

	kindLiterals := make([]string, len(kinds))
	for i, kind := range kinds {
		kindLiterals[i] = tsString(kind)
	}
	fmt.Fprintf(&d.out, declarationsHeader, strings.Join(kindLiterals, " | "))

	if err := d.writeRoot(root); err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(defs) {
		if err := d.writeDef(name, defs[name]); err != nil {
			return nil, err
		}
	}

	if !d.sawKind {
		return nil, fmt.Errorf("$defs/Document has no kind property")
	}
	if !d.sawDocuments {
		return nil, fmt.Errorf("$defs/Document has no documents property")
	}
	if !d.sawPolicy {
		return nil, fmt.Errorf("$defs/OperationMCP has no %q property", policyKey)
	}
	if !d.sawBehaviorList {
		return nil, fmt.Errorf("$defs/TypeDef has no behaviors property")
	}
	if !d.sawBehaviorName || !d.sawBehaviorConfig {
		return nil, fmt.Errorf("$defs/BehaviorRef has no name or no config property")
	}
	for _, slot := range extensionSlots {
		if !d.sawExtensions[slot.def] {
			return nil, fmt.Errorf("$defs/%s has no extensions property", slot.def)
		}
	}
	return d.out.Bytes(), nil
}

// writeRoot writes the root oneOf as the SchemaFile union.
func (d *declarations) writeRoot(root map[string]any) error {
	variants, ok := root["oneOf"].([]any)
	if !ok || len(variants) == 0 {
		return fmt.Errorf("the root has no oneOf list")
	}
	names := make([]string, len(variants))
	for i, variant := range variants {
		name, err := d.typeOf(variant, fmt.Sprintf("oneOf[%d]", i))
		if err != nil {
			return err
		}
		names[i] = name
	}
	d.out.WriteString("\n")
	if description, ok := root["description"].(string); ok {
		d.writeComment("", description)
	}
	fmt.Fprintf(&d.out, "export type SchemaFile = %s;\n", strings.Join(names, " | "))
	return nil
}

// writeDef writes one $defs entry, a closed object, as an interface.
func (d *declarations) writeDef(name string, schema any) error {
	at := "$defs/" + name
	node, ok := schema.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: a definition must be an object schema", at)
	}
	keys := append([]string{"type", "properties", "required", "additionalProperties", "description", "title"}, valueConstraints...)
	if name == "BehaviorRef" {
		// allOf holds one branch per registered behavior, closing its
		// config; the types leave the config open.
		keys = append(keys, "allOf")
		if err := d.behaviorBranches(node["allOf"], at+"/allOf"); err != nil {
			return err
		}
	}
	if err := onlyKeys(node, at, keys...); err != nil {
		return err
	}
	if node["type"] != "object" || node["additionalProperties"] != false {
		return fmt.Errorf("%s: a definition must be a closed object (type object, additionalProperties false)", at)
	}
	props, _ := node["properties"].(map[string]any)
	required, err := stringList(node["required"], at+"/required")
	if err != nil {
		return err
	}
	for _, key := range required {
		if _, ok := props[key]; !ok {
			return fmt.Errorf("%s: required property %q is not declared", at, key)
		}
	}

	d.out.WriteString("\n")
	if description, ok := node["description"].(string); ok {
		d.writeComment("", description)
	}
	fmt.Fprintf(&d.out, "export interface %s {\n", name)
	policy := false
	for _, key := range sortedKeys(props) {
		propAt := at + "/properties/" + key
		var typ string
		switch {
		case key == "extensions":
			if err := d.openSlot(props[key], propAt); err != nil {
				return err
			}
			d.sawExtensions[name] = true
			typ = "Extensions"
		case name == "Document" && key == "documents":
			if err := d.openSlot(props[key], propAt); err != nil {
				return err
			}
			d.sawDocuments = true
			typ = "Documents"
		case name == "Document" && key == "kind":
			if err := d.kindProperty(props[key], propAt); err != nil {
				return err
			}
			d.sawKind = true
			typ = "SchemaKind"
		case name == "OperationMCP" && key == d.policyKey:
			d.sawPolicy = true
			policy = true
			continue
		case name == "TypeDef" && key == "behaviors":
			if err := d.behaviorList(props[key], propAt); err != nil {
				return err
			}
			d.sawBehaviorList = true
			d.writeComment("  ", "The behaviors the type composes, in the order their checks run. A registry admits the behaviors it registers: the core's, which the engine implements, and its extensions'.")
			typ = "BehaviorRef[]"
		case name == "BehaviorRef" && key == "name":
			if err := d.behaviorName(props[key], propAt); err != nil {
				return err
			}
			d.sawBehaviorName = true
			d.writeComment("  ", "The behavior's registered name: bare for a core behavior, <extension>.<Name> for an extension's. A registry admits the behaviors it registers.")
			typ = "string"
		case name == "BehaviorRef" && key == "config":
			if err := d.behaviorConfig(props[key], propAt); err != nil {
				return err
			}
			d.sawBehaviorConfig = true
			d.writeComment("  ", "The type's config of the behavior, which the registry holds to the behavior's config schema. A config of {} is stored as none.")
			typ = "unknown"
		default:
			typ, err = d.typeOf(props[key], propAt)
			if err != nil {
				return err
			}
		}
		optional := "?"
		if slices.Contains(required, key) {
			optional = ""
		}
		fmt.Fprintf(&d.out, "  %s%s: %s;\n", tsKey(key), optional, typ)
	}
	if policy {
		values, err := d.policyValues(props[d.policyKey], at+"/properties/"+d.policyKey)
		if err != nil {
			return err
		}
		d.writeComment("  ", fmt.Sprintf("The invocation policy of a visible tool, under the key the registry's policy names and with one of its values. The core's key is %s, with the values %s.", d.policyKey, values))
		d.out.WriteString("  [invocationPolicyKey: string]: unknown;\n")
	}
	d.out.WriteString("}\n")
	return nil
}

// typeOf is the TypeScript type of a property or item schema.
func (d *declarations) typeOf(schema any, at string) (string, error) {
	node, ok := schema.(map[string]any)
	if !ok {
		return "", fmt.Errorf("%s: a schema must be an object, got %T", at, schema)
	}
	if ref, ok := node["$ref"]; ok {
		if err := onlyKeys(node, at, "$ref"); err != nil {
			return "", err
		}
		target, _ := ref.(string)
		name, ok := strings.CutPrefix(target, "#/$defs/")
		if _, defined := d.defs[name]; !ok || !defined {
			return "", fmt.Errorf("%s: $ref %v does not name a $defs entry", at, ref)
		}
		return name, nil
	}
	if variants, ok := node["oneOf"]; ok {
		// One of several JSON types: a Go any written with
		// jsonschema:"oneof_type=...", such as a literal env value.
		if err := onlyKeys(node, at, "oneOf", "description", "title", "default"); err != nil {
			return "", err
		}
		list, _ := variants.([]any)
		if len(list) == 0 {
			return "", fmt.Errorf("%s: a oneOf must list schemas", at)
		}
		types := make([]string, len(list))
		for i, variant := range list {
			typ, err := d.typeOf(variant, fmt.Sprintf("%s/oneOf/%d", at, i))
			if err != nil {
				return "", err
			}
			types[i] = typ
		}
		return strings.Join(types, " | "), nil
	}
	if err := onlyKeys(node, at, append([]string{"type", "enum", "const", "items", "additionalProperties", "description", "title", "default"}, valueConstraints...)...); err != nil {
		return "", err
	}
	if value, ok := node["const"]; ok {
		s, isString := value.(string)
		if typ, typed := node["type"]; !isString || typed && typ != "string" {
			return "", fmt.Errorf("%s: a const must be a string", at)
		}
		return tsString(s), nil
	}
	if values, ok := node["enum"]; ok {
		if node["type"] != "string" {
			return "", fmt.Errorf("%s: an enum must be of type string", at)
		}
		list, err := stringList(values, at+"/enum")
		if err != nil || len(list) == 0 {
			return "", fmt.Errorf("%s: an enum must list strings", at)
		}
		literals := make([]string, len(list))
		for i, value := range list {
			literals[i] = tsString(value)
		}
		return strings.Join(literals, " | "), nil
	}
	typ, _ := node["type"].(string)
	if _, ok := node["items"]; ok && typ != "array" {
		return "", fmt.Errorf("%s: items on a schema of type %q", at, typ)
	}
	if _, ok := node["additionalProperties"]; ok && typ != "object" {
		return "", fmt.Errorf("%s: additionalProperties on a schema of type %q", at, typ)
	}
	switch typ {
	case "string":
		return "string", nil
	case "boolean":
		return "boolean", nil
	case "integer", "number":
		return "number", nil
	case "array":
		items, ok := node["items"]
		if !ok {
			return "", fmt.Errorf("%s: an array without items", at)
		}
		elem, err := d.typeOf(items, at+"/items")
		if err != nil {
			return "", err
		}
		if strings.Contains(elem, " | ") {
			elem = "(" + elem + ")"
		}
		return elem + "[]", nil
	case "object":
		additional, ok := node["additionalProperties"]
		if !ok {
			// A map of any JSON value (a Go map[string]any).
			return "{ [key: string]: unknown }", nil
		}
		if _, isSchema := additional.(map[string]any); !isSchema {
			return "", fmt.Errorf("%s: a closed object with no properties is only written for a registry's slots", at)
		}
		value, err := d.typeOf(additional, at+"/additionalProperties")
		if err != nil {
			return "", err
		}
		return "{ [key: string]: " + value + " }", nil
	default:
		return "", fmt.Errorf("%s: unsupported schema type %v", at, node["type"])
	}
}

// openSlot checks that an extensions or documents property is the object
// a registry closes before the types replace it with the open one.
func (d *declarations) openSlot(schema any, at string) error {
	node, ok := schema.(map[string]any)
	if !ok || node["type"] != "object" {
		return fmt.Errorf("%s: expected the object a registry closes", at)
	}
	return nil
}

// kindProperty checks that Document.kind lists the registry's kinds before
// the types open it.
func (d *declarations) kindProperty(schema any, at string) error {
	node, _ := schema.(map[string]any)
	values, err := stringList(node["enum"], at+"/enum")
	if err != nil || !slices.Equal(values, d.kinds) {
		return fmt.Errorf("%s: expected the enum of the registry's kinds %v", at, d.kinds)
	}
	return nil
}

// behaviorList checks that TypeDef.behaviors is a list of BehaviorRef,
// limited to no entry when the registry has no behavior, before the types
// open it.
func (d *declarations) behaviorList(schema any, at string) error {
	node, _ := schema.(map[string]any)
	if err := onlyKeys(node, at, "type", "items", "default", "maxItems"); err != nil {
		return err
	}
	items, _ := node["items"].(map[string]any)
	maxItems, limited := node["maxItems"]
	if node["type"] != "array" || items["$ref"] != "#/$defs/BehaviorRef" ||
		limited != (len(d.behaviors) == 0) || limited && maxItems != float64(0) {
		return fmt.Errorf("%s: expected a list of BehaviorRef, with maxItems 0 when the registry has no behavior", at)
	}
	return nil
}

// behaviorName checks that BehaviorRef.name lists the registry's behaviors
// before the types open it: a string enum of them, or a plain string when
// there are none.
func (d *declarations) behaviorName(schema any, at string) error {
	node, _ := schema.(map[string]any)
	values, err := stringList(node["enum"], at+"/enum")
	_, hasEnum := node["enum"]
	if err != nil || node["type"] != "string" || hasEnum != (len(d.behaviors) > 0) || !slices.Equal(values, d.behaviors) {
		return fmt.Errorf("%s: expected a string enum of the registry's behaviors %v", at, d.behaviors)
	}
	return nil
}

// behaviorBranches checks that BehaviorRef.allOf is what closeBehaviors
// writes: one if/then branch per registered behavior, in the registry's
// order, that holds the config of the behavior the if names; absent when
// the registry has no behavior.
func (d *declarations) behaviorBranches(schema any, at string) error {
	if schema == nil && len(d.behaviors) == 0 {
		return nil
	}
	branches, ok := schema.([]any)
	if !ok || len(branches) != len(d.behaviors) {
		return fmt.Errorf("%s: expected one branch per registered behavior %v", at, d.behaviors)
	}
	for i, branch := range branches {
		branchAt := fmt.Sprintf("%s/%d", at, i)
		node, _ := branch.(map[string]any)
		if err := onlyKeys(node, branchAt, "if", "then"); err != nil {
			return err
		}
		cond, _ := node["if"].(map[string]any)
		props, _ := cond["properties"].(map[string]any)
		name, _ := props["name"].(map[string]any)
		then, _ := node["then"].(map[string]any)
		thenProps, _ := then["properties"].(map[string]any)
		if len(cond) != 1 || len(props) != 1 || len(name) != 1 || name["const"] != d.behaviors[i] || len(thenProps) != 1 || thenProps["config"] == nil {
			return fmt.Errorf("%s: expected the branch that holds the config of behavior %s", branchAt, d.behaviors[i])
		}
	}
	return nil
}

// behaviorConfig checks that BehaviorRef.config is the open value a
// registry closes per behavior, with {} as its default.
func (d *declarations) behaviorConfig(schema any, at string) error {
	node, _ := schema.(map[string]any)
	if err := onlyKeys(node, at, "default"); err != nil {
		return err
	}
	if fallback, ok := node["default"].(map[string]any); !ok || len(fallback) != 0 {
		return fmt.Errorf("%s: expected the default {}", at)
	}
	return nil
}

// policyValues names the core policy's values for the index signature's
// comment.
func (d *declarations) policyValues(schema any, at string) (string, error) {
	node, _ := schema.(map[string]any)
	values, err := stringList(node["enum"], at+"/enum")
	if err != nil || len(values) == 0 {
		return "", fmt.Errorf("%s: expected the enum of the policy's values", at)
	}
	return strings.Join(values, " or "), nil
}

// writeComment writes a JSDoc block, one line per line of text.
func (d *declarations) writeComment(indent, text string) {
	d.out.WriteString(indent + "/**\n")
	for _, line := range strings.Split(wrap(text, 76-len(indent)), "\n") {
		d.out.WriteString(strings.TrimRight(indent+" * "+line, " ") + "\n")
	}
	d.out.WriteString(indent + " */\n")
}

// wrap breaks text into lines of at most width characters at spaces.
func wrap(text string, width int) string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	return strings.Join(append(lines, line), "\n")
}

// valueConstraints are the keywords that narrow a value without changing
// its type: a string's pattern and length, a list's length and uniqueness,
// an object's size and key names. The types leave them to the validators.
var valueConstraints = []string{"pattern", "minLength", "maxLength", "minItems", "uniqueItems", "minProperties", "propertyNames"}

// onlyKeys fails on any keyword of node outside allowed.
func onlyKeys(node map[string]any, at string, allowed ...string) error {
	for _, key := range sortedKeys(node) {
		if !slices.Contains(allowed, key) {
			return fmt.Errorf("%s: unsupported JSON Schema keyword %q", at, key)
		}
	}
	return nil
}

// stringList reads a JSON array of strings; nil reads as an empty list.
func stringList(value any, at string) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: expected an array", at)
	}
	out := make([]string, len(items))
	for i, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s[%d]: expected a string", at, i)
		}
		out[i] = s
	}
	return out, nil
}

var tsIdentifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// tsKey writes a property name, quoted when it is not an identifier.
func tsKey(key string) string {
	if tsIdentifier.MatchString(key) {
		return key
	}
	return tsString(key)
}

// tsString writes a single-quoted TypeScript string literal.
func tsString(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch {
		case r == '\\' || r == '\'':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r > 0x7e:
			fmt.Fprintf(&b, `\u{%x}`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}
