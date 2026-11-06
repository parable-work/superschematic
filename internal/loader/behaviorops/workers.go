package behaviorops

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// EventMethod is the name of the method of the API's Workers that the
// behavior worker worker hands the event kind of the use whose member is
// use to (D104): the worker's class, the member and the kind, in Pascal
// case (ProblemWakeUpsSchedulesRun). TypeScript writes it in camel case,
// Rust in snake case.
func EventMethod(worker, use, kind string) string {
	return worker + codegen.ToPascalCase(use) + codegen.ToPascalCase(kind)
}

// EventType is the name of the API type of the event kind of the use whose
// member is use on typeName, as the Workers method takes it (D104):
// ProblemSchedulesRunEvent.
func EventType(typeName, use, kind string) string {
	return Base(typeName, use) + codegen.ToPascalCase(kind) + "Event"
}

// EventPayloadType is the name of the API type of the payload of the event
// kind of the use whose member is use on typeName (D104):
// ProblemSchedulesRunPayload.
func EventPayloadType(typeName, use, kind string) string {
	return Base(typeName, use) + codegen.ToPascalCase(kind) + "Payload"
}

// WorkerEvents are the events the behavior worker worker of td hands to the
// API's Workers (D104): for each table use of td whose behavior's
// declaration lists events, in list order, each event in declaration order,
// with the Workers method that takes it and the API types of the event and
// its payload. A use whose behavior reg does not hold is left out, which
// the verification pass refuses.
func WorkerEvents(worker string, td *ir.TypeDef, reg *registry.Registry) []ir.BehaviorWorkerEvent {
	var out []ir.BehaviorWorkerEvent
	for _, ref := range td.TableBehaviors() {
		behavior, ok := reg.Behavior(ref.Name)
		if !ok {
			continue
		}
		use := behaviorlayout.UseMember(ref)
		for _, event := range behavior.Events {
			out = append(out, ir.BehaviorWorkerEvent{
				Use:         ref.UseID(),
				Member:      use,
				Behavior:    ref.Name,
				Kind:        event.Kind,
				Method:      EventMethod(worker, use, event.Kind),
				Event:       EventType(td.Name, use, event.Kind),
				Payload:     EventPayloadType(td.Name, use, event.Kind),
				Description: event.Description,
			})
		}
	}
	return out
}

// ExpandWorkers makes each @behaviorWorker of api an ordinary worker of the
// queues its table's behaviors keep (D103): its Queues are the deadline
// queue, the change queue and the event queue (D104) the database's load
// added for the table (ir.TypeDef.BehaviorQueue), in that order, each when
// db holds it, its Queue the first of them, and its Origin
// ir.OriginBehavior. From there it is a worker as D53 has it: a
// deployable of kind worker named after the API and the class, its
// settings, its process under `stack dev`, its image and its worker pool;
// only its handlers are the API's generator's, which run each claimed
// deadline and change through the behaviors' core and hand each event to
// the project's Workers method.
//
// A worker whose table's behaviors emit events (WorkerEvents) gets its
// Events, and api gets, for each, the type of its payload, as the
// declaration's payloadSchema narrows under the use's config
// (behaviortypes.Narrowing.Events, ProblemSchedulesRunPayload), the enums
// it names by the narrowing's aliases (ProblemSchedulesKind), and the type
// of the event the method takes (ProblemSchedulesRunEvent): the row's key,
// the command's time and the payload. An enum the operations' expansion
// added for the same alias is kept when its values are the same, and one of
// other values refused. ExpandWorkers returns the scalar names the added
// types use, which it copies from db when db declares them, as Expand
// does. It assumes the workers passed verify.CheckBehaviorWorkers, so db,
// the API's database, holds each queue and the names are free, and it may
// run again on its own output.
func ExpandWorkers(api, db *ir.Schema, reg *registry.Registry) ([]string, error) {
	if api == nil || db == nil {
		return nil, nil
	}
	var scalars []string
	for _, worker := range api.Workers {
		if worker == nil || worker.BehaviorWorker == nil {
			continue
		}
		var queues []string
		for _, kind := range []string{ir.BehaviorQueueDeadlines, ir.BehaviorQueueChanges, ir.BehaviorQueueEvents} {
			if queue := db.BehaviorQueueOf(worker.BehaviorWorker.Type, kind); queue != nil {
				queues = append(queues, queue.Name)
			}
		}
		if len(queues) > 0 {
			worker.Queues = queues
			worker.Queue = queues[0]
			worker.Origin = ir.OriginBehavior
		}
		td := db.Types[worker.BehaviorWorker.Type]
		if td == nil || db.BehaviorQueueOf(td.Name, ir.BehaviorQueueEvents) == nil {
			continue
		}
		worker.Events = WorkerEvents(worker.Name, td, reg)
		if len(worker.Events) == 0 {
			worker.Events = nil
			continue
		}
		plan, err := planEvents(worker, db, td, reg)
		if err != nil {
			return nil, err
		}
		if err := addEventTypes(api, worker, plan); err != nil {
			return nil, err
		}
		for _, name := range plan.Scalars {
			if !slices.Contains(scalars, name) {
				scalars = append(scalars, name)
			}
		}
	}
	if len(scalars) == 0 {
		return nil, nil
	}
	if api.Scalars == nil {
		api.Scalars = map[string]*ir.ScalarDef{}
	}
	for _, name := range scalars {
		if api.Scalars[name] != nil {
			continue
		}
		def := &ir.ScalarDef{Name: name}
		if declared := db.Scalars[name]; declared != nil {
			def = cloneScalar(declared)
		}
		api.Scalars[name] = def
	}
	return scalars, nil
}

