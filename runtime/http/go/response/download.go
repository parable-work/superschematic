package response

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// FileDownload is what a Go implementation returns for an operation whose
// result is a FileDownload<C> (D81, D141): the file the route answers in
// place of the JSON envelope. The route sends Body as it reads it, so a
// reader that produces the file as it goes streams it, and closes Body
// when it is an io.Closer, read or not.
type FileDownload struct {
	// Body is the file's bytes.
	Body io.Reader
	// Filename is the name Content-Disposition offers; empty offers the
	// schema's, or none when the schema names none.
	Filename string
	// ContentType is the body's media type, one the schema lists in
	// contentType or contentTypes (D141); empty answers the schema's
	// contentType. One outside the list is a 500.
	ContentType string
	// ETag is the file's entity tag (D141): an opaque value, which the
	// route quotes, or a tag as the header carries it, `"v3"` or `W/"v3"`.
	ETag string
	// LastModified is when the file last changed (D141), sent as
	// Last-Modified to the second; the zero time sends none.
	LastModified time.Time
	// Header holds headers beside the ones the route writes, such as Vary.
	// A Cache-Control here replaces the schema's.
	Header http.Header
}

// FileSpec is what a FileDownload<C> result's schema says of its answer
// (D81, D141).
type FileSpec struct {
	// ContentType is the Content-Type the route answers with.
	ContentType string
	// ContentTypes lists the media types the implementation may name in
	// ContentType's place.
	ContentTypes []string
	// Filename is the name Content-Disposition offers when the
	// implementation names none.
	Filename string
	// Disposition is Content-Disposition's type, "inline" or "attachment";
	// empty is attachment.
	Disposition string
	// CacheControl is the answer's Cache-Control, sent as written; empty
	// answers no-store.
	CacheControl string
	// Sandbox adds Content-Security-Policy: sandbox and
	// Cross-Origin-Resource-Policy: same-origin, and SandboxAllow the
	// policy's tokens ("allow-scripts"). A SandboxAllow that holds a token
	// is a sandbox too.
	Sandbox      bool
	SandboxAllow []string
}

// SandboxPolicy is the Content-Security-Policy of a sandboxed file answer,
// "" for a spec with no sandbox.
func (s FileSpec) SandboxPolicy() string {
	if !s.Sandbox && len(s.SandboxAllow) == 0 {
		return ""
	}
	return strings.Join(append([]string{"sandbox"}, s.SandboxAllow...), " ")
}

// DownloadFilename is the name a filename offers once its directories and
// control characters are dropped: what follows the last / or \, without
// C0 controls or DEL; "" for nothing left, ".", or "..".
func DownloadFilename(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if name == "." || name == ".." {
		return ""
	}
	return name
}

// isAttrChar reports RFC 8187's attr-char: what filename* carries without
// percent-encoding.
func isAttrChar(b byte) bool {
	switch {
	case 'A' <= b && b <= 'Z', 'a' <= b && b <= 'z', '0' <= b && b <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", b) >= 0
}

// ContentDisposition is the Content-Disposition of an attachment named
// name (D81, RFC 6266): `attachment`, or with a name, `attachment;
// filename="<ASCII>"; filename*=<the name>`, where the ASCII fallback has
// each character outside printable ASCII, and each of `"`, `\` and `%`,
// replaced by `_`, and filename* is the name in UTF-8, percent-encoded
// outside RFC 8187's attr-char. The TypeScript runtime's
// contentDisposition writes the same header
// (runtime/http/testdata/content_disposition_parity.json).
func ContentDisposition(name string) string {
	return Disposition("attachment", name)
}

// Disposition is ContentDisposition under another type (D141): "inline"
// writes that word in attachment's place. An empty disposition is
// attachment.
func Disposition(disposition, name string) string {
	if disposition == "" {
		disposition = "attachment"
	}
	filename := DownloadFilename(name)
	if filename == "" {
		return disposition
	}
	var fallback, encoded strings.Builder
	for _, r := range filename {
		if r >= 0x20 && r <= 0x7e && r != '"' && r != '\\' && r != '%' {
			fallback.WriteRune(r)
		} else {
			fallback.WriteByte('_')
		}
		var buf [utf8.UTFMax]byte
		for _, b := range buf[:utf8.EncodeRune(buf[:], r)] {
			if isAttrChar(b) {
				encoded.WriteByte(b)
			} else {
				fmt.Fprintf(&encoded, "%%%02X", b)
			}
		}
	}
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, disposition, fallback.String(), encoded.String())
}

