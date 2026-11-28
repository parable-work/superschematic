package ir

// A table's references and files as routes (#723, D154). An API serves them
// by marking a class with no methods @rowFeatureOperations(Type, { ... }),
// from @superschematic/api:
//
//	@rowFeatureOperations(Issue, { permissions: { beginUpload: ["issues.upload"] } })
//	@requirePermission(["issues.read"])
//	export abstract class IssueLinks {}
//
// The reader records the decorator as OperationSet.RowFeatureOperations and
// the class's @auth, @requirePermission and @publicRoute as the set's Auth,
// Permissions and Public. The loader reads the API's database
// (Schema.Database), and fills the set with one operation for each of
// [RowFeatureOperationNames] the table offers: those of a @references
// table's references, the backlinks of a table a reference may point at,
// and those of an @attachments table's files. Each has Origin
// OriginRowFeatures and FieldDef.RowFeatureOperation set, and the loader
// adds the types and enums they take and return, each named Row...,
// with the same Origin. The OpenAPI document, the SDKs, the permission
// catalog and the tool documents read them as any other operation. A
// server routes an operation whose RowFeatureOperation is set to a
// generated handler that runs it through the database's references and
// attachments (orm.references, orm.attachments in TypeScript;
// References() and Attachments() in Go), as the request's caller.

// OriginRowFeatures is the Origin of each operation, type, field and enum
// the loader adds for a @rowFeatureOperations set, so the schema writer
// writes the decorator in their place.
const OriginRowFeatures = "rowFeatures"

// RowFeatureInputArgument is the argument a row feature operation's body
// travels as: the request body, of the input type the loader adds.
const RowFeatureInputArgument = "input"

// RowFeatureFileParam is the path parameter that names a file of the row,
// its id, on the routes of one file.
const RowFeatureFileParam = "file"

// The operations a @rowFeatureOperations set serves, by the names its
// operations and permissions options take.
const (
	// RowOpReferences lists the references the row holds.
	RowOpReferences = "references"
	// RowOpAddReference makes the row point at another.
	RowOpAddReference = "addReference"
	// RowOpRemoveReference stops the row pointing at another.
	RowOpRemoveReference = "removeReference"
	// RowOpBacklinks lists the rows that point at the row.
	RowOpBacklinks = "backlinks"
	// RowOpAttachments lists the row's ready files.
	RowOpAttachments = "attachments"
	// RowOpBeginUpload starts an upload of a file to the row and signs
	// the URL a browser puts its bytes to.
	RowOpBeginUpload = "beginUpload"
	// RowOpCompleteUpload holds an upload's bytes to what its begin
	// declared and makes the file ready.
	RowOpCompleteUpload = "completeUpload"
	// RowOpDownload signs a URL that downloads one of the row's files.
	RowOpDownload = "download"
	// RowOpFileAttachment files one of the row's files onto another row.
	RowOpFileAttachment = "fileAttachment"
	// RowOpRemoveAttachment removes one of the row's files, whose object
	// is released once no file names it.
	RowOpRemoveAttachment = "removeAttachment"
)

// RowFeatureOperationNames are the operations a @rowFeatureOperations set
// serves when it narrows none and its table offers them all, in the order
// the loader adds them.
var RowFeatureOperationNames = []string{
	RowOpReferences, RowOpAddReference, RowOpRemoveReference, RowOpBacklinks,
	RowOpAttachments, RowOpBeginUpload, RowOpCompleteUpload, RowOpDownload,
	RowOpFileAttachment, RowOpRemoveAttachment,
}

// The features a row feature operation runs on.
const (
	// RowFeatureReferences is a @references table's own references.
	RowFeatureReferences = "references"
	// RowFeatureBacklinks is the rows that point at a table's row.
	RowFeatureBacklinks = "backlinks"
	// RowFeatureAttachments is an @attachments table's files.
	RowFeatureAttachments = "attachments"
)

// RowFeatureOf is the feature the operation op runs on, "" for none.
func RowFeatureOf(op string) string {
	switch op {
	case RowOpReferences, RowOpAddReference, RowOpRemoveReference:
		return RowFeatureReferences
	case RowOpBacklinks:
		return RowFeatureBacklinks
	case RowOpAttachments, RowOpBeginUpload, RowOpCompleteUpload, RowOpDownload, RowOpFileAttachment, RowOpRemoveAttachment:
		return RowFeatureAttachments
	}
	return ""
}

