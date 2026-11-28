// Package triageops adds the inbound path's triage to an API as routes
// (#746, D157). An API marks a class with no members
// @triageOperations(Party, { refersTo? }), from @superschematic/api, naming
// a @party table of its database, whose @conversations tables hold the
// messages the inbound path keeps in triage:
//
//	@triageOperations(Contact, { refersTo: ContactView })
//	@requirePermission(["parties.triage"])
//	export abstract class Triage {}
//
// Once the set passes its checks (verify.CheckTriageOperations), and before
// the rest of the verification pass, the loader fills it here with
// ir.TriageOperationNames, in that order: GET triage, the held messages
// newest first, a page at a time (D60), those of one kind of channel when
// the query names it; GET triage/{message}, one message that was held, its
// sender, channel, body and files; POST triage/{message}/settle, which
// settles it onto a row of the party table; and POST
// triage/{message}/dismiss, which closes its triage. Each takes the set's
// clause as its own, its permissions the class's @requirePermission or
// ir.TriagePermission when it names none, so no route is open; generated
// @docs; and the marker ir.FieldDef.TriageOperation. The two reads are
// tools (triage, triage_message); the settle and the dismiss stay routes,
// their @mcp hidden, since each decides who a sender is. The input and
// result types, and the enums they name, are named Triage...: a held
// message, its files, the settle's input and what a settle did, as the
// runtime's inbound path answers them. So every check the pass makes of an
// authored operation holds for these, and the OpenAPI document, the SDKs,
// the permission catalog and the tool documents read them as any other;
// only the TypeScript server routes them, to a generated handler that runs
// them through the inbound path as the request's caller.
package triageops

