// Package rowfeatureops adds a table's references and files to an API as
// routes (#723, D154). An API marks a class with no members
// @rowFeatureOperations(Type, { ... }), from @superschematic/api, naming a
// table of its database that keeps references (@references), holds files
// (@attachments) or that a reference may point at:
//
//	@rowFeatureOperations(Issue, { permissions: { beginUpload: ["issues.upload"] } })
//	@auth
//	export abstract class IssueLinks {}
//
// Once the set passes its checks (verify.CheckRowFeatureOperations), and
// before the rest of the verification pass, the loader fills it here with
// one operation for each of ir.RowFeatureOperationNames the table offers
// and the set's options admit, in that order, each routed under the table
// and its key as a table's behavior operations are (GET issue/{id}/backlinks,
// POST issue/{id}/files/{file}/complete), taking the row's key, a file's id
// where its path names one and an input of the operation's body, and
// returning its result, with generated @docs, the set's clause as its own
// (the permissions the set names for the operation in place of the
// class's), and the marker ir.FieldDef.RowFeatureOperation. The reads, the
// row's references, backlinks and files, and the add of a reference are
// tools (issue_backlinks, issue_add_reference); the rest stay routes, their
// @mcp hidden: an upload's bytes go to the bucket, which no tool call
// carries. The input and result types, and the enums they name, are named
// Row... and shared by every set of the API: a reference, a backlink, a
// file, an upload and a download as the runtime's references and
// attachments answer them. So every check the pass makes of an authored
// operation holds for these, and the OpenAPI document, the SDKs, the
// permission catalog and the tool documents read them as any other; only
// the servers route them, to a generated handler that runs them through
// the database's references and attachments as the request's caller.
package rowfeatureops

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The scalars an added type names beside the primitives and the table's
// key.
const (
	int64Scalar    = "Generic.Int64"
	dateTimeScalar = "Temporal.DateTime"
)

// The names of the types and enums the expansion adds, shared by every
// set of the API.
const (
	RowRefType          = "RowRef"
	ReferenceType       = "RowReference"
	ReferenceInputType  = "RowReferenceInput"
	RemovedType         = "RowReferencesRemoved"
	BacklinkType        = "RowBacklink"
	BacklinksType       = "RowBacklinks"
	BacklinkViaEnum     = "RowBacklinkVia"
	AttachmentType      = "RowAttachment"
	AttachmentStateEnum = "RowAttachmentState"
	ProcessingEnum      = "RowProcessingState"
	BeginInputType      = "RowAttachmentBeginInput"
	UploadType          = "RowAttachmentUpload"
	BegunType           = "RowAttachmentBegun"
	DownloadType        = "RowAttachmentDownload"
	FileInputType       = "RowAttachmentFileInput"
)

// route is where one operation is served: its method and its path below
// the row's (issue/{id}).
type route struct {
	method, path string
}

// routes are the operations' routes, by operation.
var routes = map[string]route{
	ir.RowOpReferences:       {"GET", "/references"},
	ir.RowOpAddReference:     {"POST", "/references"},
	ir.RowOpRemoveReference:  {"POST", "/references/remove"},
	ir.RowOpBacklinks:        {"GET", "/backlinks"},
	ir.RowOpAttachments:      {"GET", "/files"},
	ir.RowOpBeginUpload:      {"POST", "/files"},
	ir.RowOpCompleteUpload:   {"POST", "/files/{" + ir.RowFeatureFileParam + "}/complete"},
	ir.RowOpDownload:         {"GET", "/files/{" + ir.RowFeatureFileParam + "}"},
	ir.RowOpFileAttachment:   {"POST", "/files/{" + ir.RowFeatureFileParam + "}/file"},
	ir.RowOpRemoveAttachment: {"DELETE", "/files/{" + ir.RowFeatureFileParam + "}"},
}

// RowPath is the route every operation of the table's row sits under,
// below the API's prefix: the table in kebab case and its key
// (issue/{id}), as a table's behavior operations are routed.
func RowPath(typeName, key string) string {
	return codegen.ToKebabCase(typeName) + "/{" + key + "}"
}

// RestPath is the route of the operation op of typeName's row, below the
// API's prefix (issue/{id}/files/{file}/complete).
func RestPath(typeName, key, op string) string {
	return RowPath(typeName, key) + routes[op].path
}

