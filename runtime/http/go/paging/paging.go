// Package paging reads and builds the page of a @paginated operation
// (D60): the generated router reads the request's cursor, limit and sort
// with Read, the implementation takes the Request beside its arguments and
// returns a Page, {items, next}, which the router writes as the
// envelope's data. A cursor is opaque to the caller: the unpadded
// base64url of the compact JSON of the implementation's key, the position
// of the last item of a page in the operation's order. A sorted
// operation's cursor also names its sort (D60, amended): the router binds
// the page's next to the request's sort (Bind) and unbinds a sent cursor
// before the implementation reads it (Parse), so the implementation's
// keys are the same with a sort as without. The TypeScript runtime's
// page.ts and the Rust runtime's page module read and build the same, held
// to runtime/http/testdata/page_parity.json.
package paging

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/parable-work/superschematic/runtime/http/go/apperror"
	"github.com/parable-work/superschematic/runtime/http/go/response"
)

// The page's query parameters.
const (
	CursorParam = "cursor"
	LimitParam  = "limit"
)

// CursorMaxLength is the longest cursor a router reads, in characters.
const CursorMaxLength = 1024

// cursorPattern is the unpadded base64url alphabet.
var cursorPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// maxKeyDepth is how deep a cursor's key may not nest its arrays and
// objects: the Rust runtime's JSON reader refuses the 128th level, so
// every runtime refuses it.
const maxKeyDepth = 128

// integerText is a whole number as a query sends one: a sign, then digits.
var integerText = regexp.MustCompile(`^[+-]?[0-9]+$`)

// maxSafeInteger is the largest integer every runtime reads exactly, 2^53
// - 1, which JavaScript's numbers stop at.
const maxSafeInteger = 1<<53 - 1

// RefusedCursorDetail and RefusedCursorReason are the 400 of a cursor
// the implementation cannot read (DecodeCursor).
const (
	RefusedCursorDetail = "cursor is not one this operation returned"
	RefusedCursorReason = "not a cursor this operation returned"
)

// SortedCursorDetail and SortedCursorReason are the 400 of a cursor
// returned under another sort (D60, amended).
const (
	SortedCursorDetail = "cursor is not one this sort returned"
	SortedCursorReason = "returned under another sort"
)

// Spec is an operation's @paginated limits, and its SortBy argument, as
// the router carries them.
type Spec struct {
	DefaultLimit int
	MaxLimit     int
	// Sort is the operation's SortBy argument (D60, amended); nil for an
	// operation without one.
	Sort *SortSpec
}

// Request is the page a caller asks for: the cursor of the page before,
// "" for the first page, the limit, its default filled in, and the sort.
type Request struct {
	// Cursor is the implementation's own cursor, its key's, as
	// EncodeCursor wrote it: a sorted operation's router has unbound it
	// from the sort it was returned under.
	Cursor string
	Limit  int
	// Sort is the order the page is read in: the one sent, or the spec's
	// default when the request sends none. Zero for an operation without
	// a sort.
	Sort Sort
}

// Sort is the order a page is read in (D60, amended): a field of the item
// type, by its JSON name, ascending, or descending when Desc; ties break
// by the item's id in the same direction. Zero for an operation without a
// SortBy argument.
type Sort struct {
	Field string
	Desc  bool
}

// String is the sort as a query writes it: "placedAt", "-placedAt"; "" for
// the zero Sort.
func (s Sort) String() string {
	if s.Desc {
		return "-" + s.Field
	}
	return s.Field
}

// ParseSort reads a sort as a query writes it, "placedAt" or "-placedAt":
// ok is false for any other text, a field's name being a letter or _, then
// letters, digits and _.
func ParseSort(text string) (sort Sort, ok bool) {
	if !sortPattern.MatchString(text) {
		return Sort{}, false
	}
	field, desc := strings.CutPrefix(text, "-")
	return Sort{Field: field, Desc: desc}, true
}

// sortPattern is a sort's text: a field's name, after a "-" descending.
var sortPattern = regexp.MustCompile(`^-?[A-Za-z_][A-Za-z0-9_]*$`)

// SortSpec is an operation's SortBy argument as the router carries it: its
// query parameter, its fields by their JSON names, and @paginated's
// defaultSort, the order of a request that sends none.
type SortSpec struct {
	Param   string
	Fields  []string
	Default Sort
}