// isTagChar reports an etagc within printable ASCII: what a tag holds
// between its quotes.
func isTagChar(b byte) bool {
	return b == 0x21 || (b >= 0x23 && b <= 0x7e)
}

// EntityTag is the ETag header of a tag an implementation names (D141):
// the tag itself when it is written as the header carries one, `"v3"` or
// `W/"v3"`, and the value in quotes otherwise. A value that holds a quote,
// a space or a character outside printable ASCII is refused.
func EntityTag(tag string) (string, error) {
	quoted := strings.TrimPrefix(tag, "W/")
	if len(quoted) >= 2 && quoted[0] == '"' && quoted[len(quoted)-1] == '"' {
		inner := quoted[1 : len(quoted)-1]
		valid := true
		for i := 0; i < len(inner); i++ {
			valid = valid && isTagChar(inner[i])
		}
		if valid {
			return tag, nil
		}
	}
	if tag == "" {
		return "", fmt.Errorf("a FileDownload etag is printable ASCII with no space and no quote, or a quoted tag, not %q", tag)
	}
	for i := 0; i < len(tag); i++ {
		if !isTagChar(tag[i]) {
			return "", fmt.Errorf("a FileDownload etag is printable ASCII with no space and no quote, or a quoted tag, not %q", tag)
		}
	}
	return `"` + tag + `"`, nil
}

// IfNoneMatchNames reports whether an If-None-Match header names etag, by
// the weak comparison (RFC 9110, 8.8.3.2): `*` names every tag, and a list
// names the tags it holds, a W/ prefix on either side ignored. A list that
// does not parse names nothing past the point it stops parsing.
func IfNoneMatchNames(header, etag string) bool {
	opaque := strings.TrimPrefix(etag, "W/")
	rest := strings.TrimSpace(header)
	if rest == "*" {
		return true
	}
	for rest != "" {
		rest = strings.TrimPrefix(rest, "W/")
		if !strings.HasPrefix(rest, `"`) {
			return false
		}
		end := strings.IndexByte(rest[1:], '"')
		if end < 0 {
			return false
		}
		if rest[:end+2] == opaque {
			return true
		}
		rest = strings.TrimLeft(rest[end+2:], " \t")
		if rest == "" || rest[0] != ',' {
			return false
		}
		rest = strings.TrimLeft(rest[1:], " \t,")
	}
	return false
}

const (
	httpDay     = `(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)`
	httpLongDay = `(?:Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday)`
	httpMonth   = `(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)`
	httpClock   = `(\d{2}):(\d{2}):(\d{2})`
)

var (
	// Sun, 06 Nov 1994 08:49:37 GMT
	imfFixdate = regexp.MustCompile(`^` + httpDay + `, (\d{2}) ` + httpMonth + ` (\d{4}) ` + httpClock + ` GMT$`)
	// Sunday, 06-Nov-94 08:49:37 GMT
	rfc850Date = regexp.MustCompile(`^` + httpLongDay + `, (\d{2})-` + httpMonth + `-(\d{2}) ` + httpClock + ` GMT$`)
	// Sun Nov  6 08:49:37 1994
	asctimeDate = regexp.MustCompile(`^` + httpDay + ` ` + httpMonth + ` ([ \d]\d) ` + httpClock + ` (\d{4})$`)
	httpMonths  = []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
)

func utcSeconds(year, month, day, hour, minute, second string) (int64, bool) {
	number := func(text string) int {
		value, _ := strconv.Atoi(strings.TrimSpace(text))
		return value
	}
	monthIndex := 0
	for i, name := range httpMonths {
		if name == month {
			monthIndex = i + 1
		}
	}
	y, d, h, m, s := number(year), number(day), number(hour), number(minute), number(second)
	if h > 23 || m > 59 || s > 59 || d < 1 {
		return 0, false
	}
	date := time.Date(y, time.Month(monthIndex), d, h, m, s, 0, time.UTC)
	// A day the month does not hold rolls into the next one.
	if int(date.Month()) != monthIndex || date.Day() != d {
		return 0, false
	}
	return date.Unix(), true
}

