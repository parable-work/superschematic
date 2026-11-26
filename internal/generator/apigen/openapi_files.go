package apigen

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// multipartEncoding is the OpenAPI encoding of a multipart body's file
// parts that name the media types they admit (D81), keyed by part name;
// nil when none does.
func multipartEncoding(fields []FileUploadField) map[string]interface{} {
	encoding := map[string]interface{}{}
	for _, field := range fields {
		if len(field.AllowedTypes) > 0 {
			encoding[field.Name] = map[string]interface{}{"contentType": strings.Join(field.AllowedTypes, ", ")}
		}
	}
	if len(encoding) == 0 {
		return nil
	}
	return encoding
}

// fileDownloadResponse is the 200 of a FileDownload<C> result (D81, D141):
// the file's bytes as each media type C names, with the Content-Disposition
// header that offers its filename, the validators the operation may send
// and the headers every file answer carries, and no envelope.
func fileDownloadResponse(download *ir.FileDownloadDef) map[string]interface{} {
	disposition := download.DispositionOrDefault() + ", with the file's name in filename and filename* (RFC 6266)"
	if download.Filename != "" {
		disposition = fmt.Sprintf("%s; %q unless the operation names another", disposition, download.Filename)
	}
	stringHeader := func(description string) map[string]interface{} {
		return map[string]interface{}{
			"description": description,
			"schema":      map[string]interface{}{"type": "string"},
		}
	}
	cacheControl := "no-store, unless the operation sets another"
	if download.CacheControl != "" {
		cacheControl = fmt.Sprintf("%q, unless the operation sets another", download.CacheControl)
	}
	headers := map[string]interface{}{
		"Content-Disposition":    stringHeader(disposition),
		"Cache-Control":          stringHeader(cacheControl),
		"X-Content-Type-Options": stringHeader("nosniff"),
		"ETag":                   stringHeader("The file's entity tag, when the operation names one"),
		"Last-Modified":          stringHeader("When the file last changed, when the operation says"),
	}
	if policy := download.SandboxPolicy(); policy != "" {
		headers["Content-Security-Policy"] = stringHeader(policy)
		headers["Cross-Origin-Resource-Policy"] = stringHeader("same-origin")
	}
	content := map[string]interface{}{}
	for _, contentType := range download.AllContentTypes() {
		content[contentType] = map[string]interface{}{
			"schema": map[string]interface{}{"type": "string", "format": "binary"},
		}
	}
	return map[string]interface{}{
		"description": "The file",
		"headers":     headers,
		"content":     content,
	}
}

// fileNotModifiedResponse is the 304 of a FileDownload<C> result read by
// GET (D141): the answer to a request whose If-None-Match names the file's
// entity tag, or whose If-Modified-Since is not before the file's time,
// with the validators and no body.
func fileNotModifiedResponse() map[string]interface{} {
	return map[string]interface{}{
		"description": "The caller's copy is current: If-None-Match names the file's entity tag, or, with no If-None-Match, If-Modified-Since is not before its Last-Modified. No body.",
		"headers": map[string]interface{}{
			"ETag": map[string]interface{}{
				"description": "The file's entity tag, when the operation names one",
				"schema":      map[string]interface{}{"type": "string"},
			},
			"Last-Modified": map[string]interface{}{
				"description": "When the file last changed, when the operation says",
				"schema":      map[string]interface{}{"type": "string"},
			},
		},
	}
}
