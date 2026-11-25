package ir

import (
	"fmt"
	"mime"
	"strings"
)

// FileDownloadDef is a FileDownload<C> result (D81, D141): the operation
// answers a file, whose bytes are the response body, in place of the JSON
// envelope. Every server sets Content-Type to ContentType, or to the one of
// ContentTypes the implementation names, and Content-Disposition to
// Disposition under the filename the implementation names, or Filename
// when it names none; the OpenAPI document describes the body as binary of
// those media types, and an SDK hands the caller the bytes and the
// filename.
type FileDownloadDef struct {
	// ContentType is the media type of the body, as the Content-Type header
	// carries it: "text/csv", or with parameters, "text/csv; charset=utf-8".
	ContentType string `json:"contentType" yaml:"contentType"`

	// ContentTypes lists the media types the implementation may answer in
	// place of ContentType (D141), each as Content-Type carries it. One it
	// names outside ContentType and this list is a 500.
	ContentTypes []string `json:"contentTypes,omitempty" yaml:"contentTypes,omitempty"`

	// Filename is the filename Content-Disposition offers when the
	// implementation names none ("orders.csv"); empty offers none then.
	Filename string `json:"filename,omitempty" yaml:"filename,omitempty"`

	// Disposition is the Content-Disposition's type (D141): "inline" for a
	// file a page shows, and empty or "attachment" for one a browser saves.
	Disposition string `json:"disposition,omitempty" yaml:"disposition,omitempty"`

	// CacheControl is the answer's Cache-Control, sent as written (D141);
	// empty answers no-store.
	CacheControl string `json:"cacheControl,omitempty" yaml:"cacheControl,omitempty"`

	// Sandbox, when set, has the answer carry Content-Security-Policy:
	// sandbox and Cross-Origin-Resource-Policy: same-origin (D141).
	Sandbox *FileDownloadSandbox `json:"sandbox,omitempty" yaml:"sandbox,omitempty"`
}

