// Package paging reads and builds the page of a @paginated operation
// (D60): the generated router reads the request's cursor and limit with
// Read, the implementation takes the Request beside its arguments and
// returns a Page, {items, next}, which the router writes as the
// envelope's data. A cursor is opaque to the caller: the unpadded
// base64url of the compact JSON of the implementation's key, the position
// of the last item of a page in the operation's order. The TypeScript
// runtime's readPage and the Rust runtime's page module read and build the
// same, held to runtime/http/testdata/page_parity.json.
package paging

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
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

// Spec is an operation's @paginated limits, as the router carries them.
type Spec struct {
	DefaultLimit int
	MaxLimit     int
}

// Request is the page a caller asks for: the cursor of the page before,
// "" for the first page, and the limit, its default filled in.
type Request struct {
	Cursor string
	Limit  int
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
// next to the cursor of the last one kept, EncodeCursor(key(row)). limit
// is the request's; one below 1 is taken as 1.
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
// Rust runtimes write it. A key should encode short: a router reads a
// cursor of at most CursorMaxLength characters, about 768 bytes of JSON.
func EncodeCursor(key any) (string, error) {
	var text bytes.Buffer
	encoder := json.NewEncoder(&text)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(key); err != nil {
		return "", fmt.Errorf("paging: encode a cursor's key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes.TrimSuffix(text.Bytes(), []byte("\n"))), nil
}

// DecodeCursor reads a cursor back into key, a pointer to the
// implementation's key type. A cursor that is not of the alphabet, not the
// canonical unpadded base64url of UTF-8 JSON, or whose JSON key does not
// decode into, is refused with the 400 every runtime answers, an
// *apperror.AppError at cursor: the caller starts again. A key that is not
// a pointer is the implementation's error, returned as it is.
func DecodeCursor(cursor string, key any) error {
	if cursor == "" || len(cursor) > CursorMaxLength || !cursorPattern.MatchString(cursor) {
		return RefusedCursor()
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil || !utf8.Valid(data) {
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

// Parse reads a page request from a query: the first value of cursor and
// of limit, an empty one as absent. A cursor is 1 to CursorMaxLength
// characters of the unpadded base64url alphabet; a limit a whole number
// from 1 to the spec's MaxLimit, its DefaultLimit when absent. A refusal
// is the 400 *apperror.AppError at the parameter, the cursor's first.
func Parse(query url.Values, spec Spec) (Request, *apperror.AppError) {
	request := Request{Limit: spec.DefaultLimit}
	if cursor := query.Get(CursorParam); cursor != "" {
		switch {
		case utf8.RuneCountInString(cursor) > CursorMaxLength:
			return Request{}, refuse(CursorParam, fmt.Sprintf("must be at most %d characters", CursorMaxLength), "maxLength")
		case !cursorPattern.MatchString(cursor):
			return Request{}, refuse(CursorParam, "does not match the required pattern", "pattern")
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
	return request, nil
}

// Read reads the request's page (Parse). On a refusal it writes the 400
// problem, with the request's id, and returns false.
func Read(w http.ResponseWriter, r *http.Request, spec Spec) (Request, bool) {
	request, refusal := Parse(r.URL.Query(), spec)
	if refusal != nil {
		response.LoggedErrorWithDetails(w, r, refusal.HTTPStatus(), refusal.Message, refusal.ErrorCode, refusal.Details)
		return Request{}, false
	}
	return request, true
}
