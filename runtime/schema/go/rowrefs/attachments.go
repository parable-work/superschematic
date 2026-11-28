package rowrefs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Bucket is what attachments sign URLs for, read back and delete objects
// in: the HTTP runtime's bucket.Bucket through bucket.ForAttachments, or a
// test's own.
type Bucket interface {
	// SignedURL is a URL that lets whoever holds it GET or PUT the object
	// name until expires passes; a PUT sends contentType, which the
	// signature covers.
	SignedURL(ctx context.Context, name, method string, expires time.Duration, contentType string) (string, error)
	// Open opens the object name for reading; ErrObjectNotFound when there
	// is none.
	Open(ctx context.Context, name string) (io.ReadCloser, error)
	// Delete removes the object name; ErrObjectNotFound when there is
	// none.
	Delete(ctx context.Context, name string) error
}

// ErrObjectNotFound is what a Bucket wraps for an object that is not
// there.
var ErrObjectNotFound = errors.New("rowrefs: no such object")

// The states of a file.
const (
	StatePending = "pending"
	StateReady   = "ready"
)

// The states of a file processor's text or preview of a file (D159): none
// asked, asked, made, or failed.
const (
	ProcessingNone    = "none"
	ProcessingPending = "pending"
	ProcessingDone    = "done"
	ProcessingFailed  = "failed"
)

// The kinds of a processor's work Pending lists.
const (
	KindExtraction = "extraction"
	KindPreview    = "preview"
)

// Attachment is a file of a row.
type Attachment struct {
	ID string `json:"id"`
	// Table and Key are the row that holds it.
	Table     string `json:"table"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Size      int64  `json:"size"`
	// ContentHash is the SHA-256 of its bytes, 64 lowercase hex digits.
	ContentHash string `json:"contentHash"`
	// ObjectKey is the bucket's object that holds its bytes, which a file
	// filed elsewhere shares.
	ObjectKey string  `json:"objectKey"`
	Caption   *string `json:"caption,omitempty"`
	// State is StatePending while its upload is under way, StateReady once
	// its bytes were checked.
	State      string     `json:"state"`
	UploadedBy string     `json:"uploadedBy,omitempty"`
	UploadedAt time.Time  `json:"uploadedAt"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	// FiledFrom is the file it was filed from, as table:key:id.
	FiledFrom     *string `json:"filedFrom,omitempty"`
	Extraction    string  `json:"extraction"`
	ExtractedText *string `json:"extractedText,omitempty"`
	Preview       string  `json:"preview"`
	PreviewObject *string `json:"previewObject,omitempty"`
}

// Upload is what a browser sends its upload with: a PUT of the file's
// bytes to URL, with Headers, before ExpiresAt.
type Upload struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

// AttachmentBlock is a Comments attachment block that names a file of the
// row's Attachments by its id.
type AttachmentBlock struct {
	Kind      string `json:"kind"`
	URI       string `json:"uri"`
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
}

// BlockOf is the Comments attachment block of a ready file: its uri
// attachment:<id>.
func BlockOf(file Attachment) AttachmentBlock {
	return AttachmentBlock{Kind: "attachment", URI: "attachment:" + file.ID, Name: file.Name, MediaType: file.MediaType, Bytes: file.Size, SHA256: file.ContentHash}
}

// PreviewObjectOf is the name of the object that holds an object's
// preview, which a file processor writes and a release deletes with it.
func PreviewObjectOf(objectKey string) string { return "previews/" + objectKey }

// FileRef is a file, by the table of its row and its id.
type FileRef struct {
	Table string
	ID    string
}

// BeginInput is the file Begin starts an upload of.
type BeginInput struct {
	To        RowRef
	Name      string
	MediaType string
	Size      int64
	SHA256    string
	Caption   *string
}

// Begun is what Begin answers: the pending file and its upload.
type Begun struct {
	Attachment Attachment `json:"attachment"`
	Upload     Upload     `json:"upload"`
}

