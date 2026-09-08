package apperror_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/parable-work/superschematic/runtime/http/go/apperror"
	"github.com/parable-work/superschematic/runtime/schema/go/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestAppError_HTTPStatus(t *testing.T) {
	tests := []struct {
		code       string
		wantStatus int
	}{
		{apperror.ErrCodeNotFound, 404},
		{apperror.ErrCodeUnauthorized, 401},
		{apperror.ErrCodeForbidden, 403},
		{apperror.ErrCodeConflict, 409},
		{apperror.ErrCodeUnprocessable, 422},
		{apperror.ErrCodeBadRequest, 400},
		{apperror.ErrCodeInternal, 500},
		{"UNKNOWN_CODE", 500},
	}

	for _, tc := range tests {
		t.Run(tc.code, func(t *testing.T) {
			err := &apperror.AppError{Code: tc.code, Message: "test"}
			assert.Equal(t, tc.wantStatus, err.HTTPStatus())
		})
	}
}

func TestAppError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("db connection failed")
	err := apperror.NewAppError(apperror.ErrCodeInternal, "An unexpected error occurred", inner)

	assert.Equal(t, "An unexpected error occurred", err.Error())
	assert.ErrorIs(t, err, inner)

	var appErr *apperror.AppError
	require.True(t, errors.As(err, &appErr))
	assert.Equal(t, apperror.ErrCodeInternal, appErr.Code)
}

func TestAppError_WrappedUnwrapping(t *testing.T) {
	inner := apperror.NotFoundError("widget", errors.New("not in db"))
	wrapped := fmt.Errorf("handler context: %w", inner)

	var appErr *apperror.AppError
	require.True(t, errors.As(wrapped, &appErr))
	assert.Equal(t, apperror.ErrCodeNotFound, appErr.Code)
	assert.Equal(t, "widget not found", appErr.Message)
}

func TestAppError_Constructors(t *testing.T) {
	tests := []struct {
		name        string
		err         *apperror.AppError
		wantCode    string
		wantMessage string
	}{
		{
			name:        "not_found",
			err:         apperror.NotFoundError("widget", nil),
			wantCode:    apperror.ErrCodeNotFound,
			wantMessage: "widget not found",
		},
		{
			name:        "unauthorized",
			err:         apperror.UnauthorizedError(nil),
			wantCode:    apperror.ErrCodeUnauthorized,
			wantMessage: "Authentication required",
		},
		{
			name:        "forbidden",
			err:         apperror.ForbiddenError(nil),
			wantCode:    apperror.ErrCodeForbidden,
			wantMessage: "Access denied",
		},
		{
			name:        "conflict",
			err:         apperror.ConflictError("resource already exists", nil),
			wantCode:    apperror.ErrCodeConflict,
			wantMessage: "resource already exists",
		},
		{
			name:        "bad_request",
			err:         apperror.BadRequestError("invalid input", nil),
			wantCode:    apperror.ErrCodeBadRequest,
			wantMessage: "invalid input",
		},
		{
			name:        "unprocessable_entity",
			err:         apperror.UnprocessableEntityError("invalid state transition", nil),
			wantCode:    apperror.ErrCodeUnprocessable,
			wantMessage: "invalid state transition",
		},
		{
			name:        "internal",
			err:         apperror.InternalError(nil),
			wantCode:    apperror.ErrCodeInternal,
			wantMessage: "An unexpected error occurred",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantCode, tc.err.Code)
			assert.Equal(t, tc.wantMessage, tc.err.Message)
		})
	}
}

