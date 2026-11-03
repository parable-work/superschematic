package ir

import (
	"fmt"
	"mime"
	"strings"
)

// FileDownloadDef is a FileDownload<C> result (D81): the operation answers
// a file, whose bytes are the response body, in place of the JSON envelope.
// Every server sets Content-Type to ContentType and Content-Disposition to
// an attachment under the filename the implementation names, or Filename
// when it names none; the OpenAPI document describes the body as binary of
// that media type, and an SDK hands the caller the bytes and the filename.
type FileDownloadDef struct {
	// ContentType is the media type of the body, as the Content-Type header
	// carries it: "text/csv", or with parameters, "text/csv; charset=utf-8".
	ContentType string `json:"contentType" yaml:"contentType"`

	// Filename is the filename Content-Disposition offers when the
	// implementation names none ("orders.csv"); empty offers none then.
	Filename string `json:"filename,omitempty" yaml:"filename,omitempty"`
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

// ValidateFileDownload checks a FileDownload<C> result's members: a media
// type Content-Type can carry, and a filename that is a name alone, with no
// directory and no control character. owner names the operation.
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
	return errs
}