// Download is what Download answers: a URL that downloads the file's
// bytes until ExpiresAt, and the file, whose preview this first view asked.
type Download struct {
	URL        string     `json:"url"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	Attachment Attachment `json:"attachment"`
}

// FileInput files a file onto another row: a copy, or with Move the file
// itself, with Role (filed unless set) as the reference it makes.
type FileInput struct {
	FileRef
	To   RowRef
	Move bool
	Role string
}

// PendingObject is an object whose extraction or preview a processor has
// yet to make.
type PendingObject struct {
	ObjectKey   string `json:"objectKey"`
	MediaType   string `json:"mediaType"`
	ContentHash string `json:"contentHash"`
}

// ProcessedPart is what a processor made of an object's text or preview:
// its state, ProcessingDone or ProcessingFailed, and the text or the
// preview's object.
type ProcessedPart struct {
	State string
	// Text is the extracted text; Object the preview's object.
	Text   *string
	Object *string
}

// ProcessedInput is what a processor made of an object.
type ProcessedInput struct {
	ObjectKey  string
	Extraction *ProcessedPart
	Preview    *ProcessedPart
}

const (
	defaultUploadSeconds = 60 * 60
	maxUploadSeconds     = 7 * 24 * 60 * 60
	releaseGraceSeconds  = 60 * 60
)

var (
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
	mediaType = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]{0,126}/[a-z0-9][a-z0-9!#$&^_.+-]{0,126}$`)
	badName   = regexp.MustCompile(`[/\\\x00-\x1f\x7f]`)
)

// attachmentColumns are the columns a read of a file lists: each "::text"
// one read as text in the host's words, and row_key the holder's row
// column.
var attachmentColumns = []string{
	"id::text",
	"row_key",
	`"name"`,
	`"media_type"`,
	`"size"::text`,
	`"content_hash"`,
	`"object_key"`,
	`"caption"`,
	`"state"`,
	`"uploaded_by"::text`,
	`"uploaded_at"`,
	`"expires_at"`,
	`"filed_from"`,
	`"extraction"`,
	`"extracted_text"`,
	`"preview"`,
	`"preview_object"`,
}

// columnsOf is the columns a read of a file of holder lists, in w's
// words.
func columnsOf(w sqlWords, holder AttachmentsHolder) string {
	out := make([]string, len(attachmentColumns))
	for i, c := range attachmentColumns {
		switch {
		case c == "row_key":
			c = w.text(holder.Row) + " AS row_key"
		case strings.HasSuffix(c, "::text"):
			column := strings.TrimSuffix(c, "::text")
			c = w.text(column) + " AS " + strings.Trim(column, `"`)
		}
		out[i] = c
	}
	return strings.Join(out, ", ")
}

func mediaTypeAllowed(allowed []string, kind string) bool {
	if len(allowed) == 0 {
		return true
	}
	family, _, _ := strings.Cut(kind, "/")
	for _, one := range allowed {
		if one == kind || one == "*/*" || (strings.HasSuffix(one, "/*") && strings.TrimSuffix(one, "/*") == family) {
			return true
		}
	}
	return false
}

// checkName holds a file's name to 1 to 255 code points (D14, amended:
// lengths count code points), none of them /, \ or a control character,
// and not white space alone. TypeScript's isFileName applies the same rule;
// runtime/http/testdata/attachments_parity.json holds the vectors both run.
func checkName(name string) error {
	if n := utf8.RuneCountInString(name); n < 1 || n > 255 || badName.MatchString(name) || strings.TrimSpace(name) == "" {
		return refuse(CodeInvalid, nil, "a file's name is 1 to 255 characters with no /, \\ or control character, not %q", name)
	}
	return nil
}

// Attachments are the attachment operations of an ORM whose database
// declares @attachments (D154). A nil Attachments, a no-op database's,
// answers ErrNoDatabase.
type Attachments struct {
	host       Host
	def        *AttachmentsDefinition
	references *ReferencesDefinition
	now        func() time.Time
}

