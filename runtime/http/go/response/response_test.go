package response

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/parable-work/superschematic/runtime/http/go/requestctx"
	"github.com/parable-work/superschematic/runtime/schema/go/ptr"
	"github.com/parable-work/superschematic/runtime/schema/go/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func testMeta() *Meta {
	return &Meta{RequestID: "test-request-id"}
}

func parseBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
	return m
}

func assertNotEnveloped(t *testing.T, body []byte) {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(body, &m))
	_, hasData := m["data"]
	meta, hasMeta := m["meta"]
	if hasData && hasMeta {
		metaMap, ok := meta.(map[string]any)
		if ok {
			_, hasReqID := metaMap["requestId"]
			assert.False(t, hasReqID, "error response must not have data + meta.requestId (looks enveloped)")
		}
	}
}

func assertRFC7807Shape(t *testing.T, body []byte) {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(body, &m))
	_, hasDetail := m["detail"]
	assert.True(t, hasDetail, "expected top-level 'detail' key (RFC 7807)")
	_, hasType := m["type"]
	assert.True(t, hasType, "expected top-level 'type' key (RFC 7807)")
	_, hasTitle := m["title"]
	assert.True(t, hasTitle, "expected top-level 'title' key (RFC 7807)")
	_, hasStatus := m["status"]
	assert.True(t, hasStatus, "expected top-level 'status' key (RFC 7807)")
	assertNotEnveloped(t, body)
}

// ---------------------------------------------------------------------------
// A. JSONEnvelope
// ---------------------------------------------------------------------------

func TestJSONEnvelope(t *testing.T) {
	type obj struct {
		ID string `json:"id"`
	}

	tests := []struct {
		name   string
		status int
		data   any
		meta   *Meta
		links  *Links
		assert func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:   "single_object",
			status: http.StatusOK,
			data:   obj{ID: "abc"},
			meta:   testMeta(),
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusOK, rec.Code)
				m := parseBody(t, rec)
				data := m["data"].(map[string]any)
				assert.Equal(t, "abc", data["id"])
				meta := m["meta"].(map[string]any)
				assert.Equal(t, "test-request-id", meta["requestId"])
				assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			},
		},
		{
			name:   "nil_payload",
			status: http.StatusOK,
			data:   nil,
			meta:   testMeta(),
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusOK, rec.Code)
				m := parseBody(t, rec)
				assert.Nil(t, m["data"])
				meta := m["meta"].(map[string]any)
				assert.NotEmpty(t, meta["requestId"])
			},
		},
		{
			name:   "empty_struct",
			status: http.StatusOK,
			data:   struct{}{},
			meta:   testMeta(),
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				m := parseBody(t, rec)
				data, ok := m["data"].(map[string]any)
				require.True(t, ok)
				assert.Empty(t, data)
			},
		},
		{
			name:   "slice_payload",
			status: http.StatusOK,
			data:   []string{"a", "b"},
			meta:   testMeta(),
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				m := parseBody(t, rec)
				data, ok := m["data"].([]any)
				require.True(t, ok)
				assert.Equal(t, []any{"a", "b"}, data)
			},
		},
		{
			name:   "nested_object",
			status: http.StatusOK,
			data:   map[string]any{"inner": map[string]any{"deep": true}},
			meta:   testMeta(),
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				m := parseBody(t, rec)
				data := m["data"].(map[string]any)
				inner := data["inner"].(map[string]any)
				assert.Equal(t, true, inner["deep"])
			},
		},
		{
			name:   "status_201",
			status: http.StatusCreated,
			data:   struct{}{},
			meta:   testMeta(),
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusCreated, rec.Code)
			},
		},
		{
			name:   "content_type",
			status: http.StatusOK,
			data:   struct{}{},
			meta:   testMeta(),
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			},
		},
		{
			name:   "requestId_present",
			status: http.StatusOK,
			data:   struct{}{},
			meta:   testMeta(),
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				m := parseBody(t, rec)
				meta := m["meta"].(map[string]any)
				assert.NotEmpty(t, meta["requestId"])
			},
		},
		{
			name:   "no_extra_keys",
			status: http.StatusOK,
			data:   struct{}{},
			meta:   testMeta(),
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				m := parseBody(t, rec)
				for key := range m {
					assert.Contains(t, []string{"data", "meta"}, key, "unexpected top-level key: %s", key)
				}
			},
		},
		{
			name:   "with_links",
			status: http.StatusOK,
			data:   struct{}{},
			meta:   testMeta(),
			links:  &Links{Next: ptr.To("/next")},
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				m := parseBody(t, rec)
				links := m["links"].(map[string]any)
				assert.Equal(t, "/next", links["next"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			JSONEnvelope(rec, tt.status, tt.data, tt.meta, tt.links)
			tt.assert(t, rec)
		})
	}
}