func TestRespond(t *testing.T) {
	logger := zap.NewNop()

	tests := []struct {
		name            string
		err             error
		wantStatus      int
		wantContentType string
		wantBodyKey     string
		wantBodyValue   string
	}{
		{
			name:            "not_found",
			err:             apperror.NotFoundError("widget 123", errors.New("not in db")),
			wantStatus:      404,
			wantContentType: "application/problem+json",
			wantBodyKey:     "detail",
			wantBodyValue:   "widget 123 not found",
		},
		{
			name:            "unauthorized",
			err:             apperror.UnauthorizedError(errors.New("no token")),
			wantStatus:      401,
			wantContentType: "application/problem+json",
			wantBodyKey:     "detail",
			wantBodyValue:   "Authentication required",
		},
		{
			name:            "forbidden",
			err:             apperror.ForbiddenError(errors.New("role mismatch")),
			wantStatus:      403,
			wantContentType: "application/problem+json",
			wantBodyKey:     "detail",
			wantBodyValue:   "Access denied",
		},
		{
			name:            "conflict",
			err:             apperror.ConflictError("resource already exists", errors.New("dup key")),
			wantStatus:      409,
			wantContentType: "application/problem+json",
			wantBodyKey:     "detail",
			wantBodyValue:   "resource already exists",
		},
		{
			name:            "bad_request",
			err:             apperror.BadRequestError("invalid email format", errors.New("parse fail")),
			wantStatus:      400,
			wantContentType: "application/problem+json",
			wantBodyKey:     "detail",
			wantBodyValue:   "invalid email format",
		},
		{
			name:            "unprocessable_entity",
			err:             apperror.UnprocessableEntityError("connector configuration is invalid", errors.New("semantic validation failed")),
			wantStatus:      422,
			wantContentType: "application/problem+json",
			wantBodyKey:     "detail",
			wantBodyValue:   "connector configuration is invalid",
		},
		{
			name:            "internal_error",
			err:             apperror.InternalError(errors.New("database down")),
			wantStatus:      500,
			wantContentType: "application/problem+json",
			wantBodyKey:     "detail",
			wantBodyValue:   "An unexpected error occurred",
		},
		{
			name:            "unknown_error_does_not_leak_internals",
			err:             errors.New("secret database password in error message"),
			wantStatus:      500,
			wantContentType: "application/problem+json",
			wantBodyKey:     "detail",
			wantBodyValue:   "An unexpected error occurred",
		},
		{
			name:            "wrapped_app_error",
			err:             fmt.Errorf("handler context: %w", apperror.NotFoundError("connector", errors.New("not in db"))),
			wantStatus:      404,
			wantContentType: "application/problem+json",
			wantBodyKey:     "detail",
			wantBodyValue:   "connector not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			apperror.Respond(w, logger, tc.err)

			assert.Equal(t, tc.wantStatus, w.Code)
			assert.Equal(t, tc.wantContentType, w.Header().Get("Content-Type"))

			var body map[string]any
			err := json.NewDecoder(w.Body).Decode(&body)
			require.NoError(t, err)
			assert.Equal(t, tc.wantBodyValue, body[tc.wantBodyKey])
		})
	}
}

func TestRespond_ValidationError(t *testing.T) {
	logger := zap.NewNop()

	tests := []struct {
		name          string
		setup         func() *apperror.ValidationAppError
		wantFieldKeys []string
	}{
		{
			name: "single_field",
			setup: func() *apperror.ValidationAppError {
				return apperror.UniqueConstraintError("email", "Email already taken", errors.New("dup"))
			},
			wantFieldKeys: []string{"email"},
		},
		{
			name: "multiple_fields",
			setup: func() *apperror.ValidationAppError {
				ve := validate.NewValidationErrors()
				ve.AddFieldError("email", "required", "Email is required")
				ve.AddFieldError("name", "max_length", "Name too long")
				ve.AddFieldError("age", "min", "Must be at least 18")
				return apperror.NewValidationError(ve, errors.New("validation failed"))
			},
			wantFieldKeys: []string{"email", "name", "age"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			apperror.Respond(w, logger, tc.setup())

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))

			var body map[string]any
			err := json.NewDecoder(w.Body).Decode(&body)
			require.NoError(t, err)

			assert.Equal(t, "Validation failed", body["detail"])

			errorsMap, ok := body["errors"].(map[string]any)
			require.True(t, ok, "errors field must be a map")
			for _, key := range tc.wantFieldKeys {
				assert.Contains(t, errorsMap, key)
			}
			assert.Len(t, errorsMap, len(tc.wantFieldKeys))
		})
	}
}

func TestRespond_NilLogger(t *testing.T) {
	w := httptest.NewRecorder()
	apperror.Respond(w, nil, errors.New("something went wrong"))

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var body map[string]any
	err := json.NewDecoder(w.Body).Decode(&body)
	require.NoError(t, err)
	assert.Equal(t, "An unexpected error occurred", body["detail"])
}

func TestValidationAppError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("parse failure")
	ve := validate.NewValidationErrors()
	ve.AddFieldError("email", "format", "bad format")
	validationErr := apperror.NewValidationError(ve, inner)

	assert.Equal(t, "Validation failed", validationErr.Error())
	assert.ErrorIs(t, validationErr, inner)

	var vErr *apperror.ValidationAppError
	require.True(t, errors.As(validationErr, &vErr))
	assert.Len(t, vErr.Errors, 1)
}