// NewAttachments is the attachments of def over host; references, the
// database's, lets File make the reference of a file filed elsewhere.
func NewAttachments(host Host, def *AttachmentsDefinition, references *ReferencesDefinition) *Attachments {
	if host == nil || def == nil {
		return nil
	}
	return &Attachments{host: inDialect(host), def: def, references: references, now: time.Now}
}

func (a *Attachments) holderOf(table string) (AttachmentsHolder, error) {
	holder, ok := a.def.Holders[table]
	if !ok {
		return AttachmentsHolder{}, refuse(CodeNotAHolder, map[string]any{"table": table}, "%s holds no files: it is no @attachments table", table)
	}
	return holder, nil
}

func attachmentOf(table string, holder AttachmentsHolder, row Row) Attachment {
	out := Attachment{
		ID:            UUIDFromDB(text(row, "id")),
		Table:         table,
		Key:           wireKey(holder.UUID, text(row, "row_key")),
		Name:          text(row, "name"),
		MediaType:     text(row, "media_type"),
		Size:          integer(row, "size"),
		ContentHash:   text(row, "content_hash"),
		ObjectKey:     text(row, "object_key"),
		Caption:       optional(row, "caption"),
		State:         StatePending,
		UploadedAt:    timeOf(row, "uploaded_at"),
		ExpiresAt:     optionalTime(row, "expires_at"),
		FiledFrom:     optional(row, "filed_from"),
		Extraction:    text(row, "extraction"),
		ExtractedText: optional(row, "extracted_text"),
		Preview:       text(row, "preview"),
		PreviewObject: optional(row, "preview_object"),
	}
	if text(row, "state") == StateReady {
		out.State = StateReady
	}
	if by := text(row, "uploaded_by"); by != "" {
		out.UploadedBy = UUIDFromDB(by)
	}
	return out
}

