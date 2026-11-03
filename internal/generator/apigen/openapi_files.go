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

// fileDownloadResponse is the 200 of a FileDownload<C> result (D81): the
// file's bytes as the media type C names, with the Content-Disposition
// header that offers its filename, and no envelope.
func fileDownloadResponse(download *ir.FileDownloadDef) map[string]interface{} {
	disposition := "attachment, with the file's name in filename and filename* (RFC 6266)"
	if download.Filename != "" {
		disposition = fmt.Sprintf("%s; %q unless the operation names another", disposition, download.Filename)
	}
	return map[string]interface{}{
		"description": "The file",
		"headers": map[string]interface{}{
			"Content-Disposition": map[string]interface{}{
				"description": disposition,
				"schema":      map[string]interface{}{"type": "string"},
			},
		},
		"content": map[string]interface{}{
			download.ContentType: map[string]interface{}{
				"schema": map[string]interface{}{"type": "string", "format": "binary"},
			},
		},
	}
}
