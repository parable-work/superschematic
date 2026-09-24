package schemafile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"weak"

	invopop "github.com/invopop/jsonschema"
	validator "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// schemaResourceName is the meta-schema file name; Naming.MetaSchemaURL
// turns it into the $id the validator registers the schema-file JSON Schema
// under.
const schemaResourceName = "schema-file.json"

// generateDefinition reflects the on-disk document model into an exhaustive
// JSON Schema. The schema is generated from the Go structs so it can never
// drift from the IR: every object sets additionalProperties: false, so
// unknown keys and unknown decorator names are rejected.
//
// The registry supplies what the structs cannot: the schema kinds, the
// extension names and their decorators (closing every "extensions" slot),
// and the sidecar documents (closing "documents").
//
// The root is a oneOf over the multi-definition Document and the
// single-definition forms (a TypeDef, or an Enum / Union / Scalar /
// OperationSet file carrying a "kind" discriminator).
func generateDefinition(reg *registry.Registry) ([]byte, error) {
	r := &invopop.Reflector{Anonymous: true}
	reflected := r.Reflect(&Document{})

	raw, err := json.Marshal(reflected)
	if err != nil {
		return nil, fmt.Errorf("marshaling reflected schema: %w", err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decoding reflected schema: %w", err)
	}

	defs, ok := root["$defs"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("reflected schema has no $defs")
	}

	// Closed value sets the reflector cannot see (named string types are
	// inlined as plain strings).
	patches := []struct {
		def      string
		property string
		values   []string
	}{
		{"Document", "kind", reg.Kinds()},
		{"TypeDef", "role", roleNames()},
		{"ScalarDef", "languagePrimitive", []string{
			string(ir.LanguageString), string(ir.LanguageNumber),
			string(ir.LanguageBoolean), string(ir.LanguageObject),
		}},
		{"FieldDef", "httpMethod", []string{"GET", "POST", "PUT", "PATCH", "DELETE"}},
		{"FieldDef", "paramType", []string{"query"}},
		{"OperationDocs", "lifecycle", []string{
			string(ir.DocsLifecycleDraft), string(ir.DocsLifecycleExperimental),
			string(ir.DocsLifecycleActive), string(ir.DocsLifecycleDeprecated),
			string(ir.DocsLifecycleRetired),
		}},
		{"OperationDocs", "visibility", []string{
			string(ir.DocsVisibilityPublic), string(ir.DocsVisibilityInternal),
			string(ir.DocsVisibilityPreview),
		}},
		{"OperationDocs", "mappingStatus", []string{
			string(ir.DocsMappingStatusMapped), string(ir.DocsMappingStatusUncertain),
		}},
	}
	for _, p := range patches {
		if err := constrainProperty(defs, p.def, p.property, p.values); err != nil {
			return nil, err
		}
	}

	if err := closeExtensionSlots(defs, reg); err != nil {
		return nil, err
	}

	// Single-definition file forms: the definition shape plus a required
	// "kind" discriminator. A single TypeDef file needs no variant because
	// "role" is already a required TypeDef property.
	singles := []struct {
		fileDef string
		baseDef string
		kind    string
	}{
		{"EnumFile", "EnumDef", "Enum"},
		{"UnionFile", "UnionDef", "Union"},
		{"ScalarFile", "ScalarDef", "Scalar"},
		{"OperationSetFile", "OperationSet", "OperationSet"},
	}
	for _, s := range singles {
		variant, err := singleDefVariant(defs, s.baseDef, s.kind)
		if err != nil {
			return nil, err
		}
		defs[s.fileDef] = variant
	}

	delete(root, "$ref")
	n := reg.Naming()
	root["$id"] = n.MetaSchemaURL(schemaResourceName)
	root["title"] = n.SchemaLanguage + " schema file"
	root["description"] = "A " + n.SchemaLanguage + " source file in the JSON or YAML projection: a multi-definition document mirroring the Schema IR, or a single definition object."
	root["oneOf"] = []any{
		map[string]any{"$ref": "#/$defs/Document"},
		map[string]any{"$ref": "#/$defs/TypeDef"},
		map[string]any{"$ref": "#/$defs/EnumFile"},
		map[string]any{"$ref": "#/$defs/UnionFile"},
		map[string]any{"$ref": "#/$defs/ScalarFile"},
		map[string]any{"$ref": "#/$defs/OperationSetFile"},
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// constrainProperty sets an enum constraint on a named property of a $defs entry.
func constrainProperty(defs map[string]any, defName, property string, values []string) error {
	def, ok := defs[defName].(map[string]any)
	if !ok {
		return fmt.Errorf("generated schema is missing the %s definition", defName)
	}
	props, ok := def["properties"].(map[string]any)
	if !ok {
		return fmt.Errorf("generated schema definition %s has no properties", defName)
	}
	prop, ok := props[property].(map[string]any)
	if !ok {
		return fmt.Errorf("generated schema definition %s has no %q property", defName, property)
	}
	enum := make([]any, len(values))
	for i, v := range values {
		enum[i] = v
	}
	prop["enum"] = enum
	return nil
}

// singleDefVariant deep-copies a $defs entry and adds the required "kind"
// discriminator the single-definition file form carries.
func singleDefVariant(defs map[string]any, baseDef, kind string) (map[string]any, error) {
	base, ok := defs[baseDef].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("generated schema is missing the %s definition", baseDef)
	}
	raw, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	var variant map[string]any
	if err := json.Unmarshal(raw, &variant); err != nil {
		return nil, err
	}
	props, ok := variant["properties"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("generated schema definition %s has no properties", baseDef)
	}
	props["kind"] = map[string]any{"const": kind}
	required, _ := variant["required"].([]any)
	variant["required"] = append(required, "kind")
	return variant, nil
}

func roleNames() []string {
	return []string{
		string(ir.RoleDBTable), string(ir.RoleAPIView), string(ir.RoleAPIInput),
		string(ir.RoleEmbeddedStruct), string(ir.RoleAPIOperationSet),
		string(ir.RoleTrait), string(ir.RoleProjection),
	}
}

// extensionSlots lists the $defs that carry an "extensions" property and
// the decorator targets whose specs may appear under each extension there.
// FieldDef serves both fields and operations (an operation is a FieldDef).
// The root Document has no decorator target: no decorator vocabulary exists
// for the schema root, so each registered extension's root value is an open
// object the extension owns.
var extensionSlots = []struct {
	def     string
	targets []registry.DecoratorTarget
}{
	{"Document", nil},
	{"TypeDef", []registry.DecoratorTarget{registry.TargetType}},
	{"FieldDef", []registry.DecoratorTarget{registry.TargetField, registry.TargetOperation}},
	{"OperationSet", []registry.DecoratorTarget{registry.TargetOperationSet}},
}

// closeExtensionSlots replaces every open "extensions" object with one
// closed to the registered extension names, each closed to that extension's
// decorators for the slot's targets (DecoratorSpec.Args, or {"const": true}
// for an argument-less decorator), and "documents" on the Document with an
// object closed to the registered document names and their schemas.
func closeExtensionSlots(defs map[string]any, reg *registry.Registry) error {
	for _, slot := range extensionSlots {
		props, err := propertiesOf(defs, slot.def)
		if err != nil {
			return err
		}
		if _, ok := props["extensions"]; !ok {
			return fmt.Errorf("$defs/%s has no extensions property", slot.def)
		}
		if slot.targets == nil {
			extProps := map[string]any{}
			for _, name := range reg.Extensions() {
				extProps[name] = map[string]any{"type": "object"}
			}
			props["extensions"] = closedObject(extProps)
			continue
		}
		byExt := map[string]map[string]any{}
		for _, name := range reg.Extensions() {
			byExt[name] = map[string]any{}
		}
		for _, spec := range reg.Decorators() {
			if spec.Extension == "" || !slices.Contains(slot.targets, spec.Target) {
				continue
			}
			args := any(map[string]any{"const": true})
			if spec.Args != nil {
				var decoded any
				if err := json.Unmarshal(spec.Args, &decoded); err != nil {
					return fmt.Errorf("decorator @%s Args: %w", spec.Name, err)
				}
				args = decoded
			}
			byExt[spec.Extension][spec.Name] = args
		}
		extProps := map[string]any{}
		for name, decorators := range byExt {
			extProps[name] = closedObject(decorators)
		}
		props["extensions"] = closedObject(extProps)
	}

	docProps, err := propertiesOf(defs, "Document")
	if err != nil {
		return err
	}
	documents := map[string]any{}
	for _, spec := range reg.Documents() {
		var schema any = map[string]any{"type": "object"}
		if spec.Schema != nil {
			var decoded any
			if err := json.Unmarshal(spec.Schema, &decoded); err != nil {
				return fmt.Errorf("document %q Schema: %w", spec.Name, err)
			}
			schema = decoded
		}
		documents[spec.Name] = schema
	}
	docProps["documents"] = closedObject(documents)
	return nil
}

// closedObject is a JSON Schema object admitting exactly the given
// properties, all optional.
func closedObject(properties map[string]any) map[string]any {
	obj := map[string]any{"type": "object", "additionalProperties": false}
	if len(properties) > 0 {
		obj["properties"] = properties
	}
	return obj
}

func propertiesOf(defs map[string]any, defName string) (map[string]any, error) {
	def, ok := defs[defName].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("$defs/%s missing", defName)
	}
	props, ok := def["properties"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("$defs/%s has no properties", defName)
	}
	return props, nil
}

// compiled is the JSON Schema and its per-definition validators for one
// registry, built once per registry composition per process.
type compiled struct {
	once  sync.Once
	bytes []byte
	err   error
	defs  map[string]*validator.Schema
}

// compiledKey identifies a cache entry. The weak pointer lets a registry that
// is no longer referenced (test registries, mostly) be collected together with
// its compiled schema instead of pinning several megabytes each; the extension
// list makes a registry that gains an extension after its first Decode
// recompile rather than validate against the stale core-only schema.
type compiledKey struct {
	reg        weak.Pointer[registry.Registry]
	extensions string
}

var (
	compiledByRegistry sync.Map // compiledKey -> *compiled

	coreOnce     sync.Once
	coreRegistry *registry.Registry
)

// core is the registry the registry-less entry points (Decode, Merge,
// Definition) use: the core kinds and decorators under the default naming.
func core() *registry.Registry {
	coreOnce.Do(func() {
		coreRegistry = registry.New(naming.Default())
	})
	return coreRegistry
}

// orCore lets every *With entry point accept nil for the core registry.
func orCore(reg *registry.Registry) *registry.Registry {
	if reg == nil {
		return core()
	}
	return reg
}

// Definition returns the schema-file JSON Schema for the core registry.
// Callers holding a registry use DefinitionFor.
func Definition() ([]byte, error) {
	return DefinitionFor(core())
}

// DefinitionFor returns the canonical schema-file JSON Schema document for a
// registry (nil for the core one): what `superschematic json-schema` emits and what
// both readers validate against. The result is cached per registry.
func DefinitionFor(reg *registry.Registry) ([]byte, error) {
	c := ensureCompiled(orCore(reg))
	return c.bytes, c.err
}

// ensureCompiled generates the JSON Schema and compiles a validator per
// dispatchable definition, once per registry.
func ensureCompiled(reg *registry.Registry) *compiled {
	key := compiledKey{reg: weak.Make(reg), extensions: strings.Join(reg.Extensions(), ",")}
	entry, loaded := compiledByRegistry.LoadOrStore(key, &compiled{})
	if !loaded {
		runtime.AddCleanup(reg, func(k compiledKey) { compiledByRegistry.Delete(k) }, key)
	}
	c := entry.(*compiled)
	c.once.Do(func() {
		c.bytes, c.err = generateDefinition(reg)
		if c.err != nil {
			return
		}

		resource, err := validator.UnmarshalJSON(bytes.NewReader(c.bytes))
		if err != nil {
			c.err = fmt.Errorf("decoding generated JSON Schema: %w", err)
			return
		}
		compiler := validator.NewCompiler()
		resourceURL := reg.Naming().MetaSchemaURL(schemaResourceName)
		if err := compiler.AddResource(resourceURL, resource); err != nil {
			c.err = fmt.Errorf("registering generated JSON Schema: %w", err)
			return
		}

		c.defs = make(map[string]*validator.Schema)
		for _, defName := range []string{"Document", "TypeDef", "EnumFile", "UnionFile", "ScalarFile", "OperationSetFile"} {
			sch, err := compiler.Compile(resourceURL + "#/$defs/" + defName)
			if err != nil {
				c.err = fmt.Errorf("compiling generated JSON Schema (%s): %w", defName, err)
				return
			}
			c.defs[defName] = sch
		}
	})
	return c
}

// validateAgainstDef validates a JSON payload against one named definition of
// the schema-file JSON Schema. Dispatching to the specific definition (rather
// than the root oneOf) keeps validation errors pointed at the actual shape.
func validateAgainstDef(reg *registry.Registry, data []byte, defName, source string) error {
	c := ensureCompiled(reg)
	if c.err != nil {
		return c.err
	}
	sch, ok := c.defs[defName]
	if !ok {
		return fmt.Errorf("no compiled JSON Schema definition %q", defName)
	}
	instance, err := validator.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	if err := sch.Validate(instance); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	return nil
}