// Values are the values the parameter takes: each field, then the field
// after "-" (placedAt, -placedAt, totalCents, -totalCents).
func (s SortSpec) Values() []string {
	values := make([]string, 0, 2*len(s.Fields))
	for _, field := range s.Fields {
		values = append(values, field, "-"+field)
	}
	return values
}

// Page is what a paged operation returns: the page's items, which may be
// fewer than its limit, even none, and the cursor of the next page, nil
// after the last.
type Page[T any] struct {
	Items []T     `json:"items"`
	Next  *string `json:"next"`
}

// MarshalJSON writes nil Items as [] and nil Next as null.
func (p Page[T]) MarshalJSON() ([]byte, error) {
	items := p.Items
	if items == nil {
		items = []T{}
	}
	return json.Marshal(struct {
		Items []T     `json:"items"`
		Next  *string `json:"next"`
	}{items, p.Next})
}

// Last is the last page: items and no next.
func Last[T any](items []T) Page[T] {
	return Page[T]{Items: items}
}

// More is a page with a next page after it.
func More[T any](items []T, next string) Page[T] {
	return Page[T]{Items: items, Next: &next}
}

// FromRows is the page of rows an implementation read for a request: it
// reads up to limit+1 rows in the operation's order, after the request's
// cursor, and FromRows keeps limit of them and, when there were more, sets
// next to the cursor of the last one kept, EncodeCursor(key(row)), whose
// error it returns. limit is the request's; one below 1 is taken as 1.
func FromRows[T any](rows []T, limit int, key func(T) any) (Page[T], error) {
	limit = max(limit, 1)
	if len(rows) <= limit {
		return Last(rows), nil
	}
	items := rows[:limit:limit]
	next, err := EncodeCursor(key(items[limit-1]))
	if err != nil {
		return Page[T]{}, err
	}
	return More(items, next), nil
}

// EncodeCursor is the cursor of a position: the unpadded base64url of the
// compact JSON of key, written without HTML escapes, as the TypeScript and
// Rust runtimes write it. A key every router would refuse to read back is
// an error, the implementation's: one whose cursor is past CursorMaxLength
// characters, about 768 bytes of JSON, and one DecodeCursor refuses
// (JSON null, an empty object, a number past float64's range, a lone
// surrogate escape, arrays and objects nested 128 deep).
func EncodeCursor(key any) (string, error) {
	var text bytes.Buffer
	encoder := json.NewEncoder(&text)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(key); err != nil {
		return "", fmt.Errorf("paging: encode a cursor's key: %w", err)
	}
	data := bytes.TrimSuffix(text.Bytes(), []byte("\n"))
	if problem := keyProblem(data); problem != "" {
		return "", fmt.Errorf("paging: encode a cursor's key: %s, which no router reads back", problem)
	}
	cursor := base64.RawURLEncoding.EncodeToString(data)
	if len(cursor) > CursorMaxLength {
		return "", fmt.Errorf("paging: encode a cursor's key: its cursor is %d characters, past the %d a router reads", len(cursor), CursorMaxLength)
	}
	return cursor, nil
}