// Method is the HTTP method of the operation op.
func Method(op string) string {
	return routes[op].method
}

// Reads reports whether op writes nothing: its route is a GET.
func Reads(op string) bool {
	return routes[op].method == "GET"
}

// NamesFile reports whether op's route names one of the row's files.
func NamesFile(op string) bool {
	return strings.Contains(routes[op].path, "{"+ir.RowFeatureFileParam+"}")
}

// Handle is the MCP handle of typeName's op (issue_add_reference).
func Handle(typeName, op string) string {
	return codegen.ToSnakeCase(typeName) + "_" + codegen.ToSnakeCase(op)
}

// Capability is the @docs capability of typeName's op
// (issue.add-reference).
func Capability(typeName, op string) string {
	return codegen.ToKebabCase(typeName) + "." + codegen.ToKebabCase(op)
}

// Title is the @docs title of typeName's op (Issue: add reference).
func Title(typeName, op string) string {
	return typeName + ": " + strings.ReplaceAll(codegen.ToKebabCase(op), "-", " ")
}

// IsTool reports whether op is a tool: the reads of a row's references,
// backlinks and files, and the add of a reference. The rest stay routes.
func IsTool(op string) bool {
	switch op {
	case ir.RowOpReferences, ir.RowOpAddReference, ir.RowOpBacklinks, ir.RowOpAttachments:
		return true
	}
	return false
}

// RouteOnlyReason is the @mcp hidden reason of an operation that is no
// tool.
func RouteOnlyReason(op string) string {
	switch ir.RowFeatureOf(op) {
	case ir.RowFeatureAttachments:
		return "A file's upload, download and filing stay routes: its bytes go to and from the bucket through a signed URL, which no tool call carries (#723)."
	}
	return "A reference's removal stays a route: a tool adds references and reads them (#723)."
}

// RequireServiceToolReason is the @mcp hidden reason of an operation the
// expansion adds to set, a set whose @requireService admits only calling
// services: no end user's agent can call its tool (D37).
func RequireServiceToolReason(set string) string {
	return "Only the services that @requireService on " + set + " admits call it (D37)."
}

// descriptions say what each operation does, as its comment, its @docs
// description and its tool's.
var descriptions = map[string]string{
	ir.RowOpReferences:       "Lists the references the row holds, oldest first, each to a row the caller can read.",
	ir.RowOpAddReference:     "Makes the row point at another row, of any table it may point at, with a role (none unless given), once per target and role; answers the reference, the one there when it was there.",
	ir.RowOpRemoveReference:  "Stops the row pointing at another row with the role, or with any role when none is given; answers how many references it removed.",
	ir.RowOpBacklinks:        "Lists the rows that point at the row, across every table, through a reference or a ref block of a message, each one the caller can read, most recent first. Only the owner of a row whose audience is not everyone hears how many rows it cannot see point at it.",
	ir.RowOpAttachments:      "Lists the row's ready files, oldest first.",
	ir.RowOpBeginUpload:      "Starts an upload of a file to the row: a pending file, held to the table's bounds with the row's other files and uploads, and the URL the browser puts the file's bytes to, straight to the bucket, before it expires.",
	ir.RowOpCompleteUpload:   "Checks an upload's bytes in the bucket against the size and SHA-256 its begin declared, and makes the file ready; a file whose bytes differ is removed.",
	ir.RowOpDownload:         "Signs a URL that downloads one of the row's files until it expires, and answers the file, whose preview this first view asks.",
	ir.RowOpFileAttachment:   "Files one of the row's files onto another row, which may be of another table, with no second upload: a copy that names the same object, or with move the file itself. When the table keeps references that may point at the other row, the row then points at it with the role (filed unless given), so the other row lists it among its backlinks.",
	ir.RowOpRemoveAttachment: "Removes one of the row's files; its object is released once no file names it.",
}

// Permissions are the permissions the operation op of set needs: the list
// the set's permissions names for it, or the class's @requirePermission.
func Permissions(set *ir.OperationSet, op string) []string {
	if own, ok := set.RowFeatureOperations.Permissions[op]; ok {
		return slices.Clone(own)
	}
	return slices.Clone(set.Permissions)
}