// read is the file, read as the system, once the reader sees its row; not
// found for either.
func (a *Attachments) read(ctx context.Context, h Host, file FileRef, lockRow string) (AttachmentsHolder, Attachment, error) {
	holder, err := a.holderOf(file.Table)
	if err != nil {
		return holder, Attachment{}, err
	}
	notFound := h.NotFound(fmt.Sprintf("%s holds no file %s", file.Table, file.ID))
	id := UUIDToDB(file.ID)
	if id == "" {
		return holder, Attachment{}, notFound
	}
	rows, err := h.Query(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE id = $1%s`, columnsOf(wordsOf(h), holder), holder.Table, lockRow), id)
	if err != nil {
		return holder, Attachment{}, err
	}
	if len(rows) == 0 {
		return holder, Attachment{}, notFound
	}
	found := attachmentOf(file.Table, holder, rows[0])
	seen, err := visible(ctx, h, file.Table, []string{found.Key})
	if err != nil {
		return holder, Attachment{}, err
	}
	if !seen[found.Key] {
		return holder, Attachment{}, notFound
	}
	return holder, found, nil
}

// admit holds one more file of size to row's bounds, under the row's
// lock: the declaration's media types and bounds.
func (a *Attachments) admit(ctx context.Context, h Host, holder AttachmentsHolder, row RowRef, kind string, size int64) error {
	if !mediaTypeAllowed(holder.MediaTypes, kind) {
		return refuse(CodeMediaType, map[string]any{"mediaType": kind, "allowed": holder.MediaTypes}, "%s holds files of %s, and not %s", row.Table, strings.Join(holder.MediaTypes, ", "), kind)
	}
	if size > holder.MaxFileBytes {
		return refuse(CodeTooLarge, map[string]any{"size": size, "max": holder.MaxFileBytes}, "a file of %s is at most %d bytes, and this one is %d", row.Table, holder.MaxFileBytes, size)
	}
	key, err := dbKey(holder.UUID, row.Key)
	if err != nil {
		return err
	}
	if err := lock(ctx, h, fmt.Sprintf("attachments:%s:%s", holder.Table, key)); err != nil {
		return err
	}
	w := wordsOf(h)
	held, err := h.Query(ctx, fmt.Sprintf(`SELECT %s AS files, %s AS bytes FROM %s WHERE %s = $1 AND ("state" = 'ready' OR "expires_at" > %s)`, w.int("count(*)"), w.text(`COALESCE(sum("size"), 0)`), holder.Table, holder.Row, w.now()), key)
	if err != nil {
		return err
	}
	files, bytes := integer(held[0], "files"), integer(held[0], "bytes")
	if files+1 > int64(holder.MaxFiles) {
		return refuse(CodeRowFull, map[string]any{"files": files, "max": holder.MaxFiles}, "%s %s holds %d files, the most a row may", row.Table, row.Key, files)
	}
	if bytes+size > holder.MaxRowBytes {
		return refuse(CodeRowFull, map[string]any{"bytes": bytes, "size": size, "max": holder.MaxRowBytes}, "%s %s's files hold %d bytes, and %d more would pass its %d", row.Table, row.Key, bytes, size, holder.MaxRowBytes)
	}
	return nil
}

func expiry(seconds int) (time.Duration, error) {
	if seconds == 0 {
		seconds = defaultUploadSeconds
	}
	if seconds < 1 || seconds > maxUploadSeconds {
		return 0, refuse(CodeInvalid, nil, "an upload's URL lives 1 to %d seconds, not %d", maxUploadSeconds, seconds)
	}
	return time.Duration(seconds) * time.Second, nil
}

// Begin starts an upload of a file to in.To: a pending file, held to the
// declaration's bounds with the row's other files and uploads, and a URL
// the browser PUTs the bytes to, which expires after expiresInSeconds (an
// hour when 0).
func (a *Attachments) Begin(ctx context.Context, in BeginInput, bucket Bucket, expiresInSeconds int) (Begun, error) {
	if a == nil {
		return Begun{}, ErrNoDatabase
	}
	holder, err := a.holderOf(in.To.Table)
	if err != nil {
		return Begun{}, err
	}
	if err := checkName(in.Name); err != nil {
		return Begun{}, err
	}
	if !mediaType.MatchString(in.MediaType) {
		return Begun{}, refuse(CodeInvalid, nil, "a file's media type is type/subtype in lower case with no parameters, not %q", in.MediaType)
	}
	if in.Size < 0 {
		return Begun{}, refuse(CodeInvalid, nil, "a file's size is a whole number of bytes, not %d", in.Size)
	}
	if !sha256Hex.MatchString(in.SHA256) {
		return Begun{}, refuse(CodeInvalid, nil, "a file's sha256 is the SHA-256 of its bytes, 64 lowercase hex digits")
	}
	lives, err := expiry(expiresInSeconds)
	if err != nil {
		return Begun{}, err
	}
	rowKey, err := dbKey(holder.UUID, in.To.Key)
	if err != nil {
		return Begun{}, err
	}
	objectKey := "attachments/" + newUUID()
	expiresAt := a.now().Add(lives)
	var created Attachment
	err = a.host.Transaction(ctx, func(h Host) error {
		if err := mustSee(ctx, h, in.To); err != nil {
			return err
		}
		if err := a.admit(ctx, h, holder, in.To, in.MediaType, in.Size); err != nil {
			return err
		}
		var caption any
		if in.Caption != nil {
			caption = *in.Caption
		}
		rows, err := h.Query(ctx, fmt.Sprintf(`INSERT INTO %s (%s, "name", "media_type", "size", "content_hash", "object_key", "caption", "state", "uploaded_by", "expires_at")
             VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $9) RETURNING %s`, holder.Table, holder.Row, columnsOf(wordsOf(h), holder)),
			rowKey, in.Name, in.MediaType, in.Size, in.SHA256, objectKey, caption, userArg(ctx, h), expiresAt)
		if err != nil {
			return err
		}
		// An upload never completed is released once its URL has expired.
		if _, err := h.Exec(ctx, fmt.Sprintf(`INSERT INTO %s ("object_key", "content_hash", "reason", "due_at") VALUES ($1, $2, 'expired', $3)`, holder.Released), objectKey, in.SHA256, expiresAt.Add(releaseGraceSeconds*time.Second)); err != nil {
			return err
		}
		created = attachmentOf(in.To.Table, holder, rows[0])
		return nil
	})
	if err != nil {
		return Begun{}, err
	}
	url, err := bucket.SignedURL(ctx, objectKey, "PUT", lives, in.MediaType)
	if err != nil {
		return Begun{}, err
	}
	return Begun{Attachment: created, Upload: Upload{URL: url, Method: "PUT", Headers: map[string]string{"content-type": in.MediaType}, ExpiresAt: expiresAt}}, nil
}

// hashOf is the SHA-256 and the size of body, reading at most limit+1
// bytes: a body past limit is not the file it was declared, and stops
// there.
func hashOf(body io.Reader, limit int64) (string, int64, error) {
	sum := sha256.New()
	n, err := io.Copy(sum, io.LimitReader(body, limit+1))
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(sum.Sum(nil)), n, nil
}

// Complete checks an upload's bytes against the size and SHA-256 its
// Begin declared, and makes the file ready; a file whose bytes differ is
// removed, and its object with it once the released message's worker
// runs.
func (a *Attachments) Complete(ctx context.Context, ref FileRef, bucket Bucket) (Attachment, error) {
	if a == nil {
		return Attachment{}, ErrNoDatabase
	}
	holder, file, err := a.read(ctx, a.host, ref, "")
	if err != nil {
		return Attachment{}, err
	}
	if file.State != StatePending {
		return Attachment{}, refuse(CodeNotPending, map[string]any{"state": file.State}, "file %s is %s, not an upload under way", ref.ID, file.State)
	}
	if file.ExpiresAt == nil || !file.ExpiresAt.After(a.now()) {
		at := ""
		if file.ExpiresAt != nil {
			at = file.ExpiresAt.UTC().Format(time.RFC3339)
		}
		return Attachment{}, refuse(CodeExpired, nil, "the upload of file %s expired at %s", ref.ID, at)
	}
	body, err := bucket.Open(ctx, file.ObjectKey)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return Attachment{}, refuse(CodeNotUploaded, nil, "the bucket holds no bytes of file %s yet", ref.ID)
		}
		return Attachment{}, err
	}
	hash, size, err := hashOf(body, file.Size)
	_ = body.Close()
	if err != nil {
		return Attachment{}, err
	}
	id := UUIDToDB(file.ID)
	if size != file.Size || hash != file.ContentHash {
		if _, err := a.host.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1 AND "state" = 'pending'`, holder.Table), id); err != nil {
			return Attachment{}, err
		}
		uploaded := any(hash)
		if size > file.Size {
			uploaded = nil
		}
		return Attachment{}, refuse(CodeMismatch, map[string]any{
			"size":   map[string]any{"declared": file.Size, "uploaded": size},
			"sha256": map[string]any{"declared": file.ContentHash, "uploaded": uploaded},
		}, "the bytes uploaded for file %s are not the ones its upload declared", ref.ID)
	}
	w := wordsOf(a.host)
	rows, err := a.host.Query(ctx, fmt.Sprintf(`UPDATE %s SET "state" = 'ready', "expires_at" = NULL, "uploaded_at" = %s, "extraction" = 'pending' WHERE id = $1 AND "state" = 'pending' RETURNING %s`, holder.Table, w.now(), columnsOf(w, holder)), id)
	if err != nil {
		return Attachment{}, err
	}
	if len(rows) == 0 {
		return Attachment{}, refuse(CodeNotPending, nil, "file %s is no longer an upload under way", ref.ID)
	}
	return attachmentOf(ref.Table, holder, rows[0]), nil
}

