// Package rowrefs is references and attachments on any row (D154) as the
// generated Go ORM serves them (D184): the References and Attachments of a
// database that declares @references or @attachments (or a table's
// Comments, whose ref blocks are references too), with the TypeScript
// runtime's tables, columns, statements, locks and refusals
// (runtime/http/typescript/src/rowfeatures.ts).
//
// A reference is a row of a @references table's sub-table: the row that
// holds it, the target's table and key, and a role. Every read keeps to
// what its caller may read, through the target's and the holder's own
// repositories (Host.Visible), which apply the caller's audience and
// grants as any read of theirs does (D156, D180): a reference to a row the
// caller cannot read is left out entirely, and ReferencedBy answers only
// the owner of a private row how many rows it cannot see point at it. A
// target's delete clears the references to it, a trigger's work, whatever
// deletes it.
//
// An attachment is a row of an @attachments table's sub-table: a file in
// a bucket. A browser uploads it straight to the bucket through a URL
// Begin signs, so its bytes never pass through the API; Complete reads the
// object back, hashing it as it streams, and holds it to the size and
// SHA-256 Begin declared. File copies or moves a file onto another row,
// the same object, with no second upload. A file's delete enqueues its
// object's release, and Release, which the released queue's worker calls,
// deletes the object once no file of any table names it. Extraction and
// previews are a file processor's (D159): Complete asks extraction, a
// file's first view (Download) asks its preview, and Processed records
// what the processor made.
//
// Nothing here opens a connection: the generated ORM implements Host over
// its pool or its transaction and its repositories, and the definitions
// are the ones it is generated with. Postgres alone: the loader refuses
// either declaration on SQLite.
package rowrefs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// RowRef is a row of a table, by its type's name and its key in its wire
// form: a UUID in base62, as the Go types and the TypeScript runtime write
// one; a key in either form is read.
type RowRef struct {
	Table string `json:"table"`
	Key   string `json:"key"`
}

// RowTable is a sub-table of a row: the sub-table, its column of the
// row's key, and whether that key is a UUID.
type RowTable struct {
	Table string `json:"table"`
	Row   string `json:"row"`
	UUID  bool   `json:"uuid,omitempty"`
}

// ReferencesHolder is a @references table's sub-table, the tables it may
// point at (any when empty), and the most references a row holds.
type ReferencesHolder struct {
	RowTable
	Tables    []string `json:"tables,omitempty"`
	MaxPerRow int      `json:"maxPerRow"`
}