// Offers reports whether td, a table of db, offers op: a reference's
// operations on a @references table, the backlinks on a table a reference
// may point at in a database that keeps references, and a file's on an
// @attachments table.
func Offers(db *ir.Schema, td *ir.TypeDef, op string) bool {
	switch ir.RowFeatureOf(op) {
	case ir.RowFeatureReferences:
		return td.RowReferences != nil
	case ir.RowFeatureBacklinks:
		return db.KeepsReferences() && td.IsReferenceTarget()
	case ir.RowFeatureAttachments:
		return td.Attachments != nil
	}
	return false
}

// Added is a type or an enum the expansion adds: its kind ("type" or
// "enum") and its name.
type Added struct {
	Kind string
	Name string
}

// Plan is what the expansion adds for one @rowFeatureOperations set: its
// operations, and the types and enums they take and return, in the order
// they are first named.
type Plan struct {
	// Type is the table and Key its key field.
	Type string
	Key  string
	// Bucket is the Bucket service the file operations read.
	Bucket     string
	Operations []*ir.FieldDef
	Types      []*ir.TypeDef
	Enums      []*ir.EnumDef
	// Added lists every type and enum, in the order they are first named.
	Added []Added
	// Scalars are the scalar names the operations and types name.
	Scalars []string
}

// BucketOf is the Bucket service the set's file operations read: the one
// its options name, or the API's one bucket; "" when the API lists none,
// or several and the options name none.
func BucketOf(set *ir.OperationSet, api *ir.Schema) string {
	if set.RowFeatureOperations.Bucket != "" {
		return set.RowFeatureOperations.Bucket
	}
	if len(api.Buckets) == 1 {
		return api.Buckets[0].Name
	}
	return ""
}

// keyField is td's one @key field, or nil.
func keyField(td *ir.TypeDef) *ir.FieldDef {
	var key *ir.FieldDef
	for _, fd := range td.Fields {
		if fd != nil && fd.Key {
			if key != nil {
				return nil
			}
			key = fd
		}
	}
	return key
}

// PlanSet works out what the expansion adds for set, a
// @rowFeatureOperations set of api whose table db, the API's database,
// declares. It assumes the set passed verify.CheckRowFeatureOperations: the
// table is db's, keyed by one field, and offers each operation the
// options list.
func PlanSet(set *ir.OperationSet, api, db *ir.Schema, reg *registry.Registry) (*Plan, error) {
	cfg := set.RowFeatureOperations
	td := db.Types[cfg.Type]
	if td == nil {
		return nil, fmt.Errorf("rowfeatureops: %s: %s declares no type %s", set.Name, db.Name, cfg.Type)
	}
	key := keyField(td)
	if key == nil {
		return nil, fmt.Errorf("rowfeatureops: %s: %s has no one @key field", set.Name, td.Name)
	}
	p := &Plan{Type: td.Name, Key: key.Name, Bucket: BucketOf(set, api)}
	b := &builder{plan: p, named: map[string]bool{}}
	b.useScalar(key.TypeRef.Name)
	policy := reg.ToolInvocationPolicy()
	for _, op := range ir.RowFeatureOperationNames {
		if !cfg.Serves(op) || !Offers(db, td, op) {
			continue
		}
		p.Operations = append(p.Operations, b.operationOf(set, db, td, key, op, policy))
	}
	return p, nil
}

