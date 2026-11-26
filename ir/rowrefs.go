package ir

import (
	"fmt"
	"regexp"
)

// References and attachments on any row (D154).
//
// A DB table marked @references keeps, for each row, the rows it points
// at: a target is a row of any table of the database, or of one the
// declaration lists, named by its table and key, with an optional role.
// The loader adds a sub-table of the targets beside the table
// ([RowReferencesTypeName]); a trigger on each table a reference may point
// at clears the references to a row when the row is deleted, and enqueues
// a message on the declaration's cleared queue for each, so the holder
// hears of it. The runtime reads the references of a row and the rows that
// point at one (referencedBy) across every table, each held to what the
// caller may read.
//
// A DB table marked @attachments keeps, for each row, files in a bucket:
// each a name, a media type, a size, the SHA-256 of its bytes, a caption,
// who uploaded it and when, and the name of the object that holds the
// bytes. The loader adds a sub-table of the files beside the table
// ([AttachmentsTypeName]). Two rows may hold one object: a file filed
// onto another row reuses it. A trigger enqueues a message on the
// declaration's released queue whenever a file's row is deleted, and the
// worker that drains it deletes the object once no row of any attachments
// table names it.

// RowReferencesDef is the @references declaration of a DB table (D154).
//
// In TypeScript: @references<T>({ tables?, cleared?, maxPerRow? })
type RowReferencesDef struct {
	// Tables are the tables a reference may point at, by their types'
	// names. Empty is any table of the database whose key is one column.
	Tables []string `json:"tables,omitempty" yaml:"tables,omitempty"`

	// Cleared is the @queue class of the schema a target's delete
	// enqueues one message on for each reference it clears, whose fields
	// the loader fills ([RowReferencesClearedFields]). Empty clears the
	// references and enqueues nothing.
	Cleared string `json:"cleared,omitempty" yaml:"cleared,omitempty"`

	// MaxPerRow bounds the references one row holds. Zero is
	// DefaultMaxReferencesPerRow.
	MaxPerRow int `json:"maxPerRow,omitempty" yaml:"maxPerRow,omitempty"`
}

// DefaultMaxReferencesPerRow is the most references a row holds when its
// declaration names no maxPerRow.
const DefaultMaxReferencesPerRow = 1000

// AttachmentsDef is the @attachments declaration of a DB table (D154).
//
// In TypeScript: @attachments<T>({ released, maxFileBytes?, maxRowBytes?, maxFiles?, mediaTypes? })
type AttachmentsDef struct {
	// Released is the @queue class of the schema a file row's delete
	// enqueues a message on, naming its object, and an upload that was
	// never completed enqueues one on once its URL has expired. Its fields
	// the loader fills ([AttachmentsReleasedFields]). The worker that
	// drains it deletes the object once no file names it. Required: an
	// object no worker deletes would stay in the bucket for good.
	Released string `json:"released" yaml:"released"`

	// MaxFileBytes bounds one file. Zero is DefaultMaxFileBytes; at most
	// MaxSinglePutBytes, the largest object one signed PUT uploads.
	MaxFileBytes int64 `json:"maxFileBytes,omitempty" yaml:"maxFileBytes,omitempty"`

	// MaxRowBytes bounds the files of one row together, those whose upload
	// is under way among them. Zero is DefaultMaxRowBytes.
	MaxRowBytes int64 `json:"maxRowBytes,omitempty" yaml:"maxRowBytes,omitempty"`

	// MaxFiles bounds the files of one row. Zero is DefaultMaxFiles.
	MaxFiles int `json:"maxFiles,omitempty" yaml:"maxFiles,omitempty"`

	// MediaTypes are the media types a file may have, each a type
	// (image/png) or a range (image/*). Empty takes any.
	MediaTypes []string `json:"mediaTypes,omitempty" yaml:"mediaTypes,omitempty"`
}

// The limits of @attachments when its declaration names none (D154).
const (
	// DefaultMaxFileBytes is 100 MiB.
	DefaultMaxFileBytes int64 = 100 << 20
	// DefaultMaxRowBytes is 1 GiB.
	DefaultMaxRowBytes int64 = 1 << 30
	// DefaultMaxFiles is 100.
	DefaultMaxFiles = 100
	// MaxSinglePutBytes is 5 GiB, the largest object one PUT uploads to
	// S3's API, R2's and Cloud Storage's alike. A larger file needs a
	// multipart upload, which D154 leaves for later.
	MaxSinglePutBytes int64 = 5 << 30
)

