package tswriter

import (
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// fileUploadExpr renders FileUpload<C> (D81): bare when C is empty, else
// with each constraint the IR holds.
func (e *emitter) fileUploadExpr(config *ir.FileUploadConfig) string {
	var members []string
	if config.MaxSize > 0 {
		members = append(members, "maxBytes: "+strconv.Itoa(config.MaxSize))
	}
	if len(config.AllowedTypes) > 0 {
		members = append(members, "contentTypes: "+stringTupleExpr(config.AllowedTypes))
	}
	if len(config.AllowedExtensions) > 0 {
		members = append(members, "extensions: "+stringTupleExpr(config.AllowedExtensions))
	}
	if config.Category != "" {
		e.failf("a FileUpload with a category has no TypeScript authoring form")
	}
	if len(members) == 0 {
		return e.use("FileUpload")
	}
	return e.use("FileUpload") + "<{ " + strings.Join(members, "; ") + " }>"
}

// fileDownloadExpr renders FileDownload<C> (D81).
func (e *emitter) fileDownloadExpr(download *ir.FileDownloadDef) string {
	members := []string{"contentType: " + strconv.Quote(download.ContentType)}
	if download.Filename != "" {
		members = append(members, "filename: "+strconv.Quote(download.Filename))
	}
	return e.use("FileDownload") + "<{ " + strings.Join(members, "; ") + " }>"
}

// stringTupleExpr renders a tuple of string literals: ["text/csv", ".csv"].
func stringTupleExpr(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = strconv.Quote(value)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