// ---------------------------------------------------------------------------
// B. Error (RFC 7807 Problem Details)
// ---------------------------------------------------------------------------

func TestError(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
		assert  func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:    "bad_request",
			status:  http.StatusBadRequest,
			message: "bad request",
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusBadRequest, rec.Code)
				m := parseBody(t, rec)
				assert.Equal(t, "bad request", m["detail"])
				assert.Equal(t, "about:blank", m["type"])
				assert.Equal(t, "Bad Request", m["title"])
				assert.Equal(t, float64(400), m["status"])
				assertNotEnveloped(t, rec.Body.Bytes())
			},
		},
		{
			name:    "unauthorized",
			status:  http.StatusUnauthorized,
			message: "unauthorized",
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusUnauthorized, rec.Code)
				assertRFC7807Shape(t, rec.Body.Bytes())
				assertNotEnveloped(t, rec.Body.Bytes())
			},
		},
		{
			name:    "forbidden",
			status:  http.StatusForbidden,
			message: "forbidden",
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusForbidden, rec.Code)
			},
		},
		{
			name:    "not_found",
			status:  http.StatusNotFound,
			message: "not found",
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusNotFound, rec.Code)
			},
		},
		{
			name:    "conflict",
			status:  http.StatusConflict,
			message: "conflict",
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusConflict, rec.Code)
			},
		},
		{
			name:    "internal",
			status:  http.StatusInternalServerError,
			message: "internal error",
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusInternalServerError, rec.Code)
			},
		},
		{
			name:    "empty_message",
			status:  http.StatusBadRequest,
			message: "",
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				m := parseBody(t, rec)
				val, exists := m["detail"]
				assert.True(t, exists, "detail key must exist")
				assert.Equal(t, "", val)
			},
		},
		{
			name:    "special_chars",
			status:  http.StatusBadRequest,
			message: `field "name" <invalid>`,
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				m := parseBody(t, rec)
				assert.Equal(t, `field "name" <invalid>`, m["detail"])
			},
		},
		{
			name:    "content_type",
			status:  http.StatusBadRequest,
			message: "x",
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
			},
		},
		{
			name:    "rfc7807_keys",
			status:  http.StatusBadRequest,
			message: "x",
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				m := parseBody(t, rec)
				assert.Contains(t, m, "type")
				assert.Contains(t, m, "title")
				assert.Contains(t, m, "status")
				assert.Contains(t, m, "detail")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Error(rec, tt.status, tt.message)
			tt.assert(t, rec)
		})
	}
}

// ---------------------------------------------------------------------------
// C. ValidationErrors (RFC 7807)
// ---------------------------------------------------------------------------

func TestValidationErrors(t *testing.T) {
	singleFieldErrors := validate.ValidationErrors{
		"name": []validate.ValidationError{{Validator: "required", Message: "required"}},
	}

	multiFieldErrors := validate.ValidationErrors{
		"name":  []validate.ValidationError{{Validator: "required", Message: "required"}},
		"email": []validate.ValidationError{{Validator: "format", Message: "invalid email"}},
		"age":   []validate.ValidationError{{Validator: "min", Message: "must be >= 0"}},
	}

	tests := []struct {
		name   string
		errors validate.ValidationErrors
		assert func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:   "single_field",
			errors: singleFieldErrors,
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusBadRequest, rec.Code)
				m := parseBody(t, rec)
				assert.Equal(t, "Validation failed", m["detail"])
				assert.Equal(t, "about:blank", m["type"])
				assert.Equal(t, "Validation Failed", m["title"])
				errs := m["errors"].(map[string]any)
				assert.Len(t, errs, 1)
				assertNotEnveloped(t, rec.Body.Bytes())
			},
		},
		{
			name:   "multiple_fields",
			errors: multiFieldErrors,
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusBadRequest, rec.Code)
				errs := parseBody(t, rec)["errors"].(map[string]any)
				assert.Len(t, errs, 3)
			},
		},
		{
			name:   "detail_message_fixed",
			errors: singleFieldErrors,
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				m := parseBody(t, rec)
				assert.Equal(t, "Validation failed", m["detail"])
			},
		},
		{
			name:   "no_data_key",
			errors: singleFieldErrors,
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				m := parseBody(t, rec)
				_, hasData := m["data"]
				assert.False(t, hasData, "validation error response must not contain 'data' key")
				_, hasMeta := m["meta"]
				assert.False(t, hasMeta, "validation error response must not contain 'meta' key")
				assertNotEnveloped(t, rec.Body.Bytes())
			},
		},
		{
			name:   "content_type",
			errors: singleFieldErrors,
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
			},
		},
		{
			name:   "error_structure",
			errors: singleFieldErrors,
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				m := parseBody(t, rec)
				errs := m["errors"].(map[string]any)
				nameErrs, ok := errs["name"]
				require.True(t, ok, "expected 'name' key in errors map")
				errList, ok := nameErrs.([]any)
				require.True(t, ok, "expected array of errors for field")
				require.Len(t, errList, 1)
				first := errList[0].(map[string]any)
				assert.Equal(t, "required", first["validator"])
				assert.Equal(t, "required", first["message"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ValidationErrors(rec, tt.errors)
			tt.assert(t, rec)
		})
	}
}

