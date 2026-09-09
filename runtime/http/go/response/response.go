package response

import (
	"encoding/json"
	"net/http"
	"reflect"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/parable-work/superschematic/runtime/http/go/filterparse"
	"github.com/parable-work/superschematic/runtime/http/go/requestctx"
	"github.com/parable-work/superschematic/runtime/schema/go/validate"
	"go.uber.org/zap"
)

// problemDetail represents an RFC 9457 (Problem Details for HTTP APIs) response.
type problemDetail struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	Code      string `json:"code,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

// validationProblemDetail extends problemDetail with field-level validation errors.
type validationProblemDetail struct {
	problemDetail
	Errors validate.ValidationErrors `json:"errors"`
}

// filterProblemDetail extends problemDetail with filter-level parse errors.
type filterProblemDetail struct {
	problemDetail
	Errors []filterparse.ParseError `json:"errors"`
}

// writeProblemJSON writes an RFC 9457 Problem Details response.
func writeProblemJSON(w http.ResponseWriter, v any, status int) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// JSON sends a JSON response.
// It validates encoding before writing headers so failures still return a clean 500.
func JSON(w http.ResponseWriter, status int, data any) {
	if data == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		return
	}

	payload, err := json.Marshal(data)
	if err != nil {
		Error(w, http.StatusInternalServerError, "Failed to encode response")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
	_, _ = w.Write([]byte("\n"))
}

// Error sends an RFC 9457 Problem Details error response.
func Error(w http.ResponseWriter, status int, message string, errorCode ...string) {
	d := problemDetail{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: message,
	}
	if len(errorCode) > 0 {
		d.Code = errorCode[0]
	}
	writeProblemJSON(w, d, status)
}

// LoggedError extracts the request-scoped logger from context, emits a
// structured log line (Warn for 4xx, Error for 5xx), then writes an RFC 9457
// Problem Details response including the requestId from context.
func LoggedError(w http.ResponseWriter, r *http.Request, status int, message string, errorCode ...string) {
	logger := requestctx.LoggerFromContext(r.Context())
	fields := []zap.Field{
		zap.Int("status", status),
		zap.String("error", message),
	}
	if len(errorCode) > 0 && errorCode[0] != "" {
		fields = append(fields, zap.String("error_code", errorCode[0]))
	}
	if status >= 500 {
		logger.Error("http error response", fields...)
	} else {
		logger.Warn("http error response", fields...)
	}

	d := problemDetail{
		Type:      "about:blank",
		Title:     http.StatusText(status),
		Status:    status,
		Detail:    message,
		RequestID: chimiddleware.GetReqID(r.Context()),
	}
	if len(errorCode) > 0 {
		d.Code = errorCode[0]
	}
	writeProblemJSON(w, d, status)
}

// ValidationErrors sends an RFC 9457 Problem Details validation error response.
func ValidationErrors(w http.ResponseWriter, errors validate.ValidationErrors, errorCode ...string) {
	d := validationProblemDetail{
		problemDetail: problemDetail{
			Type:   "about:blank",
			Title:  "Validation Failed",
			Status: http.StatusBadRequest,
			Detail: "Validation failed",
		},
		Errors: errors,
	}
	if len(errorCode) > 0 {
		d.Code = errorCode[0]
	}
	writeProblemJSON(w, d, http.StatusBadRequest)
}

// LoggedValidationErrors extracts the request-scoped logger from context,
// emits a Warn-level structured log, then writes an RFC 9457 validation error
// response including the requestId from context.
func LoggedValidationErrors(w http.ResponseWriter, r *http.Request, errors validate.ValidationErrors, errorCode ...string) {
	logger := requestctx.LoggerFromContext(r.Context())
	logFields := []zap.Field{
		zap.Int("status", http.StatusBadRequest),
		zap.Int("validation_error_count", len(errors)),
	}
	if len(errorCode) > 0 && errorCode[0] != "" {
		logFields = append(logFields, zap.String("error_code", errorCode[0]))
	}
	logger.Warn("http validation error response", logFields...)

	d := validationProblemDetail{
		problemDetail: problemDetail{
			Type:      "about:blank",
			Title:     "Validation Failed",
			Status:    http.StatusBadRequest,
			Detail:    "Validation failed",
			RequestID: chimiddleware.GetReqID(r.Context()),
		},
		Errors: errors,
	}
	if len(errorCode) > 0 {
		d.Code = errorCode[0]
	}
	writeProblemJSON(w, d, http.StatusBadRequest)
}

// Created sends a 201 Created response.
func Created(w http.ResponseWriter, data any) {
	JSON(w, http.StatusCreated, data)
}

// NoContent sends a 204 No Content response.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// Envelope wraps a response payload with optional metadata and pagination links.
type Envelope struct {
	Data  any    `json:"data"`
	Meta  *Meta  `json:"meta,omitempty"`
	Links *Links `json:"links,omitempty"`
}

// Meta holds response metadata such as request tracing and pagination counts.
type Meta struct {
	RequestID  string `json:"requestId"`
	TotalCount *int   `json:"totalCount,omitempty"`
}

// Links holds HATEOAS-style pagination links.
type Links struct {
	First *string `json:"first,omitempty"`
	Prev  *string `json:"prev,omitempty"`
	Next  *string `json:"next,omitempty"`
	Last  *string `json:"last,omitempty"`
}

// JSONEnvelope sends a JSON response wrapped in an envelope with optional meta and links.
func JSONEnvelope(w http.ResponseWriter, status int, data any, meta *Meta, links *Links) {
	env := Envelope{
		Data:  data,
		Meta:  meta,
		Links: links,
	}
	JSON(w, status, env)
}

// CollectionEnvelope sends a JSON response for collection endpoints.
// It ensures the data field is always serialized as an array ([] not null),
// handling both untyped nil and typed nil slices (e.g., []Widget(nil)).
func CollectionEnvelope(w http.ResponseWriter, status int, data any, meta *Meta, links *Links) {
	if isNilOrNilSlice(data) {
		data = []any{}
	}
	JSONEnvelope(w, status, data, meta, links)
}

func isNilOrNilSlice(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Slice && rv.IsNil()
}

// FilterErrors sends an RFC 9457 Problem Details filter-error response.
func FilterErrors(w http.ResponseWriter, errors []filterparse.ParseError, errorCode ...string) {
	// Marshal nil as an empty array so clients can unconditionally
	// iterate `body.errors` without null-checking.
	if errors == nil {
		errors = []filterparse.ParseError{}
	}
	d := filterProblemDetail{
		problemDetail: problemDetail{
			Type:   "about:blank",
			Title:  "Invalid Filter Parameters",
			Status: http.StatusBadRequest,
			Detail: "Invalid filter parameters",
		},
		Errors: errors,
	}
	if len(errorCode) > 0 {
		d.Code = errorCode[0]
	}
	writeProblemJSON(w, d, http.StatusBadRequest)
}

// LoggedFilterErrors extracts the request-scoped logger from context, emits a
// Warn-level structured log, then writes an RFC 9457 filter-error response
// including the requestId from context.
func LoggedFilterErrors(w http.ResponseWriter, r *http.Request, errors []filterparse.ParseError, errorCode ...string) {
	// Marshal nil as an empty array so clients can unconditionally
	// iterate `body.errors` without null-checking.
	if errors == nil {
		errors = []filterparse.ParseError{}
	}
	logger := requestctx.LoggerFromContext(r.Context())
	logFields := []zap.Field{
		zap.Int("status", http.StatusBadRequest),
		zap.Int("filter_error_count", len(errors)),
	}
	if len(errorCode) > 0 && errorCode[0] != "" {
		logFields = append(logFields, zap.String("error_code", errorCode[0]))
	}
	logger.Warn("http filter error response", logFields...)

	d := filterProblemDetail{
		problemDetail: problemDetail{
			Type:      "about:blank",
			Title:     "Invalid Filter Parameters",
			Status:    http.StatusBadRequest,
			Detail:    "Invalid filter parameters",
			RequestID: chimiddleware.GetReqID(r.Context()),
		},
		Errors: errors,
	}
	if len(errorCode) > 0 {
		d.Code = errorCode[0]
	}
	writeProblemJSON(w, d, http.StatusBadRequest)
}
