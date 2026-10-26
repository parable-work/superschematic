package response

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// FileDownload is what a Go implementation returns for an operation whose
// result is a FileDownload<C> (D81): the file the route answers in place
// of the JSON envelope. The route sends Body as it reads it, so a reader
// that produces the file as it goes streams it, and closes Body when it is
// an io.Closer.
type FileDownload struct {
	// Body is the file's bytes.
	Body io.Reader
	// Filename is the name Content-Disposition offers; empty offers the
	// schema's, or none when the schema names none.
	Filename string
	// Header holds headers beside Content-Type and Content-Disposition,
	// such as Last-Modified; Cache-Control is no-store unless it sets one.
	Header http.Header
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
	filename := DownloadFilename(name)
	if filename == "" {
		return "attachment"
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
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, fallback.String(), encoded.String())
}

// File answers a FileDownload result: the file's headers, contentType and
// the Content-Disposition of its filename, or defaultFilename when it
// names none, then its body. A nil file is the implementation's error.
func File(w http.ResponseWriter, r *http.Request, contentType, defaultFilename string, file *FileDownload) {
	if file == nil || file.Body == nil {
		LoggedError(w, r, http.StatusInternalServerError, "An unexpected error occurred", "internal_error")
		return
	}
	if closer, ok := file.Body.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}
	header := w.Header()
	for key, values := range file.Header {
		header[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
	}
	noStore(w)
	filename := file.Filename
	if filename == "" {
		filename = defaultFilename
	}
	header.Set("Content-Type", contentType)
	header.Set("Content-Disposition", ContentDisposition(filename))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file.Body)
}