// List is the ready files of row, oldest first.
func (a *Attachments) List(ctx context.Context, row RowRef) ([]Attachment, error) {
	if a == nil {
		return nil, ErrNoDatabase
	}
	holder, err := a.holderOf(row.Table)
	if err != nil {
		return nil, err
	}
	key, err := dbKey(holder.UUID, row.Key)
	if err != nil {
		return nil, err
	}
	if err := mustSee(ctx, a.host, row); err != nil {
		return nil, err
	}
	rows, err := a.host.Query(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE %s = $1 AND "state" = 'ready' ORDER BY "uploaded_at", id`, columnsOf(wordsOf(a.host), holder), holder.Table, holder.Row), key)
	if err != nil {
		return nil, err
	}
	out := make([]Attachment, len(rows))
	for i, one := range rows {
		out[i] = attachmentOf(row.Table, holder, one)
	}
	return out, nil
}

// Get is one file of a table's row.
func (a *Attachments) Get(ctx context.Context, ref FileRef) (Attachment, error) {
	if a == nil {
		return Attachment{}, ErrNoDatabase
	}
	_, file, err := a.read(ctx, a.host, ref, "")
	return file, err
}

// Download is a URL that downloads the file's bytes until it expires
// (after expiresInSeconds, an hour when 0), and the file, whose preview
// this first view asks: most files are never viewed, so none is made at
// upload (D154).
func (a *Attachments) Download(ctx context.Context, ref FileRef, bucket Bucket, expiresInSeconds int) (Download, error) {
	if a == nil {
		return Download{}, ErrNoDatabase
	}
	holder, file, err := a.read(ctx, a.host, ref, "")
	if err != nil {
		return Download{}, err
	}
	if file.State != StateReady {
		return Download{}, refuse(CodeNotReady, nil, "file %s is still being uploaded", ref.ID)
	}
	lives, err := expiry(expiresInSeconds)
	if err != nil {
		return Download{}, err
	}
	url, err := bucket.SignedURL(ctx, file.ObjectKey, "GET", lives, "")
	if err != nil {
		return Download{}, err
	}
	viewed := file
	if file.Preview == ProcessingNone {
		rows, err := a.host.Query(ctx, fmt.Sprintf(`UPDATE %s SET "preview" = 'pending' WHERE id = $1 AND "preview" = 'none' RETURNING %s`, holder.Table, columnsOf(wordsOf(a.host), holder)), UUIDToDB(file.ID))
		if err != nil {
			return Download{}, err
		}
		if len(rows) > 0 {
			viewed = attachmentOf(ref.Table, holder, rows[0])
		}
	}
	return Download{URL: url, ExpiresAt: a.now().Add(lives), Attachment: viewed}, nil
}

// Caption sets the file's caption, or clears it with nil.
func (a *Attachments) Caption(ctx context.Context, ref FileRef, caption *string) (Attachment, error) {
	if a == nil {
		return Attachment{}, ErrNoDatabase
	}
	if caption != nil && utf8.RuneCountInString(*caption) > 2000 {
		return Attachment{}, refuse(CodeInvalid, nil, "a caption is a string of at most 2000 characters, or null")
	}
	holder, file, err := a.read(ctx, a.host, ref, "")
	if err != nil {
		return Attachment{}, err
	}
	var value any
	if caption != nil {
		value = *caption
	}
	rows, err := a.host.Query(ctx, fmt.Sprintf(`UPDATE %s SET "caption" = $2 WHERE id = $1 RETURNING %s`, holder.Table, columnsOf(wordsOf(a.host), holder)), UUIDToDB(file.ID), value)
	if err != nil {
		return Attachment{}, err
	}
	return attachmentOf(ref.Table, holder, rows[0]), nil
}

// Detach removes the file from its row; its object is released once no
// file names it.
func (a *Attachments) Detach(ctx context.Context, ref FileRef) error {
	if a == nil {
		return ErrNoDatabase
	}
	holder, file, err := a.read(ctx, a.host, ref, "")
	if err != nil {
		return err
	}
	_, err = a.host.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, holder.Table), UUIDToDB(file.ID))
	return err
}

// File files the file onto another row, which may be of another table,
// with no second upload: a copy that names the same object, or with Move
// the file itself, leaving its row. When the file's table keeps references
// that may point at To's table, its row then points at To, with Role
// (filed unless set), so To lists it in its backlinks.
func (a *Attachments) File(ctx context.Context, in FileInput) (Attachment, error) {
	if a == nil {
		return Attachment{}, ErrNoDatabase
	}
	to, err := a.holderOf(in.To.Table)
	if err != nil {
		return Attachment{}, err
	}
	toKey, err := dbKey(to.UUID, in.To.Key)
	if err != nil {
		return Attachment{}, err
	}
	var out Attachment
	err = a.host.Transaction(ctx, func(h Host) error {
		w := wordsOf(h)
		from, file, err := a.read(ctx, h, in.FileRef, w.forShare())
		if err != nil {
			return err
		}
		if file.State != StateReady {
			return refuse(CodeNotReady, nil, "file %s is still being uploaded", in.ID)
		}
		if err := mustSee(ctx, h, in.To); err != nil {
			return err
		}
		if err := a.admit(ctx, h, to, in.To, file.MediaType, file.Size); err != nil {
			return err
		}
		if err := lock(ctx, h, "attachment-object:"+file.ObjectKey); err != nil {
			return err
		}
		filedFrom := fmt.Sprintf("%s:%s:%s", in.Table, file.Key, file.ID)
		id := UUIDToDB(file.ID)
		rows, err := h.Query(ctx, fmt.Sprintf(`INSERT INTO %s (%s, "name", "media_type", "size", "content_hash", "object_key", "caption", "state", "uploaded_by", "filed_from", "extraction", "extracted_text", "preview", "preview_object")
             SELECT $1, "name", "media_type", "size", "content_hash", "object_key", "caption", 'ready', $2, $3, "extraction", "extracted_text", "preview", "preview_object" FROM %s WHERE id = $4
             RETURNING %s`, to.Table, to.Row, from.Table, columnsOf(w, to)), toKey, userArg(ctx, h), filedFrom, id)
		if err != nil {
			return err
		}
		if in.Move {
			if _, err := h.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, from.Table), id); err != nil {
				return err
			}
		}
		// The row the file was posted on points at the row it went to, so
		// that row lists it in its backlinks.
		if a.references != nil {
			holder, isHolder := a.references.Holders[in.Table]
			target, isTarget := a.references.Targets[in.To.Table]
			if isHolder && isTarget && (len(holder.Tables) == 0 || contains(holder.Tables, in.To.Table)) {
				targetKey, err := dbKey(target.UUID, in.To.Key)
				if err != nil {
					return err
				}
				keys, err := h.Query(ctx, fmt.Sprintf(`SELECT %s AS key FROM %s WHERE %s = $1`, w.text(target.Key), target.Table, target.Key), targetKey)
				if err != nil {
					return err
				}
				var key any
				if len(keys) > 0 {
					key = text(keys[0], "key")
				}
				holderKey, err := dbKey(holder.UUID, file.Key)
				if err != nil {
					return err
				}
				role := in.Role
				if role == "" {
					role = "filed"
				}
				if _, err := h.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (%s, "target_table", "target_key", "role", "added_by") VALUES ($1, $2, $3, $4, $5)
             ON CONFLICT (%s, "target_table", "target_key", "role") DO NOTHING`, holder.Table, holder.Row, holder.Row), holderKey, in.To.Table, key, role, userArg(ctx, h)); err != nil {
					return err
				}
			}
		}
		out = attachmentOf(in.To.Table, to, rows[0])
		return nil
	})
	return out, err
}

