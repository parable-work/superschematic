package apigen

import (
	"encoding/json"
	"slices"

	ir "github.com/parable-work/superschematic/ir"
)

// EngineInfo is the engine database of an API (D58): the DB service the
// API connects to, its authDb or its one DB-kind dependency, when a type
// of it composes a behavior. A server of the API opens the engine on that
// database, deploys the document its Go types hold as EngineDocument (its
// TypeScript types as engineDocument, its Rust types as ENGINE_DOCUMENT),
// and mounts the engine's routes at Mount, behind the API's identity and
// service callers. The generator package reads it from the database's IR
// and the outputs.engine block of its config, so every server writer reads
// one notion of it.
type EngineInfo struct {
	// Database is the DB service.
	Database string

	// Namespace is the engine namespace the database's engine types are
	// defined and published in: outputs.engine.namespace, else the
	// service's name.
	Namespace string

	// Mount is where a server mounts the engine's routes:
	// outputs.engine.mount, else /engine/<database>.
	Mount string

	// Policy is the access policy, as compact JSON the engine reads:
	// outputs.engine.policy, else "permission".
	Policy json.RawMessage

	// Retention is how long the event log keeps events, as compact JSON
	// the engine reads: outputs.engine.retention, nil when unset, which
	// keeps every event. Every entrypoint opens the engine with it; only
	// the runner, while it holds the runner's lease, prunes.
	Retention json.RawMessage

	// Types are the engine types, by name, sorted.
	Types []string

	// Behaviors are the behaviors the engine types compose, sorted by
	// name, each with what implements it.
	Behaviors []EngineBehavior

	// Derivers are the API's derivers (D74), sorted by name, each with the
	// engine types of the database whose Derived fields it fills.
	Derivers []DeriverInfo
}

// DeriverInfo is a deriver of an API (D74): its @deriver class, the
// subject it acts as and the permissions it holds, the engine types whose
// Derived fields it fills, and how it runs unless an environment changes
// it. The API's generators write a typed method for each derived field of
// its schemas, and its entrypoint hands each stale field to it.
type DeriverInfo struct {
	// Name is the @deriver class.
	Name string

	// Subject is the principal's subject, the deriver's deployable name
	// (ir.DeriverDeployableName), and Permissions the permissions it
	// holds, sorted: each schema's name, which covers every action on it
	// under the permission policy, and each Derived config's permission.
	Subject     string
	Permissions []string

	// Concurrency is how many answers it runs at a time, and BatchSize
	// how many stale fields a pull asks for: the decorator's, or the
	// defaults.
	Concurrency int
	BatchSize   int

	// Schemas are the engine types it fills, in the decorator's order, or
	// sorted by name when it names none.
	Schemas []DerivedSchema
}

// DerivedSchema is an engine type a deriver fills: its type, the
// permission its Derived config names, and its derived fields, sorted by
// name.
type DerivedSchema struct {
	Name       string
	Type       *ir.TypeDef
	Permission string
	Fields     []DerivedField
}

// DerivedField is one derived field of an engine type: the own field a
// model fills, the own fields it is derived from, in the config's order,
// and the config's instruction and model label.
type DerivedField struct {
	Name        string
	Field       *ir.FieldDef
	From        []*ir.FieldDef
	Instruction string
	Model       string
}

// Deriver returns the deriver named name, or nil.
func (e *EngineInfo) Deriver(name string) *DeriverInfo {
	if e == nil {
		return nil
	}
	for i := range e.Derivers {
		if e.Derivers[i].Name == name {
			return &e.Derivers[i]
		}
	}
	return nil
}

// EngineBehavior is a behavior an engine type composes, as the engine
// document lists it: its registered name, the extension that declares it
// (empty for the core's) and the npm package whose implementation runs it.
type EngineBehavior struct {
	Name      string
	Extension string
	Package   string
}

// EnginePermissionActions are the actions the engine's permission policy
// asks of a type, in the order a catalog lists them: <Type>.<action>.
var EnginePermissionActions = []string{"define", "publish", "read", "write"}

// PermissionPolicy reports whether the engine opens with its permission
// policy, which allows an action on a type to a caller holding
// <Type>.<action>: the permissions the API's catalog lists for it.
func (e *EngineInfo) PermissionPolicy() bool {
	if e == nil {
		return false
	}
	var shorthand string
	return json.Unmarshal(e.Policy, &shorthand) == nil && shorthand == "permission"
}

// CoreBehaviorsOnly reports whether every behavior the engine types compose
// is one the engine's core archive runs: a core behavior of
// @superschematic/engine. One of the work queue's, or of an extension,
// runs only in an engine a project builds with it.
func (e *EngineInfo) CoreBehaviorsOnly(corePackage string) bool {
	return e == nil || !slices.ContainsFunc(e.Behaviors, func(b EngineBehavior) bool {
		return b.Extension != "" || b.Package != corePackage
	})
}

// HasEngine reports whether a server of the API opens the engine on its
// database (D58).
func (o *APIOutput) HasEngine() bool {
	return o != nil && o.Engine != nil
}