// planEvents works out the types of worker's events, the events of td's
// uses (worker.Events): for each, its payload's type and the enums it
// names, then the event's type.
func planEvents(worker *ir.Worker, db *ir.Schema, td *ir.TypeDef, reg *registry.Registry) (*Plan, error) {
	key := keyField(td)
	if key == nil {
		return nil, fmt.Errorf("behaviorops: @behaviorWorker %s: %s has no @key field", worker.Name, td.Name)
	}
	p := &Plan{}
	b := &builder{plan: p, named: map[string]bool{}}
	b.useScalar(key.TypeRef.Name)
	narrowed := map[string]behaviortypes.Narrowing{}
	for _, event := range worker.Events {
		ref, ok := useOf(td, event.Use)
		if !ok {
			return nil, fmt.Errorf("behaviorops: @behaviorWorker %s: %s has no use %s", worker.Name, td.Name, event.Use)
		}
		declaration, ok := reg.Behavior(ref.Name)
		if !ok {
			return nil, fmt.Errorf("behaviorops: @behaviorWorker %s: %s's %s is the behavior %s, which is not registered", worker.Name, td.Name, ref.UseID(), ref.Name)
		}
		base := Base(td.Name, event.Member)
		n, done := narrowed[event.Use]
		if !done {
			var err error
			n, err = behaviortypes.Narrow(declaration, ref.Config, behaviortypes.Target{Schema: db.Name, Base: base, Document: db, Instance: td})
			if err != nil {
				return nil, fmt.Errorf("behaviorops: @behaviorWorker %s: %s's %s, %s: %w", worker.Name, td.Name, event.Member, behaviortypes.An(ref.Name), err)
			}
			narrowed[event.Use] = n
		}
		// The aliases' docs name the use as the operations' do (Problem's
		// for a use the table implements, Order's status for a field's).
		owner := td.Name + "'s " + event.Member
		if !ref.FieldAnchored() {
			owner = td.Name + "'s"
		}
		b.aliases = map[string]behaviortypes.Alias{}
		for _, alias := range n.Aliases {
			alias.Doc = strings.ReplaceAll(alias.Doc, behaviortypes.An(base)+"'s", owner)
			b.aliases[alias.Name] = alias
		}
		b.use, b.operation = event.Member, event.Kind
		payload, _ := n.Events[event.Kind].(map[string]any)
		properties, _ := payload["properties"].(map[string]any)
		comment := fmt.Sprintf("What %s's %s says in its event %s.", td.Name, event.Use, event.Kind)
		if len(properties) > 0 {
			b.object(payload, properties, event.Payload, comment, ir.RoleEmbeddedStruct)
		} else if b.claim("type", event.Payload) {
			p.Types = append(p.Types, &ir.TypeDef{Name: event.Payload, Comment: comment, Role: ir.RoleEmbeddedStruct, Origin: ir.OriginBehavior})
		}
		b.useScalar(int64Scalar)
		if b.claim("type", event.Event) {
			p.Types = append(p.Types, &ir.TypeDef{
				Name:    event.Event,
				Comment: fmt.Sprintf("An event %s of %s's %s, as the behavior worker %s hands it to its handler (D104).", event.Kind, td.Name, event.Use, worker.Name),
				Role:    ir.RoleEmbeddedStruct,
				Origin:  ir.OriginBehavior,
				Fields: []*ir.FieldDef{
					{Name: "key", Comment: "The key of the " + td.Name + " row the event is about.", TypeRef: ir.TypeRef{Name: key.TypeRef.Name}, Required: true, Origin: ir.OriginBehavior},
					{Name: "at", Comment: "When the command that emitted it ran, in epoch milliseconds.", TypeRef: ir.TypeRef{Name: int64Scalar}, Required: true, Origin: ir.OriginBehavior},
					{Name: "payload", Comment: "What it says.", TypeRef: ir.TypeRef{Name: event.Payload}, Required: true, Origin: ir.OriginBehavior},
				},
			})
		}
	}
	return p, nil
}

// addEventTypes adds the types and enums plan holds to api, owned by it.
// An enum api holds already, one the operations' expansion added for the
// same alias, is kept when its values are the plan's, and refused when
// they are not.
func addEventTypes(api *ir.Schema, worker *ir.Worker, plan *Plan) error {
	if api.Types == nil {
		api.Types = map[string]*ir.TypeDef{}
	}
	if api.Enums == nil {
		api.Enums = map[string]*ir.EnumDef{}
	}
	for _, ed := range plan.Enums {
		if held := api.Enums[ed.Name]; held != nil {
			if !slices.Equal(enumValues(held), enumValues(ed)) {
				return fmt.Errorf("behaviorops: @behaviorWorker %s: the payload of an event of %s names the enum %s (%s), which %s holds with the values %s (D104)", worker.Name, worker.BehaviorWorker.Type, ed.Name, strings.Join(enumValues(ed), ", "), api.Name, strings.Join(enumValues(held), ", "))
			}
			continue
		}
		ed.Owner = api.Name
		api.Enums[ed.Name] = ed
	}
	for _, td := range plan.Types {
		td.Owner = api.Name
		api.Types[td.Name] = td
	}
	return nil
}

// enumValues are an enum's values as JSON writes them.
func enumValues(ed *ir.EnumDef) []string {
	out := make([]string, 0, len(ed.Values))
	for _, value := range ed.Values {
		out = append(out, value.SerializedAs)
	}
	return out
}

// useOf is td's table use whose id is id.
func useOf(td *ir.TypeDef, id string) (ir.BehaviorRef, bool) {
	for _, ref := range td.TableBehaviors() {
		if ref.UseID() == id {
			return ref, true
		}
	}
	return ir.BehaviorRef{}, false
}