// ---------------------------------------------------------------------------
// D. CollectionEnvelope
// ---------------------------------------------------------------------------

func TestCollectionEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		data   any
		meta   *Meta
		assert func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name: "basic",
			data: []string{"a", "b"},
			meta: testMeta(),
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				m := parseBody(t, rec)
				data, ok := m["data"].([]any)
				require.True(t, ok, "data must be an array")
				assert.Len(t, data, 2)
				_, hasMeta := m["meta"]
				assert.True(t, hasMeta, "envelope must include meta")
			},
		},
		{
			name: "nil_slice",
			data: nil,
			meta: testMeta(),
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				m := parseBody(t, rec)
				data, ok := m["data"].([]any)
				require.True(t, ok, "nil input must serialize as empty array, not null")
				assert.Empty(t, data)
			},
		},
		{
			name: "meta_has_requestId",
			data: []string{"x"},
			meta: testMeta(),
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				m := parseBody(t, rec)
				meta := m["meta"].(map[string]any)
				assert.Equal(t, "test-request-id", meta["requestId"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			CollectionEnvelope(rec, http.StatusOK, tt.data, tt.meta, nil)
			tt.assert(t, rec)
		})
	}
}

func newObservedLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zap.DebugLevel)
	return zap.New(core), logs
}

func requestWithLogger(method, path string, logger *zap.Logger) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	ctx := requestctx.ContextWithLogger(r.Context(), logger)
	ctx = context.WithValue(ctx, chimiddleware.RequestIDKey, "test-req-id")
	return r.WithContext(ctx)
}

func TestLoggedError(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		message   string
		wantLevel zapcore.Level
	}{
		{
			name:      "4xx_logs_warn",
			status:    http.StatusBadRequest,
			message:   "invalid email format",
			wantLevel: zap.WarnLevel,
		},
		{
			name:      "401_logs_warn",
			status:    http.StatusUnauthorized,
			message:   "Authentication required",
			wantLevel: zap.WarnLevel,
		},
		{
			name:      "403_logs_warn",
			status:    http.StatusForbidden,
			message:   "Insufficient permissions",
			wantLevel: zap.WarnLevel,
		},
		{
			name:      "404_logs_warn",
			status:    http.StatusNotFound,
			message:   "widget not found",
			wantLevel: zap.WarnLevel,
		},
		{
			name:      "5xx_logs_error",
			status:    http.StatusInternalServerError,
			message:   "An unexpected error occurred",
			wantLevel: zap.ErrorLevel,
		},
		{
			name:      "502_logs_error",
			status:    http.StatusBadGateway,
			message:   "upstream unavailable",
			wantLevel: zap.ErrorLevel,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logger, logs := newObservedLogger()
			r := requestWithLogger("GET", "/api/widgets", logger)
			w := httptest.NewRecorder()

			LoggedError(w, r, tc.status, tc.message)

			assert.Equal(t, tc.status, w.Code)
			assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))

			var body map[string]any
			err := json.NewDecoder(w.Body).Decode(&body)
			require.NoError(t, err)
			assert.Equal(t, tc.message, body["detail"])
			assert.Equal(t, "test-req-id", body["requestId"])

			require.Equal(t, 1, logs.Len(), "expected exactly one log entry")
			entry := logs.All()[0]
			assert.Equal(t, tc.wantLevel, entry.Level)
			assert.Equal(t, "http error response", entry.Message)

			fieldMap := entry.ContextMap()
			assert.Equal(t, int64(tc.status), fieldMap["status"])
			assert.Equal(t, tc.message, fieldMap["error"])
		})
	}
}