import (
	"fmt"
	"slices"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The scalars an added type names beside the primitives.
const (
	uuidScalar     = "Identity.UUID"
	int64Scalar    = "Generic.Int64"
	dateTimeScalar = "Temporal.DateTime"
)

// The names of the types and enums the expansion adds.
const (
	MessageType       = "TriageMessage"
	AttachmentType    = "TriageAttachment"
	StateEnum         = "TriageState"
	SettleInputType   = "TriageSettleInput"
	TrustEnum         = "TriageTrust"
	SettledType       = "TriageSettled"
	SettleOutcomeEnum = "TriageOutcome"
)

// route is where one operation is served: its method and its path below
// the API's prefix.
type route struct {
	method, path string
}

// messagePath is the route of one held message.
const messagePath = ir.TriagePath + "/{" + ir.TriageMessageParam + "}"

// routes are the operations' routes, by operation.
var routes = map[string]route{
	ir.TriageOpList:       {"GET", ir.TriagePath},
	ir.TriageOpMessage:    {"GET", messagePath},
	ir.TriageOpSettleOnto: {"POST", messagePath + "/settle"},
	ir.TriageOpDismiss:    {"POST", messagePath + "/dismiss"},
}

// RestPath is the route of the operation op, below the API's prefix.
func RestPath(op string) string { return routes[op].path }

// Method is the HTTP method of the operation op.
func Method(op string) string { return routes[op].method }

// Reads reports whether op writes nothing: its route is a GET.
func Reads(op string) bool { return routes[op].method == "GET" }

// NamesMessage reports whether op's route names one held message.
func NamesMessage(op string) bool { return op != ir.TriageOpList }

// handles are the MCP handles of the reads; the settle and the dismiss are
// no tools.
var handles = map[string]string{
	ir.TriageOpList:    "triage",
	ir.TriageOpMessage: "triage_message",
}

// Handle is the MCP handle of op, "" for one that is no tool.
func Handle(op string) string { return handles[op] }

// IsTool reports whether op is a tool: the two reads.
func IsTool(op string) bool { return handles[op] != "" }

// RouteOnlyReason is the @mcp hidden reason of an operation that is no
// tool.
const RouteOnlyReason = "Settling or dismissing a held message stays a route: it decides who the sender is, which is a person's to decide in the app (#746)."

// RequireServiceToolReason is the @mcp hidden reason of a read the
// expansion adds to set, a set whose @requireService admits only calling
// services: no end user's agent can call its tool (D37).
func RequireServiceToolReason(set string) string {
	return "Only the services that @requireService on " + set + " admits call it (D37)."
}

// docs are each operation's @docs title and capability.
var docs = map[string][2]string{
	ir.TriageOpList:       {"Held messages", "triage.list"},
	ir.TriageOpMessage:    {"Held message", "triage.read"},
	ir.TriageOpSettleOnto: {"Settle onto a party", "triage.settle-onto"},
	ir.TriageOpDismiss:    {"Dismiss", "triage.dismiss"},
}

// descriptions say what each operation does, as its comment, its @docs
// description and its tool's.
var descriptions = map[string]string{
	ir.TriageOpList:       "Lists the inbound messages held in triage, newest first, a page at a time: those from an unknown sender, a lapsed identity, an address two parties share that no thread settles, or a party with no grant. channel keeps those that came on one kind of channel (sms, email, chat).",
	ir.TriageOpMessage:    "Reads one inbound message that was held in triage, open or settled: its sender's address, its channel, its body and its files, and what became of it.",
	ir.TriageOpSettleOnto: "Settles a message held in triage onto a party row: the sender's address becomes the party's identity on the channel, entered by hand (filed now, at that lower trust) or verified by a code sent to it (filed once the address answers), and the message is filed as if it had come from the party, a run request made when the party holds an active grant. An address that is a user's is refused.",
	ir.TriageOpDismiss:    "Closes a message's triage and files it nowhere: it stays on its conversation.",
}

// Permissions are the permissions every operation of set needs: the
// class's @requirePermission, or ir.TriagePermission when it names none
// and the class is not @publicRoute, which the checks refuse.
func Permissions(set *ir.OperationSet) []string {
	if len(set.Permissions) > 0 {
		return slices.Clone(set.Permissions)
	}
	return []string{ir.TriagePermission}
}

// Added is a type or an enum the expansion adds: its kind ("type" or
// "enum") and its name.
type Added struct {
	Kind string
	Name string
}

// Plan is what the expansion adds for the API's @triageOperations set: its
// operations, and the types and enums they take and return, in the order
// they are first named.
type Plan struct {
	// Messages is the database's @conversations("messages") table and
	// Party the set's party table.
	Messages   string
	Party      string
	Operations []*ir.FieldDef
	Types      []*ir.TypeDef
	Enums      []*ir.EnumDef
	// Added lists every type and enum, in the order they are first named.
	Added []Added
	// Scalars are the scalar names the operations and types name.
	Scalars []string
}

// PlanSet works out what the expansion adds for set, the
// @triageOperations set of api whose party table db, the API's database,
// declares. It assumes the set passed verify.CheckTriageOperations: db
// declares the conversations and the party table.
func PlanSet(set *ir.OperationSet, db *ir.Schema, reg *registry.Registry) (*Plan, error) {
	cfg := set.TriageOperations
	messages := db.ConversationsTable(ir.MessagesPart)
	if messages == nil {
		return nil, fmt.Errorf("triageops: %s: %s declares no @conversations(%q) table", set.Name, db.Name, ir.MessagesPart)
	}
	party := db.Types[cfg.Party]
	if party == nil || party.Party == nil {
		return nil, fmt.Errorf("triageops: %s: %s declares no @party table %s", set.Name, db.Name, cfg.Party)
	}
	p := &Plan{Messages: messages.Name, Party: party.Name}
	b := &builder{plan: p, named: map[string]bool{}}
	policy := reg.ToolInvocationPolicy()
	marker := ir.TriageOperation{Database: db.Name, Messages: messages.Name, Party: party.Name, Attachments: messages.Attachments != nil}
	for _, op := range ir.TriageOperationNames {
		p.Operations = append(p.Operations, b.operationOf(set, marker, op, policy))
	}
	return p, nil
}

// operationOf is the operation the expansion adds for op, named by it and
// marked with it.
func (b *builder) operationOf(set *ir.OperationSet, marker ir.TriageOperation, op string, policy registry.ToolInvocationPolicy) *ir.FieldDef {
	marker.Operation = op
	out := &ir.FieldDef{
		Name:        op,
		Comment:     descriptions[op],
		Required:    true,
		HTTPMethod:  Method(op),
		RestPath:    RestPath(op),
		Auth:        set.Auth,
		Permissions: Permissions(set),
		Docs: &ir.OperationDocs{
			Title:         docs[op][0],
			Description:   descriptions[op],
			Capability:    docs[op][1],
			Lifecycle:     ir.DocsLifecycleActive,
			Visibility:    ir.DocsVisibilityPublic,
			MappingStatus: ir.DocsMappingStatusMapped,
		},
		Origin:          ir.OriginTriage,
		TriageOperation: &marker,
	}
	switch {
	case !IsTool(op):
		out.MCP = &ir.OperationMCP{Hidden: true, HiddenReason: RouteOnlyReason}
	default:
		out.MCP = &ir.OperationMCP{Handle: Handle(op), Invocation: ir.MCPInvocation{Key: policy.Key, Value: policy.Default}}
		if callers := ir.EffectiveServiceCallers(set, out); callers != nil && callers.Mode == ir.ServiceCallersRequire {
			// Only a calling service reaches the route, so no end user's
			// agent can call the tool (D37).
			out.MCP = &ir.OperationMCP{Hidden: true, HiddenReason: RequireServiceToolReason(set.Name)}
		}
	}
	if Reads(op) {
		out.Docs.ReplayMode = ir.DocsReplayModeReadOnly
	}
	if NamesMessage(op) {
		b.useScalar(uuidScalar)
		// The message refers to a held message, as the list lists them
		// (D61), so a UI links each row of the list to its page.
		out.Arguments = append(out.Arguments, &ir.ArgumentDef{
			Name:       ir.TriageMessageParam,
			Comment:    "The id of the message.",
			TypeRef:    ir.TypeRef{Name: uuidScalar},
			Required:   true,
			References: &ir.ReferenceDef{Type: MessageType},
		})
	}
	switch op {
	case ir.TriageOpList:
		out.Arguments = append(out.Arguments, &ir.ArgumentDef{
			Name:    ir.TriageChannelArgument,
			Comment: "Keeps the messages that came on this kind of channel: sms, email, chat.",
			TypeRef: ir.TypeRef{Name: "string"},
			IsQuery: true,
		})
		out.Page = &ir.OperationPage{DefaultLimit: ir.TriageDefaultLimit, MaxLimit: ir.TriageMaxLimit}
		out.TypeRef = b.message(set)
		out.TypeRef.IsArray = true
	case ir.TriageOpMessage, ir.TriageOpDismiss:
		out.TypeRef = b.message(set)
	case ir.TriageOpSettleOnto:
		out.Arguments = append(out.Arguments, &ir.ArgumentDef{Name: ir.TriageInputArgument, TypeRef: b.settleInput(set), Required: true})
		out.TypeRef = b.settled()
	}
	return out
}

// Expand fills the @triageOperations set of api with the operations of
// the inbound path's triage, read from db, the API's database, and adds the
// types and enums they take and return (#746). It assumes the set passed
// verify.CheckTriageOperations, and does nothing for an API without one.
// It returns the scalar names the added types use, so the caller can
// hydrate any api did not hold; one only db declares is copied from it. It
// may run again on its own output.
func Expand(api, db *ir.Schema, reg *registry.Registry) ([]string, error) {
	var sets []*ir.OperationSet
	for _, set := range api.OperationSets {
		if set.IsTriageOperations() {
			sets = append(sets, set)
		}
	}
	if len(sets) == 0 || db == nil {
		return nil, nil
	}
	var scalars []string
	for _, set := range sets {
		plan, err := PlanSet(set, db, reg)
		if err != nil {
			return nil, err
		}
		set.Operations = plan.Operations
		if api.Types == nil {
			api.Types = map[string]*ir.TypeDef{}
		}
		if api.Enums == nil {
			api.Enums = map[string]*ir.EnumDef{}
		}
		for _, td := range plan.Types {
			td.Owner = api.Name
			api.Types[td.Name] = td
		}
		for _, ed := range plan.Enums {
			ed.Owner = api.Name
			api.Enums[ed.Name] = ed
		}
		for _, name := range plan.Scalars {
			if !slices.Contains(scalars, name) {
				scalars = append(scalars, name)
			}
		}
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

// cloneScalar is a copy of def that shares no map or slice with it.
func cloneScalar(def *ir.ScalarDef) *ir.ScalarDef {
	copied := *def
	if def.TypeMappings != nil {
		copied.TypeMappings = make(map[string]string, len(def.TypeMappings))
		for k, v := range def.TypeMappings {
			copied.TypeMappings[k] = v
		}
	}
	if def.ReservedWords != nil {
		copied.ReservedWords = append([]string(nil), def.ReservedWords...)
	}
	return &copied
}

// builder adds the types and enums of the set's operations to its plan,
// each once.
type builder struct {
	plan  *Plan
	named map[string]bool
}

// claim records name as added, and reports whether it was not before.
func (b *builder) claim(kind, name string) bool {
	if b.named[name] {
		return false
	}
	b.named[name] = true
	b.plan.Added = append(b.plan.Added, Added{Kind: kind, Name: name})
	return true
}

// useScalar records a scalar the added types name; a primitive is none.
func (b *builder) useScalar(name string) {
	switch name {
	case "string", "number", "boolean":
		return
	}
	if !slices.Contains(b.plan.Scalars, name) {
		b.plan.Scalars = append(b.plan.Scalars, name)
	}
}

// field is a member of an added type that holds a scalar or a primitive.
func (b *builder) field(name, typeName string, required bool, comment string) *ir.FieldDef {
	b.useScalar(typeName)
	return &ir.FieldDef{Name: name, Comment: comment, TypeRef: ir.TypeRef{Name: typeName}, Required: required, Origin: ir.OriginTriage}
}

// fieldOf is a member of an added type that holds another added type or
// enum.
func (b *builder) fieldOf(name string, ref ir.TypeRef, required bool, comment string) *ir.FieldDef {
	return &ir.FieldDef{Name: name, Comment: comment, TypeRef: ref, Required: required, Origin: ir.OriginTriage}
}

// addType adds the type name of role with fields, once.
func (b *builder) addType(name, comment string, role ir.Role, display *ir.TypeDisplay, fields ...*ir.FieldDef) ir.TypeRef {
	if b.claim("type", name) {
		b.plan.Types = append(b.plan.Types, &ir.TypeDef{Name: name, Comment: comment, Role: role, Origin: ir.OriginTriage, Display: display, Fields: fields})
	}
	return ir.TypeRef{Name: name}
}

// addEnum adds the enum name of the string values, once.
func (b *builder) addEnum(name, comment string, values ...string) ir.TypeRef {
	if b.claim("enum", name) {
		ed := &ir.EnumDef{Name: name, Comment: comment, Origin: ir.OriginTriage}
		for _, value := range values {
			ed.Values = append(ed.Values, ir.EnumValueDef{Name: pascal(value), SerializedAs: value})
		}
		b.plan.Enums = append(b.plan.Enums, ed)
	}
	return ir.TypeRef{Name: name}
}

// pascal is a lower-case word with its first letter upper case.
func pascal(word string) string {
	if word == "" {
		return word
	}
	return string(word[0]-'a'+'A') + word[1:]
}

// attachment is a file a held message came with.
func (b *builder) attachment() ir.TypeRef {
	return b.addType(AttachmentType, "A file a held message came with, in the inbound path's bucket.", ir.RoleEmbeddedStruct, nil,
		b.field("id", "string", true, "The file's id."),
		b.field("name", "string", true, "The file's name."),
		b.field("mediaType", "string", true, "The file's media type."),
		b.field("size", int64Scalar, true, "The file's size in bytes."),
	)
}

// message is an inbound message that was held in triage.
func (b *builder) message(set *ir.OperationSet) ir.TypeRef {
	state := b.addEnum(StateEnum, "Where a held message stands: open while it is held in triage, closed once someone settled or dismissed it.", "open", "closed")
	files := b.fieldOf("attachments", b.attachment(), true, "The files it came with, oldest first.")
	files.TypeRef.IsArray = true
	display := &ir.TypeDisplay{Noun: "Held message", Plural: "Held messages", TitleField: "fromAddress", Icon: "inbox", SummaryFields: []string{"fromAddress", "channel", "at", "body", "disposition"}}
	return b.addType(MessageType, "An inbound message the inbound path held in triage: who sent it, on what, what it said and what became of it.", ir.RoleEmbeddedStruct, display,
		b.field("id", uuidScalar, true, "The message's id."),
		b.field("channel", "string", true, "The kind of channel it came on: sms, email, chat."),
		b.field("slot", "string", false, "The provider slot that received it."),
		titled(b.field("fromAddress", "string", true, "The address it came from, normalised."), "From"),
		titled(b.field("toAddress", "string", false, "The address it was sent to."), "To"),
		b.field("subject", "string", false, "Its subject, for a channel that shows one."),
		b.field("body", "string", true, "Its text as it came."),
		titled(b.field("at", dateTimeScalar, true, "When it came."), "Received"),
		b.fieldOf("state", state, true, "open while it is held in triage, closed once settled or dismissed."),
		b.field("disposition", "string", false, "What became of it: unknown, lapsed, timeline, run, verifying."),
		b.field("detail", "string", false, "Why it went where it went."),
		b.field("partyTable", "string", false, "The table of the party it was filed as from, once settled onto one."),
		b.reference(b.field("party", uuidScalar, false, "The key of that party's row."), set),
		b.field("trust", "string", false, "How the sender's identity was trusted when it was filed: verified, or entered by hand."),
		titled(b.field("closedBy", "string", false, "Who settled or dismissed it."), "Settled by"),
		titled(b.field("closedAt", dateTimeScalar, false, "When."), "Settled at"),
		files,
	)
}

// settleInput is the body of a settle: the party row and how its address
// is trusted.
func (b *builder) settleInput(set *ir.OperationSet) ir.TypeRef {
	trust := b.addEnum(TrustEnum, "How a settled sender's address is trusted: entered by hand, at that lower trust from now, or verified once it answers a code sent to it.", "entered", "verified")
	party := b.reference(b.field("party", uuidScalar, true, "The key of the "+set.TriageOperations.Party+" row the message is settled onto; the settler reads it."), set)
	return b.addType(SettleInputType, "The party row a held message is settled onto, and how its sender's address is trusted.", ir.RoleAPIInput, nil,
		party,
		titled(b.fieldOf("trust", trust, true, "entered files the message now, at the lower trust of an address entered by hand; verified sends a code to the address and files it once the address answers."), "Trust the sender's address"),
	)
}

// reference is fd, a party's key, referring to the set's refersTo type
// when it names one (D61).
func (b *builder) reference(fd *ir.FieldDef, set *ir.OperationSet) *ir.FieldDef {
	if target := set.TriageOperations.RefersTo; target != "" {
		fd.References = &ir.ReferenceDef{Type: target}
	}
	return fd
}

// titled is fd with the label a form or a table shows for it.
func titled(fd *ir.FieldDef, title string) *ir.FieldDef {
	fd.Title = title
	return fd
}

// settled is what a settle did.
func (b *builder) settled() ir.TypeRef {
	outcome := b.addEnum(SettleOutcomeEnum, "What a settle did with a message: filed it for a run of the party's active grant, filed it on the party's timeline, or held it until the address answers its code.", "run", "timeline", "verifying")
	return b.addType(SettledType, "What a settle did with a held message.", ir.RoleEmbeddedStruct, nil,
		b.fieldOf("outcome", outcome, true, "run: filed, and a run request made under the party's active grant; timeline: filed on the party's timeline, the party holding no grant; verifying: held until the address answers the code sent to it."),
		b.field("message", uuidScalar, true, "The message's id."),
		b.field("party", uuidScalar, true, "The key of the party row."),
		b.field("grant", uuidScalar, false, "The grant a run request was made under."),
	)
}