// ParseHTTPDate is the time an HTTP date names, in seconds since the
// epoch: the fixed format, `Sun, 06 Nov 1994 08:49:37 GMT`, and the two
// obsolete ones a recipient still reads, `Sunday, 06-Nov-94 08:49:37 GMT`,
// whose year is 1969 to 2068, and `Sun Nov  6 08:49:37 1994` (RFC 9110,
// 5.6.7). The day of the week is not checked against the date. It is
// stricter than net/http's ParseTime, which reads a zone's name by the
// host's zone database, so that every runtime reads a date alike.
func ParseHTTPDate(value string) (int64, bool) {
	if m := imfFixdate.FindStringSubmatch(value); m != nil {
		return utcSeconds(m[3], m[2], m[1], m[4], m[5], m[6])
	}
	if m := rfc850Date.FindStringSubmatch(value); m != nil {
		short, _ := strconv.Atoi(m[3])
		year := 2000 + short
		if short >= 69 {
			year = 1900 + short
		}
		return utcSeconds(strconv.Itoa(year), m[2], m[1], m[4], m[5], m[6])
	}
	if m := asctimeDate.FindStringSubmatch(value); m != nil {
		return utcSeconds(m[6], m[1], m[2], m[3], m[4], m[5])
	}
	return 0, false
}

// NotModified reports whether a request's validators say its copy of the
// file is current (D141): its If-None-Match names etag, or, when it sends
// no If-None-Match, its If-Modified-Since is at or after lastModified.
// etag is the header's form, "" for a file with none, and a zero
// lastModified is a file with none. Only a GET or a HEAD is conditional.
func NotModified(r *http.Request, etag string, lastModified time.Time) bool {
	if r == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	if values, sent := r.Header["If-None-Match"]; sent {
		return etag != "" && IfNoneMatchNames(strings.Join(values, ", "), etag)
	}
	since := r.Header.Get("If-Modified-Since")
	if since == "" || lastModified.IsZero() {
		return false
	}
	seconds, ok := ParseHTTPDate(since)
	return ok && lastModified.Unix() <= seconds
}

// File answers a FileDownload result as an attachment of one media type:
// ServeFile with a spec of contentType and defaultFilename alone.
func File(w http.ResponseWriter, r *http.Request, contentType, defaultFilename string, file *FileDownload) {
	ServeFile(w, r, FileSpec{ContentType: contentType, Filename: defaultFilename}, file)
}

// ServeFile answers a FileDownload result (D81, D141): the file's headers,
// its Content-Type, the Content-Disposition of its filename, or the spec's
// when it names none, its validators, then its body. Every answer says
// X-Content-Type-Options: nosniff, a sandboxed one carries its policy, and
// Cache-Control is the spec's, no-store without one. A request whose
// validators match the file's is answered 304 and the body is closed
// unread. A nil file, a media type the spec does not list and an entity
// tag no header can carry are the implementation's errors, a 500.
func ServeFile(w http.ResponseWriter, r *http.Request, spec FileSpec, file *FileDownload) {
	if file == nil || file.Body == nil {
		LoggedError(w, r, http.StatusInternalServerError, "An unexpected error occurred", "internal_error")
		return
	}
	if closer, ok := file.Body.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}
	contentType := spec.ContentType
	if file.ContentType != "" {
		listed := file.ContentType == spec.ContentType
		for _, candidate := range spec.ContentTypes {
			listed = listed || candidate == file.ContentType
		}
		if !listed {
			LoggedError(w, r, http.StatusInternalServerError, "An unexpected error occurred", "internal_error")
			return
		}
		contentType = file.ContentType
	}
	etag := ""
	if file.ETag != "" {
		tag, err := EntityTag(file.ETag)
		if err != nil {
			LoggedError(w, r, http.StatusInternalServerError, "An unexpected error occurred", "internal_error")
			return
		}
		etag = tag
	}
	header := w.Header()
	if spec.CacheControl != "" {
		header.Set("Cache-Control", spec.CacheControl)
	}
	for key, values := range file.Header {
		header[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
	}
	noStore(w)
	header.Set("X-Content-Type-Options", "nosniff")
	if policy := spec.SandboxPolicy(); policy != "" {
		header.Set("Content-Security-Policy", policy)
		header.Set("Cross-Origin-Resource-Policy", "same-origin")
	}
	if etag != "" {
		header.Set("ETag", etag)
	}
	if !file.LastModified.IsZero() {
		header.Set("Last-Modified", file.LastModified.UTC().Format(http.TimeFormat))
	}
	if NotModified(r, etag, file.LastModified) {
		header.Del("Content-Type")
		header.Del("Content-Length")
		header.Del("Content-Disposition")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	filename := file.Filename
	if filename == "" {
		filename = spec.Filename
	}
	header.Set("Content-Type", contentType)
	header.Set("Content-Disposition", Disposition(spec.Disposition, filename))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file.Body)
}
