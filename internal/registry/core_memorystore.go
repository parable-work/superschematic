package registry

import (
	"encoding/json"
	"fmt"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// pkgMemoryStore is the authoring package of the MemoryStore kind's
// decorators, @channel and @key (D75).
const pkgMemoryStore = "@superschematic/memory-store"

// memoryStoreGenerator is the MemoryStore kind's one generator: each
// language's typed definitions that outputs.types names, and the
// store's inventory.json. It is registered by generator.RegisterCore, with
// the other core generators.
const memoryStoreGenerator = "memorystore"

// memoryStoreKind is the core MemoryStore kind (D75): a Redis or Valkey
// server the replicas of a stack share, which holds no fact. Its schema
// declares each channel and key it carries, an @channel or @key class
// whose fields are the parts of its name; the class is a declaration, no
// type. It has a sentinel, since the configs that list it import its
// handle, and it may import the object types its channels carry from its
// dependencies.
func memoryStoreKind() KindSpec {
	return KindSpec{
		Name:       string(ir.SchemaKindMemoryStore),
		StructRole: ir.RoleEmbeddedStruct,
		ForbiddenPackages: map[string]bool{
			pkgAPI:   true,
			pkgDB:    true,
			pkgStack: true,
		},
		// A channel's payload is an object type of a dependency: a General
		// service's, an API's or a DB's, whose types package each language
		// already generates.
		AllowedReferences: map[string]bool{
			string(ir.SchemaKindGeneral): true,
			string(ir.SchemaKindAPI):     true,
			string(ir.SchemaKindDB):      true,
		},
		Pipeline: []string{memoryStoreGenerator},
		Verify:   verifyMemoryStore,
	}
}

// The JSON Schemas of @channel's and @key's arguments. Each admits the
// keys of the other, a channel's TTLs and a key's payload, so that Apply
// refuses them with the reason, past an @ts-expect-error.
var (
	channelArgs = json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"payload": {"type": "object", "required": ["class"], "additionalProperties": false, "properties": {"class": {"type": "string", "minLength": 1}}},
			"name": {"type": "string", "minLength": 1},
			"ttlSeconds": {},
			"maxTtlSeconds": {}
		}
	}`)
	keyArgs = json.RawMessage(`{
		"type": "object",
		"required": ["use"],
		"additionalProperties": false,
		"properties": {
			"use": {"enum": ["counter", "claim"]},
			"ttlSeconds": {"type": "integer"},
			"maxTtlSeconds": {"type": "integer"},
			"name": {"type": "string", "minLength": 1},
			"payload": {}
		}
	}`)
)

// memoryStoreDecorators returns @channel and @key, from
// @superschematic/memory-store and only in MemoryStore schemas. Each reads
// its class into a MemoryStoreDeclaration, which Apply records in
// Schema.MemoryStoreDeclarations; the TypeScript reader leaves the class
// out of Types. The class's fields, which the walker resolves before any
// decorator applies, are the declaration's parts. A payload is a class the
// schema imports, which the argument evaluator reads as a class reference
// and records as an import.
func memoryStoreDecorators() []DecoratorSpec {
	kinds := []string{string(ir.SchemaKindMemoryStore)}
	spec := func(name string, args json.RawMessage, optional bool, read func(*ir.TypeDef, map[string]any) (*ir.MemoryStoreDeclaration, error)) DecoratorSpec {
		return DecoratorSpec{
			Name: name, Packages: []string{pkgMemoryStore}, Target: TargetType, Kinds: kinds, Args: args,
			optionalArg: optional,
			Apply: func(n Node, args []any, _ Site) error {
				var obj map[string]any
				if len(args) == 1 && args[0] != nil {
					m, ok := args[0].(map[string]any)
					if !ok {
						return ArgErrorf(0, "@%s takes an options object", name)
					}
					obj = m
				}
				decl, err := read(n.Type, obj)
				if err != nil {
					return err
				}
				if other := n.Schema.MemoryStoreDeclaration(decl.Name); other != nil {
					return fmt.Errorf("@%s class %s is named %s, like class %s; give one of them another name", name, n.Type.Name, decl.Name, other.Symbol)
				}
				n.Schema.MemoryStoreDeclarations = append(n.Schema.MemoryStoreDeclarations, decl)
				ir.SortMemoryStoreDeclarations(n.Schema.MemoryStoreDeclarations)
				return nil
			},
		}
	}
	return []DecoratorSpec{
		spec("channel", channelArgs, true, channelOf),
		spec("key", keyArgs, false, keyOf),
	}
}

// IsMemoryStoreDecorator reports whether the decorator name, from package
// pkg, is @channel or @key: a class that carries one is a declaration of a
// MemoryStore schema and no type.
func IsMemoryStoreDecorator(pkg, name string) bool {
	return pkg == pkgMemoryStore && (name == "channel" || name == "key")
}

// memoryStoreDeclaration starts the declaration td's class makes: its
// symbol, its doc, its file and its parts, with its name the class's in
// kebab case unless the decorator's name sets it.
func memoryStoreDeclaration(td *ir.TypeDef, decorator string, kind ir.MemoryStoreKind, obj map[string]any) (*ir.MemoryStoreDeclaration, error) {
	if td.Extends != "" || len(td.Implements) > 0 {
		return nil, fmt.Errorf("@%s class %s extends or implements another class; a declaration's parts are its own fields", decorator, td.Name)
	}
	decl := &ir.MemoryStoreDeclaration{
		Name:   ir.MemoryStoreDeclarationName(td.Name),
		Symbol: td.Name,
		Kind:   kind,
		Doc:    td.Comment,
		File:   td.Owner,
	}
	explicit := false
	if name, ok := obj["name"].(string); ok {
		decl.Name, explicit = name, true
	}
	if err := ir.CheckMemoryStoreDeclarationName(decl.Name, explicit); err != nil {
		if explicit {
			return nil, ArgErrorf(0, "@%s %s: %v", decorator, td.Name, err)
		}
		return nil, fmt.Errorf("@%s class %s: %v", decorator, td.Name, err)
	}
	for _, f := range td.Fields {
		if f == nil {
			continue
		}
		decl.Parts = append(decl.Parts, ir.MemoryStorePart{Name: f.Name, Type: f.TypeRef})
		if err := checkMemoryStorePartField(td.Name, f); err != nil {
			return nil, err
		}
	}
	return decl, nil
}

// checkMemoryStorePartField refuses a field that cannot be a part: one
// that is optional or null, a list or a map. Whether its type is a scalar
// whose wire form is a string or an integer is the verification pass's,
// which reads the scalars and the enums of the schema's dependencies.
func checkMemoryStorePartField(class string, f *ir.FieldDef) error {
	switch {
	case !f.Required:
		return fmt.Errorf("field %s.%s is optional; a part is a required scalar, since every part is a segment of the name", class, f.Name)
	case f.TypeRef.IsArray || f.TypeRef.IsMap:
		return fmt.Errorf("field %s.%s is a list or a map; a part is a required scalar, since every part is a segment of the name", class, f.Name)
	}
	return nil
}

// channelOf reads @channel({ payload, name }) on td.
func channelOf(td *ir.TypeDef, obj map[string]any) (*ir.MemoryStoreDeclaration, error) {
	for key := range obj {
		switch key {
		case "payload", "name":
		case "ttlSeconds", "maxTtlSeconds":
			return nil, ArgErrorf(0, "@channel %s sets %s; a channel's messages are not kept, so it has no TTL", td.Name, key)
		default:
			return nil, ArgErrorf(0, "@channel has no option %q; it takes payload and name", key)
		}
	}
	decl, err := memoryStoreDeclaration(td, "channel", ir.MemoryStoreChannel, obj)
	if err != nil {
		return nil, err
	}
	if value, ok := obj["payload"]; ok && value != nil {
		class, err := DecodeClassRef(value)
		if err != nil {
			return nil, ArgErrorf(0, "@channel payload is the object type each message holds, imported from a dependency of the store's schema: %v", err)
		}
		decl.Payload = &ir.TypeRef{Name: class}
	}
	return decl, nil
}

// keyOf reads @key({ use, ttlSeconds, maxTtlSeconds, name }) on td.
func keyOf(td *ir.TypeDef, obj map[string]any) (*ir.MemoryStoreDeclaration, error) {
	if obj == nil {
		return nil, ArgErrorf(0, "@key takes { use, ttlSeconds } or { use, maxTtlSeconds }")
	}
	for key := range obj {
		switch key {
		case "use", "ttlSeconds", "maxTtlSeconds", "name":
		case "payload":
			return nil, ArgErrorf(0, "@key %s sets payload; a key holds a count or a claim, never a value", td.Name)
		default:
			return nil, ArgErrorf(0, "@key has no option %q; it takes use, ttlSeconds, maxTtlSeconds and name", key)
		}
	}
	var kind ir.MemoryStoreKind
	switch use, _ := obj["use"].(string); use {
	case "counter":
		kind = ir.MemoryStoreCounter
	case "claim":
		kind = ir.MemoryStoreClaim
	default:
		return nil, ArgErrorf(0, "@key use is %q; it is counter or claim", use)
	}
	decl, err := memoryStoreDeclaration(td, "key", kind, obj)
	if err != nil {
		return nil, err
	}
	for key, dst := range map[string]*int{"ttlSeconds": &decl.TTLSeconds, "maxTtlSeconds": &decl.MaxTTLSeconds} {
		value, ok := obj[key]
		if !ok {
			continue
		}
		n, ok := value.(float64)
		if !ok || n != float64(int(n)) {
			return nil, ArgErrorf(0, "@key %s %s is %v; it is a whole number of seconds", td.Name, key, value)
		}
		*dst = int(n)
	}
	if err := checkMemoryStoreTTL(decl); err != nil {
		return nil, ArgErrorf(0, "@key %s: %v", td.Name, err)
	}
	return decl, nil
}

// checkMemoryStoreTTL checks a declaration's TTLs: a key sets exactly one
// of ttlSeconds and maxTtlSeconds, from 1 second to 7 days, since every
// key expires; a channel sets neither.
func checkMemoryStoreTTL(decl *ir.MemoryStoreDeclaration) error {
	ttl, maxTTL := decl.TTLSeconds != 0, decl.MaxTTLSeconds != 0
	if !decl.Kind.IsKey() {
		if ttl || maxTTL {
			return fmt.Errorf("a channel's messages are not kept, so it sets neither ttlSeconds nor maxTtlSeconds")
		}
		return nil
	}
	switch {
	case ttl && maxTTL:
		return fmt.Errorf("sets both ttlSeconds and maxTtlSeconds; a key sets one: its TTL, or the longest a call may give it")
	case !ttl && !maxTTL:
		return fmt.Errorf("sets neither ttlSeconds nor maxTtlSeconds; every key expires, so a key sets its TTL or the longest a call may give it")
	}
	for _, t := range []struct {
		name  string
		value int
	}{{"ttlSeconds", decl.TTLSeconds}, {"maxTtlSeconds", decl.MaxTTLSeconds}} {
		if t.value != 0 && (t.value < 1 || t.value > ir.MemoryStoreMaxTTLSeconds) {
			return fmt.Errorf("%s is %d; it is from 1 to %d seconds (7 days)", t.name, t.value, ir.MemoryStoreMaxTTLSeconds)
		}
	}
	return nil
}

// CheckMemoryStoreDeclaration checks one declaration in every form: its
// name, its kind, its TTLs, that only a channel has a payload, and that
// its parts are named, each once. Whether a part's type is a scalar and a
// payload an object type of a dependency is the verification pass's.
func CheckMemoryStoreDeclaration(decl *ir.MemoryStoreDeclaration) error {
	if decl.Symbol == "" {
		return fmt.Errorf("declaration %s has no symbol, the class's name its definitions are named after", decl.Name)
	}
	if err := ir.CheckMemoryStoreDeclarationName(decl.Name, decl.Name != ir.MemoryStoreDeclarationName(decl.Symbol)); err != nil {
		return err
	}
	switch decl.Kind {
	case ir.MemoryStoreChannel, ir.MemoryStoreCounter, ir.MemoryStoreClaim:
	default:
		return fmt.Errorf("kind is %q; a declaration is a channel, a counter or a claim", decl.Kind)
	}
	if err := checkMemoryStoreTTL(decl); err != nil {
		return err
	}
	if decl.Payload != nil {
		switch {
		case decl.Kind.IsKey():
			return fmt.Errorf("a %s has a payload; a key holds a count or a claim, never a value", decl.Kind)
		case decl.Payload.Name == "":
			return fmt.Errorf("payload names no type")
		case decl.Payload.IsArray || decl.Payload.IsMap:
			return fmt.Errorf("payload %s is a list or a map; a message is one object", decl.Payload.Name)
		}
	}
	seen := map[string]bool{}
	for _, part := range decl.Parts {
		switch {
		case part.Name == "":
			return fmt.Errorf("a part has no name")
		case seen[part.Name]:
			return fmt.Errorf("part %s is given twice", part.Name)
		case part.Type.IsArray || part.Type.IsMap:
			return fmt.Errorf("part %s is a list or a map; a part is a required scalar", part.Name)
		case part.Type.Name == "":
			return fmt.Errorf("part %s has no type", part.Name)
		}
		seen[part.Name] = true
		switch part.Format {
		case "", ir.MemoryStorePartString, ir.MemoryStorePartInteger, ir.MemoryStorePartBoolean, ir.MemoryStorePartEnum:
		default:
			return fmt.Errorf("part %s has format %q; want string, integer, boolean or enum", part.Name, part.Format)
		}
	}
	return nil
}

// verifyMemoryStore is the MemoryStore kind's verification (D75), in every
// form: every declaration checks (CheckMemoryStoreDeclaration), no two
// share a name, none takes a reserved name, and the schema declares
// nothing else: no type, enum, union, scalar of its own, operation set,
// job or worker. A part's scalar and a payload's type are checked against
// the dependencies by the verification pass (verify.checkMemoryStore).
func verifyMemoryStore(schema *ir.Schema, r VerifyReporter) {
	seen := map[string]string{}
	for _, decl := range schema.MemoryStoreDeclarations {
		if decl == nil {
			continue
		}
		where := fmt.Sprintf("declaration %s (class %s)", decl.Name, decl.Symbol)
		if decl.Reserved {
			if decl.Name != ir.MemoryStoreRateLimit {
				r.Errorf(decl.File, "%s is marked reserved; only the core's %s is", where, ir.MemoryStoreRateLimit)
			}
			continue
		}
		if decl.Name == ir.MemoryStoreRateLimit {
			r.Errorf(decl.File, "%s takes the name %s, which the core declares in every %s schema for the shared rate limit; give it another name", where, ir.MemoryStoreRateLimit, ir.SchemaKindMemoryStore)
			continue
		}
		if err := CheckMemoryStoreDeclaration(decl); err != nil {
			r.Errorf(decl.File, "%s: %v", where, err)
			continue
		}
		if other, dup := seen[decl.Name]; dup {
			r.Errorf(decl.File, "%s and class %s are both named %s; each declaration takes its own name", where, other, decl.Name)
			continue
		}
		seen[decl.Name] = decl.Symbol
	}
	var declared []string
	for name := range schema.Types {
		declared = append(declared, "type "+name)
	}
	for name := range schema.Enums {
		declared = append(declared, "enum "+name)
	}
	for name := range schema.Unions {
		declared = append(declared, "union "+name)
	}
	for _, set := range schema.OperationSets {
		if set != nil {
			declared = append(declared, "operation set "+set.Name)
		}
	}
	for _, job := range schema.Jobs {
		if job != nil {
			declared = append(declared, "job "+job.Name)
		}
	}
	for _, worker := range schema.Workers {
		if worker != nil {
			declared = append(declared, "worker "+worker.Name)
		}
	}
	sort.Strings(declared)
	for _, what := range declared {
		r.Errorf("", "%s is a %s service, whose schema declares only channels and keys, each an @channel or @key class from %s, with a channel's payload a type of a dependency (%s)", schema.Name, ir.SchemaKindMemoryStore, pkgMemoryStore, what)
	}
}
