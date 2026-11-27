package registry

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"sort"

	validator "github.com/santhosh-tekuri/jsonschema/v6"

	ir "github.com/parable-work/superschematic/ir"
)

// coreBundleFiles holds the declarations of the bundles the core declares,
// one JSON file each (D103, step 5): Reservation, Barrier, Mailbox and
// Assignment (D104). A bundle defines no rules: it lists the uses it adds
// to a table, the parts, each an existing behavior's, with the wiring
// between them preset, and the settings a table gives, which its parts'
// configs read. The data forms name a bundle by its declaration; the
// TypeScript form's bundles are @superschematic/db's (packages/db's
// bundles.ts), which the reader expands through the checker, and the
// loader holds the two to one expansion.
//
//go:embed bundles/*.bundle.json
var coreBundleFiles embed.FS

// coreBundles are the core's bundle declarations, by file name, in the
// order New registers them.
var coreBundles = []string{"reservation", "barrier", "mailbox", "assignment"}

// SettingKey is the one member of a placeholder in a bundle part's config,
// `{ "$setting": "resource" }`, which the table's value of the setting
// replaces (Bundle.Expand).
const SettingKey = "$setting"

// BundleDeclaration is the shape of a bundle's declaration document (D103,
// step 5).
type BundleDeclaration struct {
	// Name is the bundle's name: a letter A-Z followed by letters and
	// digits, as a core behavior's.
	Name string `json:"name"`
	// Description says what the bundle is for and what its parts do
	// together.
	Description string `json:"description"`
	// SettingsSchema is the JSON Schema of the settings a table gives the
	// bundle: an object schema, closed. A bundle without one takes none.
	SettingsSchema json.RawMessage `json:"settingsSchema,omitempty"`
	// Parts are the uses the bundle adds to a table, in the order the
	// loader adds them: the behaviors it implements first, then those it
	// anchors on fields, as a class that wrote them would list them.
	Parts []BundlePart `json:"parts"`
}

// BundlePart is one use a bundle adds: a registered behavior, anchored on
// the type (implemented) or on the field Field names, with its config, in
// which a placeholder `{ "$setting": "<name>" }` stands for the table's
// value of that setting.
type BundlePart struct {
	// Behavior is the part's registered behavior: Holds.
	Behavior string `json:"behavior"`
	// Field is the anchor field of a part anchored on a field (status);
	// empty for a part the table implements.
	Field string `json:"field,omitempty"`
	// Config is the part's config, its settings as placeholders.
	Config json.RawMessage `json:"config,omitempty"`
}

// Bundle is a registered bundle: its declaration and its compiled
// settings schema.
type Bundle struct {
	BundleDeclaration

	settings         *validator.Schema
	settingsRequired bool
}

// SettingsRequired reports whether a table that implements the bundle
// must give settings: its settings schema rejects {}.
func (b Bundle) SettingsRequired() bool {
	return b.settingsRequired
}

// ValidateSettings checks a table's settings of the bundle against its
// settings schema. Absent settings are checked as {}.
func (b Bundle) ValidateSettings(settings json.RawMessage) error {
	instance := any(map[string]any{})
	if len(settings) > 0 {
		decoded, err := validator.UnmarshalJSON(bytes.NewReader(settings))
		if err != nil {
			return fmt.Errorf("bundle %s settings: %w", b.Name, err)
		}
		instance = decoded
	}
	if b.settings == nil {
		if m, ok := instance.(map[string]any); ok && len(m) == 0 {
			return nil
		}
		return fmt.Errorf("bundle %s takes no settings", b.Name)
	}
	if err := b.settings.Validate(instance); err != nil {
		return fmt.Errorf("bundle %s settings: %w", b.Name, err)
	}
	return nil
}