// DecodeCursor reads a cursor back into key, a pointer to the
// implementation's key type. A cursor that is not of the alphabet, not the
// canonical unpadded base64url of UTF-8 JSON, whose JSON names no position
// or holds what another runtime cannot read (keyProblem), or whose JSON
// does not decode into key, is refused with the 400 every runtime answers,
// an *apperror.AppError at cursor: the caller starts again. An object
// that lacks a member of key's type decodes, its field left zero, so an
// implementation refuses a key without its id itself (RefusedCursor). A
// key that is not a non-nil pointer is the implementation's error,
// returned as it is.
func DecodeCursor(cursor string, key any) error {
	if value := reflect.ValueOf(key); value.Kind() != reflect.Pointer || value.IsNil() {
		return fmt.Errorf("paging: decode a cursor: %w", &json.InvalidUnmarshalError{Type: reflect.TypeOf(key)})
	}
	if cursor == "" || len(cursor) > CursorMaxLength || !cursorPattern.MatchString(cursor) {
		return RefusedCursor()
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil || !utf8.Valid(data) || !json.Valid(data) || keyProblem(data) != "" {
		return RefusedCursor()
	}
	if err := json.Unmarshal(data, key); err != nil {
		var invalid *json.InvalidUnmarshalError
		if errors.As(err, &invalid) {
			return fmt.Errorf("paging: decode a cursor: %w", err)
		}
		return RefusedCursor()
	}
	return nil
}

// keyProblem is why every runtime refuses to read a cursor's valid JSON,
// or "": the JSON null or an empty object, which name no position; a
// number past float64's range, which the TypeScript runtime would read as
// Infinity; a \u escape of a lone surrogate, which the Go runtime would
// read as U+FFFD; and arrays and objects nested maxKeyDepth deep, which
// the Rust runtime's reader refuses.
func keyProblem(data []byte) string {
	trimmed := bytes.TrimSpace(data)
	switch {
	case string(trimmed) == "null":
		return "the key is null, which names no position"
	case len(trimmed) >= 2 && trimmed[0] == '{' && len(bytes.TrimSpace(trimmed[1:len(trimmed)-1])) == 0:
		return "the key is an empty object, which names no position"
	}
	depth := 0
	for i := 0; i < len(data); i++ {
		switch c := data[i]; {
		case c == '[' || c == '{':
			if depth++; depth >= maxKeyDepth {
				return fmt.Sprintf("the key nests %d arrays or objects deep", maxKeyDepth)
			}
		case c == ']' || c == '}':
			depth--
		case c == '"':
			// The JSON is valid, so each string closes and each escape is
			// whole.
			for i++; data[i] != '"'; i++ {
				if data[i] != '\\' {
					continue
				}
				if i++; data[i] != 'u' {
					continue
				}
				r := hexRune(data[i+1 : i+5])
				i += 4
				if r < 0xD800 || r > 0xDFFF {
					continue
				}
				if r <= 0xDBFF && i+6 < len(data) && data[i+1] == '\\' && data[i+2] == 'u' {
					if low := hexRune(data[i+3 : i+7]); low >= 0xDC00 && low <= 0xDFFF {
						i += 6
						continue
					}
				}
				return "a string of the key escapes a lone surrogate"
			}
		case c == '-' || (c >= '0' && c <= '9'):
			end := i + 1
			for end < len(data) && strings.IndexByte("+-.0123456789Ee", data[end]) >= 0 {
				end++
			}
			if f, _ := strconv.ParseFloat(string(data[i:end]), 64); math.IsInf(f, 0) {
				return "a number of the key is past float64's range"
			}
			i = end - 1
		}
	}
	return ""
}

// hexRune is the code unit of a \u escape's four hex digits.
func hexRune(digits []byte) rune {
	n, _ := strconv.ParseUint(string(digits), 16, 32)
	return rune(n)
}

// RefusedCursor is the 400 of a cursor the implementation cannot read,
// which it may return itself for a cursor that decodes but names no
// position of its order.
func RefusedCursor() *apperror.AppError {
	return badRequest(RefusedCursorDetail, Details{Location: "query", Parameter: CursorParam, Reason: RefusedCursorReason})
}

// Details is the details member of a refused page request, as every
// runtime writes a refused query parameter's: where, which parameter,
// why, and the rule it broke.
type Details struct {
	Location  string      `json:"location"`
	Parameter string      `json:"parameter"`
	Reason    string      `json:"reason"`
	Errors    []RuleError `json:"errors,omitempty"`
}

// RuleError is one broken rule of a refused parameter: its validator
// (min, max, maxLength, pattern) and message.
type RuleError struct {
	Validator string `json:"validator"`
	Message   string `json:"message"`
}

func badRequest(detail string, details Details) *apperror.AppError {
	return apperror.BadRequestError(detail, nil).WithCode(response.ValidationCode).WithDetails(details)
}

// refuse is the 400 of a parameter the request carries, worded as the
// TypeScript runtime's params.ts words a string or integer query
// parameter's, with the broken rule when there is one.
func refuse(parameter, reason, validator string) *apperror.AppError {
	details := Details{Location: "query", Parameter: parameter, Reason: reason}
	if validator != "" {
		details.Errors = []RuleError{{Validator: validator, Message: reason}}
	}
	return badRequest(fmt.Sprintf("Invalid query parameter %s: %s", parameter, reason), details)
}

// Parse reads a page request from a query, the router's whole read: the
// first value of cursor, of limit and of the spec's sort parameter, an
// empty one as absent. A cursor is 1 to CursorMaxLength characters of the
// unpadded base64url alphabet, the alphabet checked first, on bytes, so a
// cursor outside ASCII is refused for its pattern however long it is; a
// limit a whole number from 1 to the spec's MaxLimit, its DefaultLimit
// when absent; a sort one of the spec's Values, its Default when absent.
// Last, a sorted operation's cursor is unbound from the sort it was
// returned under (UnbindCursor), so Request.Cursor is the
// implementation's own. A refusal is the 400 *apperror.AppError at the
// parameter, in this order: the cursor's text, the limit, the sort, then
// the cursor's sort.
func Parse(query url.Values, spec Spec) (Request, *apperror.AppError) {
	request := Request{Limit: spec.DefaultLimit}
	if cursor := query.Get(CursorParam); cursor != "" {
		switch {
		case !cursorPattern.MatchString(cursor):
			return Request{}, refuse(CursorParam, "does not match the required pattern", "pattern")
		case len(cursor) > CursorMaxLength:
			return Request{}, refuse(CursorParam, fmt.Sprintf("must be at most %d characters", CursorMaxLength), "maxLength")
		}
		request.Cursor = cursor
	}
	if text := query.Get(LimitParam); text != "" {
		if !integerText.MatchString(text) {
			return Request{}, refuse(LimitParam, "expected an integer", "")
		}
		limit, err := strconv.ParseInt(text, 10, 64)
		if err != nil || limit > maxSafeInteger || limit < -maxSafeInteger {
			return Request{}, refuse(LimitParam, "integer out of range", "")
		}
		switch {
		case limit < 1:
			return Request{}, refuse(LimitParam, "must be at least 1", "min")
		case limit > int64(spec.MaxLimit):
			return Request{}, refuse(LimitParam, fmt.Sprintf("must be at most %d", spec.MaxLimit), "max")
		}
		request.Limit = int(limit)
	}
	if spec.Sort == nil {
		return request, nil
	}
	request.Sort = spec.Sort.Default
	if text := query.Get(spec.Sort.Param); text != "" {
		values := spec.Sort.Values()
		if !slices.Contains(values, text) {
			return Request{}, refuse(spec.Sort.Param, "expected one of "+strings.Join(values, ", "), "")
		}
		request.Sort, _ = ParseSort(text)
	}
	if request.Cursor != "" {
		cursor, refusal := UnbindCursor(request.Cursor, request.Sort)
		if refusal != nil {
			return Request{}, refusal
		}
		request.Cursor = cursor
	}
	return request, nil
}

// The bytes a bound cursor's JSON starts with, follows its sort with, and
// ends with: {"sort":"-placedAt","key":K}.
const (
	boundPrefix = `{"sort":"`
	boundKey    = `","key":`
	boundSuffix = `}`
)

// BindCursor is the cursor of the implementation's cursor under sort
// (D60, amended): the unpadded base64url of exactly the bytes
// {"sort":"-placedAt","key":K}, K the key's JSON as the implementation's
// cursor holds it, byte for byte. A cursor DecodeCursor would refuse, a
// sort that is no sort's text, and a bound cursor past CursorMaxLength
// characters are errors, the implementation's.
func BindCursor(cursor string, sort Sort) (string, error) {
	var value json.RawMessage
	if err := DecodeCursor(cursor, &value); err != nil {
		return "", fmt.Errorf("paging: bind a cursor: %q is not a cursor EncodeCursor writes", cursor)
	}
	// The key's bytes as the cursor holds them, which DecodeCursor read.
	key, _ := base64.RawURLEncoding.Strict().DecodeString(cursor)
	text := sort.String()
	if !sortPattern.MatchString(text) {
		return "", fmt.Errorf("paging: bind a cursor: the sort %q is not a field, or - and a field", text)
	}
	data := make([]byte, 0, len(boundPrefix)+len(text)+len(boundKey)+len(key)+len(boundSuffix))
	data = append(append(append(append(append(data, boundPrefix...), text...), boundKey...), key...), boundSuffix...)
	bound := base64.RawURLEncoding.EncodeToString(data)
	if len(bound) > CursorMaxLength {
		return "", fmt.Errorf("paging: bind a cursor: its cursor under %s is %d characters, past the %d a router reads", text, len(bound), CursorMaxLength)
	}
	return bound, nil
}

// UnbindCursor is the implementation's cursor of one bound under sort
// (D60, amended): the key's JSON of {"sort":"-placedAt","key":K}, read by
// its bytes, encoded again as EncodeCursor would. A cursor bound under
// another sort is the 400 SortedCursorDetail at cursor; one that is not of
// the alphabet, not canonical unpadded base64url, or not those bytes
// around a key, names no sort and is RefusedCursor. The key itself is the
// implementation's DecodeCursor's to read.
func UnbindCursor(cursor string, sort Sort) (string, *apperror.AppError) {
	if cursor == "" || len(cursor) > CursorMaxLength || !cursorPattern.MatchString(cursor) {
		return "", RefusedCursor()
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil {
		return "", RefusedCursor()
	}
	rest, ok := bytes.CutPrefix(data, []byte(boundPrefix))
	if !ok {
		return "", RefusedCursor()
	}
	end := bytes.IndexByte(rest, '"')
	if end < 0 || !sortPattern.Match(rest[:end]) {
		return "", RefusedCursor()
	}
	text := string(rest[:end])
	rest, ok = bytes.CutPrefix(rest[end:], []byte(boundKey))
	if !ok {
		return "", RefusedCursor()
	}
	key, ok := bytes.CutSuffix(rest, []byte(boundSuffix))
	if !ok || len(key) == 0 {
		return "", RefusedCursor()
	}
	if text != sort.String() {
		return "", SortedCursor()
	}
	return base64.RawURLEncoding.EncodeToString(key), nil
}

// SortedCursor is the 400 of a cursor returned under another sort.
func SortedCursor() *apperror.AppError {
	return badRequest(SortedCursorDetail, Details{Location: "query", Parameter: CursorParam, Reason: SortedCursorReason})
}

// Bind is the page an implementation returned with its next cursor bound
// to the request's sort (BindCursor), which the router answers; the page
// as it is when the request has no sort or the page no next. Its error is
// BindCursor's, the implementation's.
func Bind[T any](page Page[T], request Request) (Page[T], error) {
	if page.Next == nil || request.Sort == (Sort{}) {
		return page, nil
	}
	bound, err := BindCursor(*page.Next, request.Sort)
	if err != nil {
		return Page[T]{}, err
	}
	page.Next = &bound
	return page, nil
}

// Read reads the request's page (Parse) from its raw query, read as
// QueryValues reads one. On a refusal it writes the 400 problem, with the
// request's id, and returns false.
func Read(w http.ResponseWriter, r *http.Request, spec Spec) (Request, bool) {
	request, refusal := Parse(QueryValues(r.URL.RawQuery), spec)
	if refusal != nil {
		response.LoggedErrorWithDetails(w, r, refusal.HTTPStatus(), refusal.Message, refusal.ErrorCode, refusal.Details)
		return Request{}, false
	}
	return request, true
}

// QueryValues reads a raw query as the TypeScript runtime's URLSearchParams
// and the Rust runtime's form_urlencoded read one (WHATWG's
// application/x-www-form-urlencoded parser): pairs split at & alone, a name
// from its value at the first =, + a space, and a % that starts no escape
// of two hex digits kept as it is. url.ParseQuery drops a pair with a
// malformed escape and splits at ;, so ?limit=%zz and ?limit=500;x=1 would
// be the first page where the other runtimes refuse the limit; read this
// way, they are the limit "%zz" and "500;x=1", which Parse refuses as
// theirs do.
func QueryValues(raw string) url.Values {
	values := url.Values{}
	for pair := range strings.SplitSeq(raw, "&") {
		if pair == "" {
			continue
		}
		name, value, _ := strings.Cut(pair, "=")
		name, value = formDecode(name), formDecode(value)
		values[name] = append(values[name], value)
	}
	return values
}

// formDecode is one name or value of a form-encoded query: + a space, each
// % and two hex digits their byte, and any other % itself.
func formDecode(text string) string {
	if !strings.ContainsAny(text, "+%") {
		return text
	}
	out := make([]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '+':
			out = append(out, ' ')
		case c == '%' && i+2 < len(text) && isHex(text[i+1]) && isHex(text[i+2]):
			n, _ := strconv.ParseUint(text[i+1:i+3], 16, 8)
			out = append(out, byte(n))
			i += 2
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}