// FileDownloadSandbox is a FileDownload's sandbox (D141): `sandbox: true`
// is one with no Allow, and `sandbox: ["allow-scripts"]` lists the tokens
// the policy adds after `sandbox`.
type FileDownloadSandbox struct {
	// Allow holds the sandbox policy's tokens, each "allow-...", in the
	// order written.
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`
}

// The dispositions of a file answer (D141).
const (
	FileDispositionAttachment = "attachment"
	FileDispositionInline     = "inline"
)

// DispositionOrDefault is the disposition a file answer carries:
// Disposition, or attachment when it names none.
func (d *FileDownloadDef) DispositionOrDefault() string {
	if d == nil || d.Disposition == "" {
		return FileDispositionAttachment
	}
	return d.Disposition
}

// AllContentTypes is every media type the result may answer: ContentType
// first, then each of ContentTypes that is not already listed.
func (d *FileDownloadDef) AllContentTypes() []string {
	if d == nil {
		return nil
	}
	all := []string{d.ContentType}
	for _, contentType := range d.ContentTypes {
		listed := false
		for _, have := range all {
			listed = listed || have == contentType
		}
		if !listed {
			all = append(all, contentType)
		}
	}
	return all
}

// SandboxPolicy is the Content-Security-Policy of a sandboxed file answer:
// "sandbox", or "sandbox allow-scripts" with its tokens; "" for a result
// with no sandbox.
func (d *FileDownloadDef) SandboxPolicy() string {
	if d == nil || d.Sandbox == nil {
		return ""
	}
	return strings.Join(append([]string{"sandbox"}, d.Sandbox.Allow...), " ")
}

// FileDownloadToolReason is the @mcp hidden reason of an operation whose
// result is a FileDownload and that declares no @mcp of its own (D81): the
// tool documents list it hidden, as they list the user model's password
// operations, and the loader refuses a visible @mcp on one.
const FileDownloadToolReason = "It answers a file, which is no tool result."

// DefaultUploadMaxBytes is a file part's limit when neither its scalar, its
// FileUpload<C> nor Validate<T, { uploadMaxBytes }> names one: 100 MiB.
const DefaultUploadMaxBytes = 100 << 20

// FileUploadOf returns the upload constraints of field, nil when it is no
// file upload: its own FileUpload<C>, or its scalar's (D81). scalarOf finds
// a scalar by name, in the schema and its dependencies.
func FileUploadOf(field *FieldDef, scalarOf func(name string) (*ScalarDef, bool)) *FileUploadConfig {
	if field == nil {
		return nil
	}
	if field.FileUpload != nil {
		return field.FileUpload
	}
	if scalarOf == nil {
		return nil
	}
	if scalar, ok := scalarOf(field.TypeRef.Name); ok && scalar != nil {
		return scalar.FileUpload
	}
	return nil
}

// ValidateFileUploadConfig checks the members of an upload's constraints as
// FileUpload<C> writes them: a limit that is not negative, media types
// without parameters, and extensions that start with a dot (".csv", or
// ".tar.gz", which a filename's end matches). owner names the field in the
// errors.
func ValidateFileUploadConfig(owner string, config *FileUploadConfig) []error {
	if config == nil {
		return nil
	}
	var errs []error
	if config.MaxSize < 0 {
		errs = append(errs, fmt.Errorf("%s: FileUpload maxBytes must be positive, not %d", owner, config.MaxSize))
	}
	for _, contentType := range config.AllowedTypes {
		mediaType, params, err := mime.ParseMediaType(contentType)
		if err != nil || len(params) > 0 || mediaType != contentType || !strings.Contains(mediaType, "/") {
			errs = append(errs, fmt.Errorf("%s: FileUpload contentTypes holds %q, which is not a lowercase media type without parameters, such as \"text/csv\"", owner, contentType))
		}
	}
	for _, extension := range config.AllowedExtensions {
		if len(extension) < 2 || extension[0] != '.' || strings.HasSuffix(extension, ".") || strings.Contains(extension, "..") ||
			strings.ContainsAny(extension, "/\\ \t\r\n") {
			errs = append(errs, fmt.Errorf("%s: FileUpload extensions holds %q, which is not a filename extension with its dot, such as \".csv\"", owner, extension))
		}
	}
	return errs
}

// ValidateFileDownload checks a FileDownload<C> result's members: media
// types Content-Type can carry, a filename that is a name alone, with no
// directory and no control character, and (D141) a disposition that is
// attachment or inline, a Cache-Control of printable ASCII and sandbox
// tokens that each start "allow-". owner names the operation.
func ValidateFileDownload(owner string, download *FileDownloadDef) []error {
	if download == nil {
		return nil
	}
	var errs []error
	if mediaType, _, err := mime.ParseMediaType(download.ContentType); err != nil || !strings.Contains(mediaType, "/") {
		errs = append(errs, fmt.Errorf("%s: FileDownload contentType %q is not a media type, such as \"text/csv\"", owner, download.ContentType))
	}
	if name := download.Filename; name != "" {
		if strings.ContainsAny(name, "/\\") || name == "." || name == ".." || strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			errs = append(errs, fmt.Errorf("%s: FileDownload filename %q is not a filename alone, with no directory and no control character", owner, name))
		}
	}
	for _, contentType := range download.ContentTypes {
		if mediaType, _, err := mime.ParseMediaType(contentType); err != nil || !strings.Contains(mediaType, "/") {
			errs = append(errs, fmt.Errorf("%s: FileDownload contentTypes holds %q, which is not a media type, such as \"image/png\"", owner, contentType))
		}
	}
	switch download.Disposition {
	case "", FileDispositionAttachment, FileDispositionInline:
	default:
		errs = append(errs, fmt.Errorf("%s: FileDownload disposition %q is not \"attachment\" or \"inline\"", owner, download.Disposition))
	}
	if value := download.CacheControl; value != "" {
		if strings.TrimSpace(value) != value || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r > 0x7e }) >= 0 {
			errs = append(errs, fmt.Errorf("%s: FileDownload cacheControl %q is not a Cache-Control value of printable ASCII, such as \"private, max-age=60\"", owner, value))
		}
	}
	if download.Sandbox != nil {
		seen := map[string]bool{}
		for _, token := range download.Sandbox.Allow {
			switch {
			case !isSandboxToken(token):
				errs = append(errs, fmt.Errorf("%s: FileDownload sandbox holds %q, which is not a sandbox token, such as \"allow-scripts\"", owner, token))
			case seen[token]:
				errs = append(errs, fmt.Errorf("%s: FileDownload sandbox holds %q twice", owner, token))
			}
			seen[token] = true
		}
	}
	return errs
}

// isSandboxToken reports whether token is a token of a sandbox policy as
// Content-Security-Policy writes one: "allow-" and lowercase letters and
// hyphens ("allow-scripts", "allow-same-origin").
func isSandboxToken(token string) bool {
	rest, ok := strings.CutPrefix(token, "allow-")
	if !ok || rest == "" || strings.HasSuffix(rest, "-") {
		return false
	}
	for _, r := range rest {
		if (r < 'a' || r > 'z') && r != '-' {
			return false
		}
	}
	return true
}