// ---------------------------------------------------------------------------
// EDR-0011: WithCode and ErrorCode tests
// ---------------------------------------------------------------------------

func TestWithCode_ShallowCopy(t *testing.T) {
	original := apperror.NewAppError(apperror.ErrCodeConflict, "Original message", fmt.Errorf("test error"))
	withCode := original.WithCode("WA-AU-001")

	assert.Equal(t, original.Message, withCode.Message)
	assert.Equal(t, original.Code, withCode.Code)
	assert.Equal(t, "WA-AU-001", withCode.ErrorCode)
	assert.Empty(t, original.ErrorCode, "original ErrorCode should remain empty")
	assert.NotSame(t, original, withCode, "WithCode must return a new object")
}

func TestWithCode_Overwrite(t *testing.T) {
	original := apperror.NewAppError(apperror.ErrCodeConflict, "msg", fmt.Errorf("err"))
	first := original.WithCode("A")
	second := first.WithCode("B")

	assert.Equal(t, "A", first.ErrorCode)
	assert.Equal(t, "B", second.ErrorCode)
	assert.Empty(t, original.ErrorCode)
}

func TestWithCode_ErrorsAs(t *testing.T) {
	original := apperror.NewAppError(apperror.ErrCodeNotFound, "Resource not found", fmt.Errorf("db error"))
	withCode := original.WithCode("WA-RS-001")

	var appErr *apperror.AppError
	require.True(t, errors.As(withCode, &appErr))
	assert.Equal(t, "WA-RS-001", appErr.ErrorCode)
}

func TestRespond_AppError_WithCode(t *testing.T) {
	w := httptest.NewRecorder()
	logger := zap.NewNop()

	err := apperror.NewAppError(apperror.ErrCodeNotFound, "Not found", fmt.Errorf("test")).WithCode("WA-RS-001")
	apperror.Respond(w, logger, err)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, "WA-RS-001", body["code"])
	assert.Equal(t, "Not found", body["detail"])
}

func TestRespond_AppError_WithoutCode(t *testing.T) {
	w := httptest.NewRecorder()
	logger := zap.NewNop()

	err := apperror.NewAppError(apperror.ErrCodeConflict, "Conflict", fmt.Errorf("test"))
	apperror.Respond(w, logger, err)

	assert.Equal(t, http.StatusConflict, w.Code)

	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Nil(t, body["code"], "code field should be absent when ErrorCode is empty")
	assert.Equal(t, "Conflict", body["detail"])
}

// ---------------------------------------------------------------------------
// EDR-0011 Phase 5: ErrorCodeRecorder integration tests
// ---------------------------------------------------------------------------

// errorCodeWriter wraps httptest.ResponseRecorder and implements SetErrorCode
// to verify that apperror.Respond propagates the error code.
type errorCodeWriter struct {
	*httptest.ResponseRecorder
	errorCode string
}

func (w *errorCodeWriter) SetErrorCode(code string) {
	w.errorCode = code
}

func TestRespond_SetsErrorCodeOnRecorder(t *testing.T) {
	w := &errorCodeWriter{ResponseRecorder: httptest.NewRecorder()}
	logger := zap.NewNop()

	err := apperror.NewAppError(apperror.ErrCodeNotFound, "Not found", fmt.Errorf("db miss")).
		WithCode("WA-RS-001")
	apperror.Respond(w, logger, err)

	assert.Equal(t, "WA-RS-001", w.errorCode,
		"Respond should call SetErrorCode when the writer implements the interface")
}

func TestRespond_DoesNotSetErrorCodeWhenEmpty(t *testing.T) {
	w := &errorCodeWriter{ResponseRecorder: httptest.NewRecorder()}
	logger := zap.NewNop()

	err := apperror.NewAppError(apperror.ErrCodeConflict, "Conflict", fmt.Errorf("dup"))
	apperror.Respond(w, logger, err)

	assert.Empty(t, w.errorCode,
		"Respond should not call SetErrorCode when ErrorCode is empty")
}

func TestRespond_PlainWriterIgnoresErrorCode(t *testing.T) {
	w := httptest.NewRecorder()
	logger := zap.NewNop()

	err := apperror.NewAppError(apperror.ErrCodeNotFound, "Not found", fmt.Errorf("miss")).
		WithCode("WA-RS-001")

	assert.NotPanics(t, func() {
		apperror.Respond(w, logger, err)
	}, "Respond should not panic when writer does not implement SetErrorCode")
	assert.Equal(t, http.StatusNotFound, w.Code)
}