// Expand returns the uses the bundle adds to a table that gives it
// settings, in its parts' order, each marked with the bundle
// (ir.BehaviorRef.Bundle) and its config canonical: each placeholder
// replaced by the setting's value, and an object's member whose
// placeholder names a setting the table leaves out left out with it. It
// holds the settings to the settings schema first. A placeholder that is
// no object's member and names an absent setting is refused, as is one
// that names no setting the schema declares.
func (b Bundle) Expand(settings json.RawMessage) ([]ir.BehaviorRef, error) {
	if err := b.ValidateSettings(settings); err != nil {
		return nil, err
	}
	values := map[string]any{}
	if len(settings) > 0 {
		if err := json.Unmarshal(settings, &values); err != nil {
			return nil, fmt.Errorf("bundle %s settings: %w", b.Name, err)
		}
	}
	declared := b.settingNames()
	refs := make([]ir.BehaviorRef, 0, len(b.Parts))
	for _, part := range b.Parts {
		ref := ir.BehaviorRef{Name: part.Behavior, Field: part.Field, Implements: part.Field == "", Bundle: b.Name}
		if len(part.Config) > 0 {
			var config any
			if err := json.Unmarshal(part.Config, &config); err != nil {
				return nil, fmt.Errorf("bundle %s part %s config: %w", b.Name, part.label(), err)
			}
			substituted, absent, err := substitute(config, values, declared)
			if err != nil {
				return nil, fmt.Errorf("bundle %s part %s config: %w", b.Name, part.label(), err)
			}
			if absent {
				return nil, fmt.Errorf("bundle %s part %s config: the config is a setting the table leaves out", b.Name, part.label())
			}
			raw, err := json.Marshal(substituted)
			if err != nil {
				return nil, fmt.Errorf("bundle %s part %s config: %w", b.Name, part.label(), err)
			}
			ref.Config = raw
		}
		refs = append(refs, ref)
	}
	if err := ir.CanonicalizeBehaviors(refs); err != nil {
		return nil, fmt.Errorf("bundle %s: %w", b.Name, err)
	}
	for i := range refs {
		if err := refs[i].ExpandTransitions(); err != nil {
			return nil, fmt.Errorf("bundle %s part %s config: %w", b.Name, b.Parts[i].label(), err)
		}
	}
	return refs, nil
}

// label names a part for an error: its behavior, and its field when it is
// anchored on one.
func (p BundlePart) label() string {
	if p.Field != "" {
		return p.Behavior + " on field " + p.Field
	}
	return p.Behavior
}

// settingNames are the settings the settings schema declares, its
// properties, sorted.
func (b Bundle) settingNames() []string {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	_ = json.Unmarshal(b.SettingsSchema, &schema)
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// placeholder reports the setting a value names when it is a placeholder,
// an object whose one member is SettingKey with a string.
func placeholder(value any) (string, bool) {
	m, ok := value.(map[string]any)
	if !ok || len(m) != 1 {
		return "", false
	}
	name, ok := m[SettingKey].(string)
	return name, ok
}

// substitute replaces each placeholder in value with the setting it names.
// absent reports that value is itself a placeholder of a setting values
// leaves out, which its holder leaves out when it is an object's member.
func substitute(value any, values map[string]any, declared []string) (out any, absent bool, err error) {
	if name, ok := placeholder(value); ok {
		if !slices.Contains(declared, name) {
			return nil, false, fmt.Errorf("the placeholder names %q, which is no setting of the bundle", name)
		}
		setting, given := values[name]
		if !given {
			return nil, true, nil
		}
		return setting, false, nil
	}
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, member := range v {
			replaced, absent, err := substitute(member, values, declared)
			if err != nil {
				return nil, false, err
			}
			if absent {
				continue
			}
			out[key] = replaced
		}
		return out, false, nil
	case []any:
		out := make([]any, 0, len(v))
		for i, element := range v {
			replaced, absent, err := substitute(element, values, declared)
			if err != nil {
				return nil, false, err
			}
			if absent {
				return nil, false, fmt.Errorf("element %d is a setting the table leaves out, and a list's element takes a value", i)
			}
			out = append(out, replaced)
		}
		return out, false, nil
	}
	return value, false, nil
}

