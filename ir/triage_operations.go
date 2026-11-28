package ir

// The inbound path's triage as routes (#746, D157). An API serves the
// messages its database's inbound path holds in triage by marking a class
// with no methods @triageOperations(Party, { ... }), from
// @superschematic/api, naming the @party table a held message is settled
// onto:
//
//	@triageOperations(Contact, { refersTo: ContactView })
//	@requirePermission(["parties.triage"])
//	export abstract class Triage {}
//
// The reader records the decorator as OperationSet.TriageOperations and
// the class's @auth, @requirePermission and @publicRoute as the set's
// Auth, Permissions and Public. The loader reads the API's database
// (Schema.Database), whose @conversations tables hold the messages, and
// fills the set with [TriageOperationNames]: the held messages, a page at
// a time and by channel; one message, its sender, body and files; settling
// one onto a party row; and dismissing one. Each has Origin OriginTriage
// and FieldDef.TriageOperation set, and the loader adds the types and
// enums they take and return, each named Triage..., with the same Origin.
// Every route needs the set's permissions, the triage permission
// (TriagePermission) when the class names none, so no route is open. The
// OpenAPI document, the SDKs, the permission catalog and the tool
// documents read them as any other operation. A TypeScript server routes
// an operation whose TriageOperation is set to a generated handler that
// runs it through the inbound path (Deps.inbound) as the request's
// caller; a Go or Rust server keeps it a method of the implementation.

// OriginTriage is the Origin of each operation, type, field and enum the
// loader adds for a @triageOperations set, so the schema writer writes the
// decorator in their place.
const OriginTriage = "triage"

// TriagePermission is the permission every operation of a
// @triageOperations set needs when its class names none: the inbound
// path's own triage permission (createInbound's triagePermission default,
// D157).
const TriagePermission = "parties.triage"

// TriagePath is the collection the operations are routed under, below the
// API's prefix: GET triage, GET triage/{message}.
const TriagePath = "triage"

// TriageMessageParam is the path parameter that names a held message, its
// id.
const TriageMessageParam = "message"

// TriageChannelArgument is the list's query argument that keeps the
// messages that came on one kind of channel (sms, email, chat).
const TriageChannelArgument = "channel"

// TriageInputArgument is the argument a settle's body travels as.
const TriageInputArgument = "input"

// The page of the list of held messages (D60).
const (
	TriageDefaultLimit = 20
	TriageMaxLimit     = 100
)

// The operations a @triageOperations set serves.
const (
	// TriageOpList lists the messages held in triage, newest first, a page
	// at a time, those of one kind of channel when asked.
	TriageOpList = "triage"
	// TriageOpMessage reads one message that was held in triage, open or
	// settled: its sender, channel, body and files.
	TriageOpMessage = "triageMessage"
	// TriageOpSettleOnto settles a held message onto a party row: the
	// sender's address becomes the party's identity, entered by hand or
	// verified by a code, and the message is filed as the party's
	// (inbound.settleOnto, #606).
	TriageOpSettleOnto = "settleOnto"
	// TriageOpDismiss closes a held message's triage and files it nowhere
	// (inbound.settle).
	TriageOpDismiss = "dismiss"
)

// TriageOperationNames are the operations a @triageOperations set serves,
// in the order the loader adds them.
var TriageOperationNames = []string{TriageOpList, TriageOpMessage, TriageOpSettleOnto, TriageOpDismiss}

// TriageOperationsConfig is @triageOperations on an operation set.
type TriageOperationsConfig struct {
	// Party is the @party table of the API's database (Schema.Database) a
	// held message is settled onto, by its name as the API imports it.
	Party string `json:"party" yaml:"party"`

	// RefersTo is the API's type the settle's party refers to (D61): a
	// type one of the API's operations lists, so a UI offers the party
	// rows to pick from. Empty refers to none, and a UI takes the party's
	// key as text or from the app's choices.
	RefersTo string `json:"refersTo,omitempty" yaml:"refersTo,omitempty"`
}

// TriageOperation marks an operation the loader added for a
// @triageOperations set: which operation of the inbound path's triage it
// runs.
type TriageOperation struct {
	// Database is the DB service whose @conversations tables hold the
	// messages, the API's database.
	Database string `json:"database" yaml:"database"`

	// Messages is the type name of its @conversations("messages") table.
	Messages string `json:"messages" yaml:"messages"`

	// Party is the @party table the settle settles a message onto.
	Party string `json:"party" yaml:"party"`

	// Attachments reports whether the messages table holds files
	// (@attachments), which a message's read lists.
	Attachments bool `json:"attachments,omitempty" yaml:"attachments,omitempty"`

	// Operation is the operation's name, one of [TriageOperationNames].
	Operation string `json:"operation" yaml:"operation"`
}

// IsTriageOperations reports whether the set is @triageOperations, whose
// operations the loader adds.
func (s *OperationSet) IsTriageOperations() bool {
	return s != nil && s.TriageOperations != nil
}