// RowFeatureOperationsConfig is @rowFeatureOperations on an operation set.
type RowFeatureOperationsConfig struct {
	// Type is the table whose references and files the set serves, by its
	// name as the API imports it: a table of the API's database
	// (Schema.Database) marked @references or @attachments, or one a
	// reference may point at.
	Type string `json:"type" yaml:"type"`

	// Operations narrows the set to the operations it lists, each one of
	// [RowFeatureOperationNames] the table offers. Empty serves every one
	// it offers.
	Operations []string `json:"operations,omitempty" yaml:"operations,omitempty"`

	// Permissions names, by operation, the permissions that replace the
	// set's own (OperationSet.Permissions) for that operation.
	Permissions map[string][]string `json:"permissions,omitempty" yaml:"permissions,omitempty"`

	// Bucket is the Bucket service, one of the API's buckets
	// (Schema.Buckets), that holds the files an upload signs a URL for
	// and a download reads. Empty is the API's one bucket; it is
	// required of a set that serves a file operation of an API that lists
	// several.
	Bucket string `json:"bucket,omitempty" yaml:"bucket,omitempty"`
}

// Serves reports whether the set's options admit the operation name:
// every one of [RowFeatureOperationNames] when Operations is empty, else
// those it lists. Whether the table offers it is the loader's to read.
func (c *RowFeatureOperationsConfig) Serves(name string) bool {
	if c == nil {
		return false
	}
	list := c.Operations
	if len(list) == 0 {
		list = RowFeatureOperationNames
	}
	for _, listed := range list {
		if listed == name {
			return true
		}
	}
	return false
}

// RowFeatureOperation marks an operation the loader added for a
// @rowFeatureOperations set: which operation of which table's references
// or files it runs.
type RowFeatureOperation struct {
	// Database is the DB service the table belongs to, the API's
	// database.
	Database string `json:"database" yaml:"database"`

	// Type is the table's type name.
	Type string `json:"type" yaml:"type"`

	// Key is the name of the table's @key field: the route's first path
	// parameter, and the operation's first argument, typed by the key.
	Key string `json:"key" yaml:"key"`

	// Operation is the operation's name, one of [RowFeatureOperationNames].
	Operation string `json:"operation" yaml:"operation"`

	// Bucket is the Bucket service that holds the files, for an operation
	// that signs a URL (beginUpload, download) or reads an object back
	// (completeUpload); empty for every other.
	Bucket string `json:"bucket,omitempty" yaml:"bucket,omitempty"`
}

// Feature is the feature the operation runs on: RowFeatureReferences,
// RowFeatureBacklinks or RowFeatureAttachments.
func (o *RowFeatureOperation) Feature() string {
	if o == nil {
		return ""
	}
	return RowFeatureOf(o.Operation)
}

// RowFeatureOperationTakesBucket reports whether the operation op reads
// the bucket: an upload's begin and complete, and a download.
func RowFeatureOperationTakesBucket(op string) bool {
	return op == RowOpBeginUpload || op == RowOpCompleteUpload || op == RowOpDownload
}

// IsRowFeatureOperations reports whether the set is @rowFeatureOperations,
// whose operations the loader adds.
func (s *OperationSet) IsRowFeatureOperations() bool {
	return s != nil && s.RowFeatureOperations != nil
}

// IsReferenceTarget reports whether a reference may point at td's rows: a
// plain table with one key, as the clear trigger and the repositories'
// references read it (D154). A database's every such table is a target
// once one of its tables keeps references or a conversation.
func (td *TypeDef) IsReferenceTarget() bool {
	if td == nil || td.Role != RoleDBTable || td.Queue != nil || td.Projection != nil || td.JsonField || td.GraphMember != nil ||
		td.BehaviorTable != nil || td.BehaviorQueue != nil || td.RowTable != nil || td.SearchIndex {
		return false
	}
	keys := 0
	for _, fd := range td.Fields {
		if fd != nil && fd.Key {
			keys++
		}
	}
	return keys == 1
}

// KeepsReferences reports whether the schema keeps references: a table
// marked @references, or one whose Comments holds messages, whose ref
// blocks are references too (D154).
func (s *Schema) KeepsReferences() bool {
	if s == nil {
		return false
	}
	for _, td := range s.Types {
		if td == nil {
			continue
		}
		if td.RowReferences != nil {
			return true
		}
		if td.BehaviorTable != nil && td.BehaviorTable.Behavior == "Comments" && td.BehaviorTable.Table == "messages" {
			return true
		}
	}
	return false
}