// FileBytes is the bound on one file.
func (a *AttachmentsDef) FileBytes() int64 {
	if a == nil || a.MaxFileBytes == 0 {
		return DefaultMaxFileBytes
	}
	return a.MaxFileBytes
}

// RowBytes is the bound on one row's files together.
func (a *AttachmentsDef) RowBytes() int64 {
	if a == nil || a.MaxRowBytes == 0 {
		return DefaultMaxRowBytes
	}
	return a.MaxRowBytes
}

// Files is the bound on one row's count of files.
func (a *AttachmentsDef) Files() int {
	if a == nil || a.MaxFiles == 0 {
		return DefaultMaxFiles
	}
	return a.MaxFiles
}

// PerRow is the bound on one row's count of references.
func (r *RowReferencesDef) PerRow() int {
	if r == nil || r.MaxPerRow == 0 {
		return DefaultMaxReferencesPerRow
	}
	return r.MaxPerRow
}

// OriginReferences marks a type or a field the loader added for
// @references (D154), and OriginAttachments one it added for
// @attachments.
const (
	OriginReferences  = "references"
	OriginAttachments = "attachments"
)

// RowTable marks the sub-table the loader adds beside a @references or an
// @attachments table (D154): a child table of the row, with a foreign key
// to it that deletes its rows with the row, which the runtime's references
// and attachments write and for which no repository is generated.
type RowTable struct {
	// Row is the row's type: Ticket.
	Row string `json:"row" yaml:"row"`
	// Feature is OriginReferences or OriginAttachments.
	Feature string `json:"feature" yaml:"feature"`
	// RowField is the sub-table's relation field to the row: ticket.
	RowField string `json:"rowField" yaml:"rowField"`
}

// RowReferencesTypeName is the type of the references sub-table of the
// table typeName: TicketReferences, whose table is ticket_references.
func RowReferencesTypeName(typeName string) string { return typeName + "References" }

// AttachmentsTypeName is the type of the attachments sub-table of the
// table typeName: TicketAttachments, whose table is ticket_attachments.
func AttachmentsTypeName(typeName string) string { return typeName + "Attachments" }

// The fields of a references sub-table, beside its relation to the row
// (D154).
const (
	// RefIDField is the reference's own id, a UUID.
	RefIDField = "id"
	// RefTargetTableField is the target's table, by its type's name.
	RefTargetTableField = "targetTable"
	// RefTargetKeyField is the target's key as the database writes it as
	// text: a UUID hyphenated, a number in decimal.
	RefTargetKeyField = "targetKey"
	// RefRoleField is the reference's role, a label the app gives it, ""
	// for none. A row points at one target once per role.
	RefRoleField = "role"
	// RefAddedByField is who added it, a UUID, null for no one.
	RefAddedByField = "addedBy"
	// RefAddedAtField is when it was added.
	RefAddedAtField = "addedAt"
)

// RowReferencesClearedFields are the fields the loader fills in a
// @references declaration's cleared queue: the holder's table and key,
// the target's, and the role.
var RowReferencesClearedFields = []string{"holderTable", "holderKey", "targetTable", "targetKey", "role"}

// The fields of an attachments sub-table, beside its relation to the row
// (D154).
const (
	AttIDField            = "id"
	AttNameField          = "name"
	AttMediaTypeField     = "mediaType"
	AttSizeField          = "size"
	AttContentHashField   = "contentHash"
	AttObjectKeyField     = "objectKey"
	AttCaptionField       = "caption"
	AttStateField         = "state"
	AttUploadedByField    = "uploadedBy"
	AttUploadedAtField    = "uploadedAt"
	AttExpiresAtField     = "expiresAt"
	AttFiledFromField     = "filedFrom"
	AttExtractionField    = "extraction"
	AttExtractedField     = "extractedText"
	AttPreviewField       = "preview"
	AttPreviewObjectField = "previewObject"
)

// The states of an attachment (the state column): an upload under way,
// whose URL a caller holds until expiresAt, and a file whose bytes the
// runtime checked against what the upload declared.
const (
	AttachmentPending = "pending"
	AttachmentReady   = "ready"
)