func contains(list []string, s string) bool {
	for _, one := range list {
		if one == s {
			return true
		}
	}
	return false
}

// Release is a released message's work, which the released queue's
// worker calls: it deletes the object, and its preview's, once no file
// names it, and answers whether it did; uploads never completed are
// removed first.
func (a *Attachments) Release(ctx context.Context, objectKey string, bucket Bucket) (bool, error) {
	if a == nil {
		return false, ErrNoDatabase
	}
	if objectKey == "" {
		return false, refuse(CodeInvalid, nil, "a released message names its object")
	}
	deleted := false
	err := a.host.Transaction(ctx, func(h Host) error {
		if err := lock(ctx, h, "attachment-object:"+objectKey); err != nil {
			return err
		}
		var held int64
		for _, table := range slices.Sorted(maps.Keys(a.def.Holders)) {
			holder := a.def.Holders[table]
			// An upload never completed goes once its URL has expired.
			w := wordsOf(h)
			if _, err := h.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE "object_key" = $1 AND "state" = 'pending' AND "expires_at" <= %s`, holder.Table, w.now()), objectKey); err != nil {
				return err
			}
			rows, err := h.Query(ctx, fmt.Sprintf(`SELECT %s AS n FROM %s WHERE "object_key" = $1`, w.int("count(*)"), holder.Table), objectKey)
			if err != nil {
				return err
			}
			held += integer(rows[0], "n")
		}
		if held > 0 {
			return nil
		}
		for _, name := range []string{objectKey, PreviewObjectOf(objectKey)} {
			if err := bucket.Delete(ctx, name); err != nil && !errors.Is(err, ErrObjectNotFound) {
				return err
			}
		}
		deleted = true
		return nil
	})
	return deleted, err
}

// Pending is the objects whose extraction or preview (kind) a processor
// has yet to make, each once, oldest first, at most limit (100 when 0).
func (a *Attachments) Pending(ctx context.Context, kind string, limit int) ([]PendingObject, error) {
	if a == nil {
		return nil, ErrNoDatabase
	}
	limit, err := limitOf(limit)
	if err != nil {
		return nil, err
	}
	column := `"extraction"`
	if kind == KindPreview {
		column = `"preview"`
	}
	var unions []string
	for _, table := range slices.Sorted(maps.Keys(a.def.Holders)) {
		unions = append(unions, fmt.Sprintf(`SELECT "object_key", "media_type", "content_hash", "uploaded_at" FROM %s WHERE %s = 'pending' AND "state" = 'ready'`, a.def.Holders[table].Table, column))
	}
	rows, err := a.host.Query(ctx, fmt.Sprintf(`SELECT "object_key", min("media_type") AS media_type, min("content_hash") AS content_hash FROM (%s) AS files GROUP BY "object_key" ORDER BY min("uploaded_at") LIMIT %d`, strings.Join(unions, " UNION ALL "), limit))
	if err != nil {
		return nil, err
	}
	out := make([]PendingObject, len(rows))
	for i, one := range rows {
		out[i] = PendingObject{ObjectKey: text(one, "object_key"), MediaType: text(one, "media_type"), ContentHash: text(one, "content_hash")}
	}
	return out, nil
}

// Processed records what a file processor made of an object, on every
// file that names it, and answers how many.
func (a *Attachments) Processed(ctx context.Context, in ProcessedInput) (int64, error) {
	if a == nil {
		return 0, ErrNoDatabase
	}
	var sets []string
	args := []any{in.ObjectKey}
	part := func(p *ProcessedPart, state, value string, v *string) {
		var arg any
		if v != nil {
			arg = *v
		}
		args = append(args, p.State, arg)
		sets = append(sets, fmt.Sprintf(`%s = $%d`, state, len(args)-1), fmt.Sprintf(`%s = $%d`, value, len(args)))
	}
	if in.Extraction != nil {
		part(in.Extraction, `"extraction"`, `"extracted_text"`, in.Extraction.Text)
	}
	if in.Preview != nil {
		part(in.Preview, `"preview"`, `"preview_object"`, in.Preview.Object)
	}
	if len(sets) == 0 {
		return 0, nil
	}
	var n int64
	for _, table := range slices.Sorted(maps.Keys(a.def.Holders)) {
		touched, err := a.host.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s WHERE "object_key" = $1`, a.def.Holders[table].Table, strings.Join(sets, ", ")), args...)
		if err != nil {
			return n, err
		}
		n += touched
	}
	return n, nil
}