func TestLoggedError_WithErrorCode(t *testing.T) {
	logger, logs := newObservedLogger()
	r := requestWithLogger("GET", "/api/widgets", logger)
	w := httptest.NewRecorder()

	LoggedError(w, r, http.StatusNotFound, "widget not found", "WA-RS-001")

	m := parseBody(t, w)
	assert.Equal(t, "WA-RS-001", m["code"])
	assert.Equal(t, "widget not found", m["detail"])
	assert.Equal(t, "test-req-id", m["requestId"])

	entry := logs.All()[0]
	fieldMap := entry.ContextMap()
	assert.Equal(t, "WA-RS-001", fieldMap["error_code"])
}

func TestLoggedError_NoRequestId(t *testing.T) {
	logger, _ := newObservedLogger()
	r := httptest.NewRequest("GET", "/api/widgets", nil)
	ctx := requestctx.ContextWithLogger(r.Context(), logger)
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()

	LoggedError(w, r, http.StatusBadRequest, "bad request")

	m := parseBody(t, w)
	_, hasReqID := m["requestId"]
	assert.False(t, hasReqID, "requestId should be absent when not in context")
}

func TestLoggedValidationErrors(t *testing.T) {
	logger, logs := newObservedLogger()

	ve := validate.NewValidationErrors()
	ve.AddFieldError("email", "required", "Email is required")
	ve.AddFieldError("name", "max_length", "Name too long")

	r := requestWithLogger("POST", "/api/users", logger)
	w := httptest.NewRecorder()

	LoggedValidationErrors(w, r, ve)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))

	var body map[string]any
	err := json.NewDecoder(w.Body).Decode(&body)
	require.NoError(t, err)
	assert.Equal(t, "Validation failed", body["detail"])
	assert.Equal(t, "test-req-id", body["requestId"])

	require.Equal(t, 1, logs.Len(), "expected exactly one log entry")
	entry := logs.All()[0]
	assert.Equal(t, zap.WarnLevel, entry.Level)
	assert.Equal(t, "http validation error response", entry.Message)

	fieldMap := entry.ContextMap()
	assert.Equal(t, int64(http.StatusBadRequest), fieldMap["status"])
	assert.Equal(t, int64(2), fieldMap["validation_error_count"])
}

func TestLoggedValidationErrors_WithErrorCode(t *testing.T) {
	logger, logs := newObservedLogger()

	ve := validate.NewValidationErrors()
	ve.AddFieldError("name", "required", "Name is required")

	r := requestWithLogger("POST", "/api/widgets", logger)
	w := httptest.NewRecorder()

	LoggedValidationErrors(w, r, ve, "WA-VL-001")

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))

	m := parseBody(t, w)
	assert.Equal(t, "Validation failed", m["detail"])
	assert.Equal(t, "WA-VL-001", m["code"])
	assert.Equal(t, "test-req-id", m["requestId"])

	require.Equal(t, 1, logs.Len())
	entry := logs.All()[0]
	fieldMap := entry.ContextMap()
	assert.Equal(t, "WA-VL-001", fieldMap["error_code"])
}

// ---------------------------------------------------------------------------
// E. Error with ErrorCode (EDR-0011 + RFC 9457)
// ---------------------------------------------------------------------------

func TestError_WithCode(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, http.StatusNotFound, "Not found", "WA-RS-001")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

	m := parseBody(t, rec)
	assert.Equal(t, "Not found", m["detail"])
	assert.Equal(t, "WA-RS-001", m["code"])
	assert.Equal(t, "about:blank", m["type"])
	assert.Equal(t, float64(404), m["status"])
	_, hasReqID := m["requestId"]
	assert.False(t, hasReqID, "Error() must not include requestId (no request context)")
}

func TestError_WithoutCode(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, http.StatusConflict, "Conflict")

	assert.Equal(t, http.StatusConflict, rec.Code)

	m := parseBody(t, rec)
	assert.Equal(t, "Conflict", m["detail"])
	// code should be omitted (omitempty on struct tag)
	_, hasCode := m["code"]
	assert.False(t, hasCode, "code field should be absent when not provided")
}

func TestError_WithEmptyCode(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, http.StatusBadRequest, "Bad request", "")

	m := parseBody(t, rec)
	assert.Equal(t, "Bad request", m["detail"])
	_, hasCode := m["code"]
	assert.False(t, hasCode, "code field should be absent when empty string")
}