// The states of an attachment's text extraction and of its preview (D154),
// which a file processor (D159) moves: none asked, asked and not yet made,
// made, or failed. Extraction is asked when an upload completes, so search
// can index the text; a preview is asked on its first view.
const (
	ProcessingNone    = "none"
	ProcessingPending = "pending"
	ProcessingDone    = "done"
	ProcessingFailed  = "failed"
)

// AttachmentsReleasedFields are the fields the loader fills in an
// @attachments declaration's released queue: the object, the SHA-256 of
// its bytes when known, and why (detached or expired).
var AttachmentsReleasedFields = []string{"objectKey", "contentHash", "reason"}

// RowReferencesTables returns the schema's @references tables, by name.
func (s *Schema) RowReferencesTables() []*TypeDef {
	var out []*TypeDef
	for _, name := range s.typeNames() {
		if td := s.Types[name]; td != nil && td.RowReferences != nil {
			out = append(out, td)
		}
	}
	return out
}

// AttachmentsTables returns the schema's @attachments tables, by name.
func (s *Schema) AttachmentsTables() []*TypeDef {
	var out []*TypeDef
	for _, name := range s.typeNames() {
		if td := s.Types[name]; td != nil && td.Attachments != nil {
			out = append(out, td)
		}
	}
	return out
}

// mediaRange is a media type or a range of one, lowercase, with no
// parameters: image/png, image/*, */*.
var mediaRange = regexp.MustCompile(`^(\*/\*|[a-z0-9][a-z0-9!#$&^_.+-]{0,126}/(\*|[a-z0-9][a-z0-9!#$&^_.+-]{0,126}))$`)

// IsMediaRange reports whether text is a media type or a range of one as
// @attachments' mediaTypes takes it.
func IsMediaRange(text string) bool { return mediaRange.MatchString(text) }

// validateRowFeatures checks what a @references or an @attachments
// declaration alone decides (D154): bounds above zero and within their
// ceilings, and media ranges in their form. Whether the tables and queues
// it names are the schema's is the loader's verification to check.
func validateRowFeatures(td *TypeDef) []error {
	var errs []error
	if r := td.RowReferences; r != nil {
		if r.MaxPerRow < 0 {
			errs = append(errs, fmt.Errorf("%s: @references maxPerRow is %d; want a count above 0", td.Name, r.MaxPerRow))
		}
		seen := map[string]bool{}
		for _, table := range r.Tables {
			if seen[table] {
				errs = append(errs, fmt.Errorf("%s: @references tables names %s twice", td.Name, table))
			}
			seen[table] = true
		}
	}
	if a := td.Attachments; a != nil {
		if a.Released == "" {
			errs = append(errs, fmt.Errorf("%s: @attachments names no released queue: a worker of it deletes an object once no file names it, and without one the bucket keeps every object for good", td.Name))
		}
		if a.MaxFileBytes < 0 || a.MaxFileBytes > MaxSinglePutBytes {
			errs = append(errs, fmt.Errorf("%s: @attachments maxFileBytes is %d; want a size above 0 and at most %d, the largest object one signed PUT uploads", td.Name, a.MaxFileBytes, MaxSinglePutBytes))
		}
		if a.MaxRowBytes < 0 {
			errs = append(errs, fmt.Errorf("%s: @attachments maxRowBytes is %d; want a size above 0", td.Name, a.MaxRowBytes))
		}
		if a.MaxRowBytes > 0 && a.MaxRowBytes < a.FileBytes() {
			errs = append(errs, fmt.Errorf("%s: @attachments maxRowBytes %d is less than one file's bound %d", td.Name, a.MaxRowBytes, a.FileBytes()))
		}
		if a.MaxFiles < 0 {
			errs = append(errs, fmt.Errorf("%s: @attachments maxFiles is %d; want a count above 0", td.Name, a.MaxFiles))
		}
		for _, mediaType := range a.MediaTypes {
			if !IsMediaRange(mediaType) {
				errs = append(errs, fmt.Errorf("%s: @attachments mediaTypes holds %q; want a lowercase media type or range with no parameters, as image/png or image/*", td.Name, mediaType))
			}
		}
	}
	return errs
}