// operationOf is the operation the expansion adds for op, named by it and
// marked with it.
func (b *builder) operationOf(set *ir.OperationSet, db *ir.Schema, td *ir.TypeDef, key *ir.FieldDef, op string, policy registry.ToolInvocationPolicy) *ir.FieldDef {
	out := &ir.FieldDef{
		Name:       op,
		Comment:    descriptions[op],
		Required:   true,
		HTTPMethod: Method(op),
		RestPath:   RestPath(td.Name, key.Name, op),
		Arguments: []*ir.ArgumentDef{{
			Name:     key.Name,
			Comment:  "The key of the " + td.Name + " row the operation runs on.",
			TypeRef:  ir.TypeRef{Name: key.TypeRef.Name},
			Required: true,
		}},
		Auth:        set.Auth,
		Permissions: Permissions(set, op),
		Public:      set.Public,
		Docs: &ir.OperationDocs{
			Title:         Title(td.Name, op),
			Description:   descriptions[op],
			Capability:    Capability(td.Name, op),
			Lifecycle:     ir.DocsLifecycleActive,
			Visibility:    ir.DocsVisibilityPublic,
			MappingStatus: ir.DocsMappingStatusMapped,
		},
		MCP: &ir.OperationMCP{
			Handle:     Handle(td.Name, op),
			Invocation: ir.MCPInvocation{Key: policy.Key, Value: policy.Default},
		},
		Origin: ir.OriginRowFeatures,
		RowFeatureOperation: &ir.RowFeatureOperation{
			Database:  db.Name,
			Type:      td.Name,
			Key:       key.Name,
			Operation: op,
		},
	}
	if ir.RowFeatureOperationTakesBucket(op) {
		out.RowFeatureOperation.Bucket = b.plan.Bucket
	}
	switch {
	case !IsTool(op):
		out.MCP = &ir.OperationMCP{Hidden: true, HiddenReason: RouteOnlyReason(op)}
	default:
		if callers := ir.EffectiveServiceCallers(set, out); callers != nil && callers.Mode == ir.ServiceCallersRequire {
			// Only a calling service reaches the route, so no end user's
			// agent can call the tool (D37).
			out.MCP = &ir.OperationMCP{Hidden: true, HiddenReason: RequireServiceToolReason(set.Name)}
		}
	}
	if Reads(op) {
		out.Docs.ReplayMode = ir.DocsReplayModeReadOnly
	}
	if NamesFile(op) {
		out.Arguments = append(out.Arguments, &ir.ArgumentDef{
			Name:     ir.RowFeatureFileParam,
			Comment:  "The id of the file, one of the row's.",
			TypeRef:  ir.TypeRef{Name: "string"},
			Required: true,
		})
	}
	input := func(ref ir.TypeRef) {
		out.Arguments = append(out.Arguments, &ir.ArgumentDef{Name: ir.RowFeatureInputArgument, TypeRef: ref, Required: true})
	}
	switch op {
	case ir.RowOpReferences:
		out.TypeRef = b.reference()
		out.TypeRef.IsArray = true
	case ir.RowOpAddReference:
		input(b.referenceInput())
		out.TypeRef = b.reference()
	case ir.RowOpRemoveReference:
		input(b.referenceInput())
		out.TypeRef = b.removed()
	case ir.RowOpBacklinks:
		out.TypeRef = b.backlinks()
	case ir.RowOpAttachments:
		out.TypeRef = b.attachment()
		out.TypeRef.IsArray = true
	case ir.RowOpBeginUpload:
		input(b.beginInput())
		out.TypeRef = b.begun()
	case ir.RowOpCompleteUpload:
		out.TypeRef = b.attachment()
	case ir.RowOpDownload:
		out.TypeRef = b.download()
	case ir.RowOpFileAttachment:
		input(b.fileInput())
		out.TypeRef = b.attachment()
	case ir.RowOpRemoveAttachment:
		out.TypeRef = ir.TypeRef{Name: "boolean"}
	}
	return out
}