// registerBundle parses and checks one core bundle declaration and adds
// it. It rejects a declaration that does not decode or has keys the shape
// does not have; a malformed or taken name; a settings schema that does
// not compile or is not a closed object schema; a bundle with no parts; a
// part whose behavior is not registered, that lists a behavior the bundle
// implements twice, or a field twice, that anchors on a field a behavior
// that names no value field, or implements one that does; and a
// placeholder that names no setting of the schema. Whether each part's
// config, its settings given, is one its behavior's config schema takes is
// the loader's, at each expansion, and a test holds the core's bundles to
// it (bundles_test.go).
func (r *Registry) registerBundle(data []byte) error {
	var decl BundleDeclaration
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decl); err != nil {
		return fmt.Errorf("registry: bundle declaration: %w", err)
	}
	if dec.More() {
		return fmt.Errorf("registry: bundle declaration of %s: trailing data after the JSON document", decl.Name)
	}
	if !behaviorBareName.MatchString(decl.Name) {
		return fmt.Errorf("registry: bundle name %q is not %s", decl.Name, behaviorNameDescriptor)
	}
	if _, dup := r.bundles[decl.Name]; dup {
		return fmt.Errorf("registry: bundle %q is already registered", decl.Name)
	}
	b := Bundle{BundleDeclaration: decl}
	if len(decl.SettingsSchema) > 0 {
		if object, closed := closedObjectSchema(decl.SettingsSchema); !object || !closed {
			return fmt.Errorf(`registry: bundle %s settingsSchema must be an object schema with "additionalProperties": false`, decl.Name)
		}
		compiled, err := compileSchema(decl.SettingsSchema, "superschematic://bundles/"+decl.Name+"/settings.json")
		if err != nil {
			return fmt.Errorf("registry: bundle %s settingsSchema: %w", decl.Name, err)
		}
		b.settings = compiled
		b.settingsRequired = compiled.Validate(map[string]any{}) != nil
	}
	if len(decl.Parts) == 0 {
		return fmt.Errorf("registry: bundle %s lists no parts", decl.Name)
	}
	declared := b.settingNames()
	var implemented, fields []string
	for _, part := range decl.Parts {
		behavior, ok := r.behaviors[part.Behavior]
		if !ok {
			return fmt.Errorf("registry: bundle %s part %s: behavior %s is not registered", decl.Name, part.label(), part.Behavior)
		}
		switch {
		case part.Field != "" && behavior.Value == "":
			return fmt.Errorf("registry: bundle %s part %s: behavior %s names no value field, so it is not anchored on a field", decl.Name, part.label(), part.Behavior)
		case part.Field == "" && behavior.Value != "":
			return fmt.Errorf("registry: bundle %s part %s: behavior %s names a value field, so it is anchored on a field", decl.Name, part.label(), part.Behavior)
		case part.Field != "" && slices.Contains(fields, part.Field):
			return fmt.Errorf("registry: bundle %s anchors two parts on field %s", decl.Name, part.Field)
		case part.Field == "" && slices.Contains(implemented, part.Behavior):
			return fmt.Errorf("registry: bundle %s implements %s twice", decl.Name, part.Behavior)
		}
		if part.Field != "" {
			fields = append(fields, part.Field)
		} else {
			implemented = append(implemented, part.Behavior)
		}
		if len(part.Config) > 0 {
			var config any
			if err := json.Unmarshal(part.Config, &config); err != nil {
				return fmt.Errorf("registry: bundle %s part %s config: %w", decl.Name, part.label(), err)
			}
			if _, _, err := substitute(config, map[string]any{}, declared); err != nil {
				return fmt.Errorf("registry: bundle %s part %s config: %w", decl.Name, part.label(), err)
			}
		}
	}
	r.bundles[decl.Name] = b
	return nil
}

// coreBundleDeclarations returns the core's bundle declarations, in
// coreBundles' order. A declaration that does not read is a defect of the
// build, as a core behavior's is.
func coreBundleDeclarations() [][]byte {
	out := make([][]byte, 0, len(coreBundles))
	for _, file := range coreBundles {
		data, err := coreBundleFiles.ReadFile(path.Join("bundles", file+".bundle.json"))
		if err != nil {
			panic("registry: core bundle " + file + ": " + err.Error())
		}
		out = append(out, data)
	}
	return out
}

// Bundle returns the bundle registered under name.
func (r *Registry) Bundle(name string) (Bundle, bool) {
	b, ok := r.bundles[name]
	return b, ok
}

// Bundles returns every registered bundle, sorted by name: the order the
// data-form JSON Schema lists them in.
func (r *Registry) Bundles() []Bundle {
	out := make([]Bundle, 0, len(r.bundles))
	for _, b := range r.bundles {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
