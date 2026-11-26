package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// idempotencyKey is the context key of the key a caller names for a call
// to an @idempotent operation, and idempotentCall that of the key the call
// sends (D140).
type (
	idempotencyKey struct{}
	idempotentCall struct{}
)

// WithIdempotencyKey returns a context whose call to an @idempotent
// operation sends key as its Idempotency-Key (D140): a retry that sends the
// same key is answered what the first call was, and runs nothing twice.
// Without it the method makes a UUID for each call, which covers the
// client's own retries of that call. Name one to retry across calls, or
// across restarts. A key is 1 to 255 printable ASCII characters. An
// operation that is not @idempotent sends none.
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, idempotencyKey{}, key)
}

// IdempotentCall returns the context of one call to an @idempotent
// operation: it carries the key the call sends, the one WithIdempotencyKey
// named or a new UUID (D140). The generated methods call it.
func IdempotentCall(ctx context.Context) context.Context {
	key, _ := ctx.Value(idempotencyKey{}).(string)
	if key == "" {
		key = newIdempotencyKey()
	}
	return context.WithValue(ctx, idempotentCall{}, key)
}

// CallIdempotencyKey is the Idempotency-Key the call of ctx sends, false
// for a call to an operation that is not @idempotent.
func CallIdempotencyKey(ctx context.Context) (string, bool) {
	key, ok := ctx.Value(idempotentCall{}).(string)
	return key, ok
}

// newIdempotencyKey is a UUID v4.
func newIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on a supported platform; a key that
		// is not random would make two calls one.
		panic("idempotency key: " + err.Error())
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// DownloadedFile is the file a FileDownload operation answers (D81): its
// bytes, the filename Content-Disposition offered, "" for none, and the
// Content-Type it was sent with.
type DownloadedFile struct {
	Body        []byte
	Filename    string
	ContentType string
}

// BinaryStream is the success body of an @httpBinaryStream operation
// (D167), unread. The caller reads Body and must Close it. Closing it
// before its end ends the request. ContentType is the media type the
// schema declares, which the client checked before it returned, and Header
// the response's headers.
type BinaryStream struct {
	ContentType string
	Header      http.Header
	Body        io.ReadCloser
}

// Close closes the body.
func (s *BinaryStream) Close() error {
	return s.Body.Close()
}

// FilenameOf is the filename a Content-Disposition offers: filename*, the
// name in UTF-8 (RFC 8187), before filename; "" for none.
func FilenameOf(disposition string) string {
	_, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		return ""
	}
	return params["filename"]
}

type PublicEncryptionKey struct {
	PublicKey string
	Algorithm string
	KeyID     string
}

type EncryptedRequestOptions struct {
	PublicEncryptionKey *PublicEncryptionKey
}

type EncryptedPayloadEnvelope struct {
	Algorithm    string `json:"algorithm"`
	Payload      string `json:"payload"`
	EncryptedKey string `json:"encryptedKey,omitempty"`
	IV           string `json:"iv,omitempty"`
	KeyID        string `json:"keyId"`
}

type UploadFile struct {
	Filename    string
	Reader      []byte
	ContentType string
}

type MultipartBody struct {
	JSONData any
	Files    map[string]UploadFile
}

func StripFieldsForMultipart(input any, fieldNames []string) (any, error) {
	if input == nil || len(fieldNames) == 0 {
		return input, nil
	}

	raw, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal multipart input: %w", err)
	}
	result := map[string]any{}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal multipart input: %w", err)
	}
	for _, fieldName := range fieldNames {
		delete(result, fieldName)
	}
	return result, nil
}

func AddQueryParam(values url.Values, key string, value any) {
	if value == nil {
		return
	}
	values.Set(key, fmt.Sprintf("%v", value))
}

// AddQueryList sets key to the items of list as one comma-separated value
// (?key=a,b), the form the Go route reads a list query parameter in. An
// empty list sets nothing: the route refuses a present empty value.
func AddQueryList[T any](values url.Values, key string, list []T) {
	if len(list) == 0 {
		return
	}
	items := make([]string, len(list))
	for i, item := range list {
		items[i] = fmt.Sprint(item)
	}
	values.Set(key, strings.Join(items, ","))
}

// PageRequest is the page a paged operation's method asks for (D60): Cursor
// is the Next of the page before, "" for the first page, and Limit the most
// items the page holds, 0 for the operation's default.
type PageRequest struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// Page is one page of a paged operation's list (D60): its items, which may
// be fewer than the limit asked for, even none, and Next, the cursor of the
// next page, nil after the last. Ask for the next page with
// PageRequest{Cursor: *page.Next}.
type Page[T any] struct {
	Items []T     `json:"items"`
	Next  *string `json:"next"`
}

// AddPageRequest sets a page request's cursor and limit in values as the
// query parameters cursor and limit, leaving out each that asks for the
// default.
func AddPageRequest(values url.Values, page PageRequest) {
	if page.Cursor != "" {
		values.Set("cursor", page.Cursor)
	}
	if page.Limit != 0 {
		values.Set("limit", strconv.Itoa(page.Limit))
	}
}

// AllItems yields the items of every page of a paged operation in turn
// (D60). fetch asks for one page: first with page, then with the Next of
// the page before and page's Limit. It asks for a page only when the loop
// has taken every item of the one before, and stops after the page whose
// Next is nil or when the loop stops. An error ends the iteration, yielded
// with the zero item: the one fetch returned, or, for a page whose Next is
// the cursor it was asked with, one naming the operation rather than a
// loop.
func AllItems[T any](operation string, page PageRequest, fetch func(PageRequest) (Page[T], error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for {
			current, err := fetch(page)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range current.Items {
				if !yield(item, nil) {
					return
				}
			}
			if current.Next == nil {
				return
			}
			if *current.Next == page.Cursor {
				var zero T
				yield(zero, fmt.Errorf("%s returned the cursor it was sent", operation))
				return
			}
			page.Cursor = *current.Next
		}
	}
}