// ReferenceTarget is a table a reference may point at: its table and key
// column, whether its key is a UUID, and an @audience table's owner and
// resolved audience columns.
type ReferenceTarget struct {
	Table      string `json:"table"`
	Key        string `json:"key"`
	UUID       bool   `json:"uuid,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Restricted string `json:"restricted,omitempty"`
}

// CommentsSource is a table's Comments messages sub-table, whose ref
// blocks are the row's references too: its blocks, state and message id
// columns.
type CommentsSource struct {
	RowTable
	Blocks string `json:"blocks"`
	State  string `json:"state"`
	ID     string `json:"id"`
}

// ReferencesDefinition is the references of a database, by holder type,
// target type and conversation's row type (D154).
type ReferencesDefinition struct {
	Holders  map[string]ReferencesHolder `json:"holders"`
	Targets  map[string]ReferenceTarget  `json:"targets"`
	Comments map[string]CommentsSource   `json:"comments,omitempty"`
}

// AttachmentsHolder is an @attachments table's sub-table, its bounds, and
// the released queue's table.
type AttachmentsHolder struct {
	RowTable
	MaxFileBytes int64    `json:"maxFileBytes"`
	MaxRowBytes  int64    `json:"maxRowBytes"`
	MaxFiles     int      `json:"maxFiles"`
	MediaTypes   []string `json:"mediaTypes,omitempty"`
	Released     string   `json:"released"`
}

// AttachmentsDefinition is the attachments of a database, by holder type
// (D154).
type AttachmentsDefinition struct {
	Holders map[string]AttachmentsHolder `json:"holders"`
}

// Definitions are what the generated ORM is generated with: its
// references, nil for a database with none, and its attachments, nil for
// one with none.
type Definitions struct {
	References  *ReferencesDefinition
	Attachments *AttachmentsDefinition
}

// MustParse reads the definitions the generated ORM holds as JSON, each ""
// for none; it panics on JSON no generator wrote.
func MustParse(references, attachments string) Definitions {
	var out Definitions
	if references != "" {
		out.References = &ReferencesDefinition{}
		if err := json.Unmarshal([]byte(references), out.References); err != nil {
			panic(fmt.Sprintf("rowrefs: the references definition: %v", err))
		}
	}
	if attachments != "" {
		out.Attachments = &AttachmentsDefinition{}
		if err := json.Unmarshal([]byte(attachments), out.Attachments); err != nil {
			panic(fmt.Sprintf("rowrefs: the attachments definition: %v", err))
		}
	}
	return out
}

// Row is one row a statement returned, by column.
type Row map[string]any

// Host is what References and Attachments run through: the generated
// ORM's database or transaction. Its statements run as written, reading
// every row; what the caller of ctx sees is Visible's.
type Host interface {
	// Query runs a statement and returns its rows.
	Query(ctx context.Context, sql string, args ...any) ([]Row, error)
	// Exec runs a statement and returns the rows it touched.
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	// Visible is which of keys, of a row of the table typeName, in their
	// wire form, the caller of ctx reads through the table's repository.
	Visible(ctx context.Context, typeName string, keys []string) (map[string]bool, error)
	// Transaction runs fn in a transaction, the host's own, or a
	// savepoint of it, through a host over it.
	Transaction(ctx context.Context, fn func(Host) error) error
	// ActingUser is the user the writes of ctx record, as a UUID's text,
	// and false for none.
	ActingUser(ctx context.Context) (string, bool)
	// Caller is whom the reads of ctx keep to, and false for every row:
	// outside any operation, and for the system actor.
	Caller(ctx context.Context) (access.Principal, bool)
	// NotFound is the ORM's error of a row that is not there.
	NotFound(message string) error
}

// Error is a references or attachments operation the declaration's rules
// refuse: Code names which, Details what.
type Error struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *Error) Error() string { return e.Message }

// The codes of an Error.
const (
	CodeNotAHolder      = "not_a_holder"
	CodeNotATarget      = "not_a_target"
	CodeTableNotAllowed = "table_not_allowed"
	CodeTooMany         = "too_many"
	CodeInvalid         = "invalid"
	CodeMediaType       = "media_type"
	CodeTooLarge        = "too_large"
	CodeRowFull         = "row_full"
	CodeNotPending      = "not_pending"
	CodeExpired         = "expired"
	CodeNotUploaded     = "not_uploaded"
	CodeMismatch        = "mismatch"
	CodeNotReady        = "not_ready"
)

func refuse(code string, details map[string]any, format string, args ...any) error {
	if details == nil {
		details = map[string]any{}
	}
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Details: details}
}

// CodeOf is err's Error code, "" for another error.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// ErrNoDatabase is what a member of a no-op database answers.
var ErrNoDatabase = errors.New("rowrefs: no database")

const (
	defaultLimit = 100
	maxLimit     = 1000
	// maxScan is the most holders ReferencedBy reads of a row before it
	// reads which the reader sees.
	maxScan = 10_000
	// batch is the most keys one Visible asks for.
	batch = 500
)

func limitOf(limit int) (int, error) {
	if limit == 0 {
		return defaultLimit, nil
	}
	if limit < 1 || limit > maxLimit {
		return 0, refuse(CodeInvalid, nil, "a limit is a whole number from 1 to %d, not %d", maxLimit, limit)
	}
	return limit, nil
}

func lock(ctx context.Context, h Host, name string) error {
	_, err := h.Query(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, name)
	return err
}

// visible is which of keys of typeName the reader sees, in batches.
func visible(ctx context.Context, h Host, typeName string, keys []string) (map[string]bool, error) {
	seen := map[string]bool{}
	for i := 0; i < len(keys); i += batch {
		end := min(i+batch, len(keys))
		got, err := h.Visible(ctx, typeName, keys[i:end])
		if err != nil {
			return nil, err
		}
		for k, ok := range got {
			if ok {
				seen[k] = true
			}
		}
	}
	return seen, nil
}

// mustSee fails with not found unless the reader sees row.
func mustSee(ctx context.Context, h Host, row RowRef) error {
	seen, err := visible(ctx, h, row.Table, []string{row.Key})
	if err != nil {
		return err
	}
	if !seen[row.Key] {
		return h.NotFound(fmt.Sprintf("%s %s is not there", row.Table, row.Key))
	}
	return nil
}

// ---- keys ----

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// uuidBytes parses a UUID in either form: hyphenated (36 characters) or
// base62 (1 to 22).
func uuidBytes(text string) ([16]byte, bool) {
	var out [16]byte
	if len(text) == 36 && text[8] == '-' && text[13] == '-' && text[18] == '-' && text[23] == '-' {
		b, err := hex.DecodeString(strings.ReplaceAll(text, "-", ""))
		if err != nil || len(b) != 16 {
			return out, false
		}
		copy(out[:], b)
		return out, true
	}
	if len(text) < 1 || len(text) > 22 {
		return out, false
	}
	n := new(big.Int)
	base := big.NewInt(62)
	for _, c := range text {
		i := strings.IndexRune(base62Alphabet, c)
		if i < 0 {
			return out, false
		}
		n.Mul(n, base).Add(n, big.NewInt(int64(i)))
	}
	b := n.Bytes()
	if len(b) > 16 {
		return out, false
	}
	copy(out[16-len(b):], b)
	return out, true
}

func hyphenated(b [16]byte) string {
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func base62(b [16]byte) string {
	n := new(big.Int).SetBytes(b[:])
	if n.Sign() == 0 {
		return "0"
	}
	var out []byte
	base := big.NewInt(62)
	mod := new(big.Int)
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		out = append(out, base62Alphabet[mod.Int64()])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// UUIDToDB is a UUID's hyphenated text, from either form; "" for text
// that is no UUID.
func UUIDToDB(text string) string {
	b, ok := uuidBytes(text)
	if !ok {
		return ""
	}
	return hyphenated(b)
}

// UUIDFromDB is a UUID's base62 wire form, from either form; text itself
// for text that is no UUID.
func UUIDFromDB(text string) string {
	b, ok := uuidBytes(text)
	if !ok {
		return text
	}
	return base62(b)
}

// dbKey is a row's key as its table's column reads it.
func dbKey(uuid bool, key string) (string, error) {
	if key == "" {
		return "", refuse(CodeInvalid, nil, "a row's key is a non-empty string, not %q", key)
	}
	if !uuid {
		return key, nil
	}
	text := UUIDToDB(key)
	if text == "" {
		return "", refuse(CodeInvalid, nil, "a row's key is a UUID, not %q", key)
	}
	return text, nil
}

// wireKey is a key's wire form, given its column's text.
func wireKey(uuid bool, text string) string {
	if uuid {
		return UUIDFromDB(text)
	}
	return text
}

func sameSubject(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	x, okX := uuidBytes(a)
	y, okY := uuidBytes(b)
	if okX && okY {
		return x == y
	}
	return a == b
}

// newUUID is a random UUID's hyphenated text.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hyphenated(b)
}

// ---- row values ----

func text(row Row, column string) string {
	switch v := row[column].(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

func optional(row Row, column string) *string {
	if row[column] == nil {
		return nil
	}
	s := text(row, column)
	return &s
}

func integer(row Row, column string) int64 {
	switch v := row[column].(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case int:
		return int64(v)
	case int16:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		var n int64
		_, _ = fmt.Sscan(v, &n)
		return n
	}
	return 0
}

func timeOf(row Row, column string) time.Time {
	if t, ok := row[column].(time.Time); ok {
		return t
	}
	return time.Time{}
}

func optionalTime(row Row, column string) *time.Time {
	if t, ok := row[column].(time.Time); ok {
		return &t
	}
	return nil
}

func boolean(row Row, column string) bool {
	b, _ := row[column].(bool)
	return b
}