// Expand fills each @rowFeatureOperations set of api with the operations
// of its table's references and files, read from db, the API's database,
// and adds the types and enums they take and return (#723). It assumes the
// sets passed verify.CheckRowFeatureOperations, and does nothing for an API
// without such a set. It returns the scalar names the added types use, so
// the caller can hydrate any api did not hold; one only db declares is
// copied from it. It may run again on its own output.
func Expand(api, db *ir.Schema, reg *registry.Registry) ([]string, error) {
	var sets []*ir.OperationSet
	for _, set := range api.OperationSets {
		if set.IsRowFeatureOperations() {
			sets = append(sets, set)
		}
	}
	if len(sets) == 0 || db == nil {
		return nil, nil
	}
	var scalars []string
	for _, set := range sets {
		plan, err := PlanSet(set, api, db, reg)
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

// builder adds the types and enums of one set's operations to its plan,
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
	return &ir.FieldDef{Name: name, Comment: comment, TypeRef: ir.TypeRef{Name: typeName}, Required: required, Origin: ir.OriginRowFeatures}
}

// fieldOf is a member of an added type that holds another added type or
// enum.
func (b *builder) fieldOf(name string, ref ir.TypeRef, required bool, comment string) *ir.FieldDef {
	return &ir.FieldDef{Name: name, Comment: comment, TypeRef: ref, Required: required, Origin: ir.OriginRowFeatures}
}

// addType adds the type name of role with fields, once.
func (b *builder) addType(name, comment string, role ir.Role, fields ...*ir.FieldDef) ir.TypeRef {
	if b.claim("type", name) {
		b.plan.Types = append(b.plan.Types, &ir.TypeDef{Name: name, Comment: comment, Role: role, Origin: ir.OriginRowFeatures, Fields: fields})
	}
	return ir.TypeRef{Name: name}
}

// addEnum adds the enum name of the string values, once.
func (b *builder) addEnum(name, comment string, values ...string) ir.TypeRef {
	if b.claim("enum", name) {
		ed := &ir.EnumDef{Name: name, Comment: comment, Origin: ir.OriginRowFeatures}
		for _, value := range values {
			ed.Values = append(ed.Values, ir.EnumValueDef{Name: codegen.ToPascalCase(value), SerializedAs: value})
		}
		b.plan.Enums = append(b.plan.Enums, ed)
	}
	return ir.TypeRef{Name: name}
}

// rowRef is a row of any table, by its type's name and its key.
func (b *builder) rowRef() ir.TypeRef {
	return b.addType(RowRefType, "A row of a table, by its type's name and its key in its wire form.", ir.RoleEmbeddedStruct,
		b.field("table", "string", true, "The row's table, by its type's name (Issue)."),
		b.field("key", "string", true, "The row's key in its wire form."),
	)
}

// reference is a reference a row holds.
func (b *builder) reference() ir.TypeRef {
	row := b.rowRef()
	return b.addType(ReferenceType, "A reference a row holds: the row that holds it, the row it points at and its role.", ir.RoleEmbeddedStruct,
		b.field("id", "string", true, "The reference's id."),
		b.fieldOf("from", row, true, "The row that holds it."),
		b.fieldOf("to", row, true, "The row it points at."),
		b.field("role", "string", true, "Its role, empty for none."),
		b.field("addedBy", "string", false, "The user who added it; absent for none."),
		b.field("addedAt", dateTimeScalar, true, "When it was added."),
	)
}

// referenceInput is the body of an add and a remove of a reference.
func (b *builder) referenceInput() ir.TypeRef {
	return b.addType(ReferenceInputType, "The row a reference points at, and its role.", ir.RoleAPIInput,
		b.fieldOf("to", b.rowRef(), true, "The row the reference points at."),
		b.field("role", "string", false, "The reference's role: none unless given. A remove with none removes the references of every role."),
	)
}

// removed is what a remove of references answers.
func (b *builder) removed() ir.TypeRef {
	return b.addType(RemovedType, "How many references a remove removed.", ir.RoleEmbeddedStruct,
		b.field("removed", int64Scalar, true, "How many references it removed."),
	)
}

// backlinks are the rows that point at a row.
func (b *builder) backlinks() ir.TypeRef {
	via := b.addEnum(BacklinkViaEnum, "How a row points at another: through a reference it holds, or a ref block of a message of its conversation.", "reference", "comment")
	link := b.addType(BacklinkType, "A row that points at another.", ir.RoleEmbeddedStruct,
		b.field("table", "string", true, "The row's table, by its type's name."),
		b.field("key", "string", true, "The row's key in its wire form."),
		b.fieldOf("via", via, true, "How it points at the row."),
		b.field("role", "string", false, "The reference's role, for a reference."),
		b.field("message", int64Scalar, false, "The message, for a ref block."),
	)
	items := b.fieldOf("items", link, true, "The rows the caller can read, most recent first.")
	items.TypeRef.IsArray = true
	return b.addType(BacklinksType, "The rows that point at a row, as its reader may see them.", ir.RoleEmbeddedStruct,
		items,
		b.field("hidden", int64Scalar, false, "How many rows the caller cannot see point at the row: present only for the owner of a row whose audience is not everyone (D156)."),
	)
}

// attachment is a file of a row.
func (b *builder) attachment() ir.TypeRef {
	state := b.addEnum(AttachmentStateEnum, "A file's state: pending while its upload is under way, ready once its bytes were checked.", ir.AttachmentPending, ir.AttachmentReady)
	processing := b.addEnum(ProcessingEnum, "A file processor's state of a file's text or preview (D159): none asked, asked, made, or failed.", ir.ProcessingNone, ir.ProcessingPending, ir.ProcessingDone, ir.ProcessingFailed)
	return b.addType(AttachmentType, "A file of a row, in a bucket.", ir.RoleEmbeddedStruct,
		b.field("id", "string", true, "The file's id."),
		b.field("table", "string", true, "The table of the row that holds it, by its type's name."),
		b.field("key", "string", true, "The key of the row that holds it, in its wire form."),
		b.field("name", "string", true, "The file's name."),
		b.field("mediaType", "string", true, "The file's media type."),
		b.field("size", int64Scalar, true, "The file's size in bytes."),
		b.field("contentHash", "string", true, "The SHA-256 of its bytes, 64 lowercase hex digits."),
		b.field("objectKey", "string", true, "The bucket's object that holds its bytes, which a file filed elsewhere shares."),
		b.field("caption", "string", false, "Its caption; absent for none."),
		b.fieldOf("state", state, true, "Pending while its upload is under way, ready once its bytes were checked."),
		b.field("uploadedBy", "string", false, "The user who uploaded it; absent for none."),
		b.field("uploadedAt", dateTimeScalar, true, "When its upload began."),
		b.field("expiresAt", dateTimeScalar, false, "When a pending file's upload URL expires."),
		b.field("filedFrom", "string", false, "The file it was filed from, as table:key:id."),
		b.fieldOf("extraction", processing, true, "The state of its text's extraction."),
		b.field("extractedText", "string", false, "Its text, once extracted."),
		b.fieldOf("preview", processing, true, "The state of its preview."),
		b.field("previewObject", "string", false, "The bucket's object that holds its preview, once made."),
	)
}

// beginInput is the body of an upload's begin.
func (b *builder) beginInput() ir.TypeRef {
	return b.addType(BeginInputType, "The file an upload begins: its name, media type, size and SHA-256, which its completion holds the bytes to.", ir.RoleAPIInput,
		b.field("name", "string", true, "The file's name."),
		b.field("mediaType", "string", true, "The file's media type, which the upload's PUT sends."),
		b.field("size", int64Scalar, true, "The file's size in bytes."),
		b.field("sha256", "string", true, "The SHA-256 of the file's bytes, 64 lowercase hex digits."),
		b.field("caption", "string", false, "Its caption; none unless given."),
	)
}

// upload is what a browser sends its upload with.
func (b *builder) upload() ir.TypeRef {
	headers := b.field("headers", "string", true, "The headers the PUT sends.")
	headers.TypeRef.IsMap = true
	return b.addType(UploadType, "What a browser sends an upload with: a PUT of the file's bytes to url, with headers, before expiresAt.", ir.RoleEmbeddedStruct,
		b.field("url", "string", true, "The URL the bytes go to, straight to the bucket."),
		b.field("method", "string", true, "The method that sends them: PUT."),
		headers,
		b.field("expiresAt", dateTimeScalar, true, "When the URL expires."),
	)
}

// begun is what an upload's begin answers.
func (b *builder) begun() ir.TypeRef {
	return b.addType(BegunType, "A pending file and the upload a browser sends its bytes with.", ir.RoleEmbeddedStruct,
		b.fieldOf("attachment", b.attachment(), true, "The pending file."),
		b.fieldOf("upload", b.upload(), true, "The upload."),
	)
}

// download is what a download answers.
func (b *builder) download() ir.TypeRef {
	return b.addType(DownloadType, "A URL that downloads a file's bytes until it expires, and the file.", ir.RoleEmbeddedStruct,
		b.field("url", "string", true, "The URL that downloads the bytes, straight from the bucket."),
		b.field("expiresAt", dateTimeScalar, true, "When the URL expires."),
		b.fieldOf("attachment", b.attachment(), true, "The file."),
	)
}

// fileInput is the body of a file's filing onto another row.
func (b *builder) fileInput() ir.TypeRef {
	return b.addType(FileInputType, "The row a file is filed onto, and how.", ir.RoleAPIInput,
		b.fieldOf("to", b.rowRef(), true, "The row the file is filed onto."),
		b.field("move", "boolean", false, "Moves the file itself, which leaves its row; a copy unless set."),
		b.field("role", "string", false, "The role of the reference the row makes to the other: filed unless given."),
	)
}
