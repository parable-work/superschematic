package apperror

import (
	"errors"
	"net/http"

	"github.com/parable-work/superschematic/runtime/http/go/response"
	"github.com/parable-work/superschematic/runtime/schema/go/validate"
	"go.uber.org/zap"
)

// errorCodeRecorder is satisfied by middleware.responseRecorder so that
// Respond can propagate the wire error code for OTel span attributes without
// requiring request context or response headers.
type errorCodeRecorder interface {
	SetErrorCode(code string)
}

const (
	ErrCodeNotFound       = "NOT_FOUND"
	ErrCodeUnauthorized   = "UNAUTHORIZED"
	ErrCodeForbidden      = "FORBIDDEN"
	ErrCodeConflict       = "CONFLICT"
	ErrCodeUnprocessable  = "UNPROCESSABLE_ENTITY"
	ErrCodeBadRequest     = "BAD_REQUEST"
	ErrCodeInternal       = "INTERNAL_ERROR"
	ErrCodeNotImplemented = "NOT_IMPLEMENTED"
)

// AppError represents an application-level error with a user-safe message.
type AppError struct {
	Code      string // HTTP error category code (e.g., "NOT_FOUND", "CONFLICT")
	ErrorCode string // Standardized wire error code (e.g., "WA-AU-001")
	Message   string
	Err       error
}

func (e *AppError) Error() string {
	return e.Message
}

func (e *AppError) Unwrap() error {
	return e.Err
}

// WithCode returns a shallow copy of the AppError with the ErrorCode set.
func (e *AppError) WithCode(code string) *AppError {
	errCopy := *e
	errCopy.ErrorCode = code
	return &errCopy
}

// HTTPStatus returns the HTTP status mapped from the error code.
func (e *AppError) HTTPStatus() int {
	switch e.Code {
	case ErrCodeNotFound:
		return http.StatusNotFound
	case ErrCodeUnauthorized:
		return http.StatusUnauthorized
	case ErrCodeForbidden:
		return http.StatusForbidden
	case ErrCodeConflict:
		return http.StatusConflict
	case ErrCodeUnprocessable:
		return http.StatusUnprocessableEntity
	case ErrCodeBadRequest:
		return http.StatusBadRequest
	case ErrCodeInternal:
		return http.StatusInternalServerError
	case ErrCodeNotImplemented:
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}

// ValidationAppError represents a validation error with structured field errors.
type ValidationAppError struct {
	Errors validate.ValidationErrors
	Err    error
}

func (e *ValidationAppError) Error() string {
	return "Validation failed"
}

func (e *ValidationAppError) Unwrap() error {
	return e.Err
}

// StructuredBodyError is implemented by errors that want to render a custom
// JSON body instead of an RFC 9457 problem detail. Used by domain-specific
// failures whose response shape is fixed by an external contract (e.g. the
// schema-resolver write-time validation response in doc Section 8.1 of the
// typed-output-schemas design doc).
//
// Implementations supply the HTTP status, the body to marshal, and any
// log fields the central Respond logger should emit alongside the error.
// Respond delegates to this interface before falling through to the
// AppError path so a single domain type controls its own wire shape.
type StructuredBodyError interface {
	error
	HTTPStatus() int
	StructuredBody() any
	LogFields() []zap.Field
}

// Respond logs a server-side error and returns a safe client response.
func Respond(w http.ResponseWriter, logger *zap.Logger, err error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	var structuredErr StructuredBodyError
	if errors.As(err, &structuredErr) {
		fields := structuredErr.LogFields()
		fields = append(fields, zap.Error(err))
		logger.Error("structured request failure", fields...)
		response.JSON(w, structuredErr.HTTPStatus(), structuredErr.StructuredBody())
		return
	}

	var validationErr *ValidationAppError
	if errors.As(err, &validationErr) {
		logger.Warn("validation failed", zap.Error(validationErr.Err))
		response.ValidationErrors(w, validationErr.Errors)
		return
	}

	var appErr *AppError
	if errors.As(err, &appErr) {
		fields := []zap.Field{
			zap.Error(appErr.Err),
			zap.String("code", appErr.Code),
			zap.String("message", appErr.Message),
		}
		if appErr.ErrorCode != "" {
			fields = append(fields, zap.String("error_code", appErr.ErrorCode))
			if ec, ok := w.(errorCodeRecorder); ok {
				ec.SetErrorCode(appErr.ErrorCode)
			}
		}
		logger.Error("request failed", fields...)
		response.Error(w, appErr.HTTPStatus(), appErr.Message, appErr.ErrorCode)
		return
	}

	logger.Error("unexpected error", zap.Error(err))
	response.Error(w, http.StatusInternalServerError, "An unexpected error occurred")
}

// NewAppError creates a new AppError with the given code, message, and wrapped error.
func NewAppError(code, message string, err error) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
		Err:     err,
	}
}

// NotFoundError creates an AppError for resources that cannot be found.
func NotFoundError(resource string, err error) *AppError {
	return &AppError{
		Code:    ErrCodeNotFound,
		Message: resource + " not found",
		Err:     err,
	}
}

// UnauthorizedError creates an AppError for authentication failures.
func UnauthorizedError(err error) *AppError {
	return &AppError{
		Code:    ErrCodeUnauthorized,
		Message: "Authentication required",
		Err:     err,
	}
}

// ForbiddenError creates an AppError for authorization failures.
func ForbiddenError(err error) *AppError {
	return &AppError{
		Code:    ErrCodeForbidden,
		Message: "Access denied",
		Err:     err,
	}
}

// ConflictError creates an AppError for resource conflicts.
func ConflictError(message string, err error) *AppError {
	return &AppError{
		Code:    ErrCodeConflict,
		Message: message,
		Err:     err,
	}
}

// UnprocessableEntityError creates an AppError for semantically invalid requests.
func UnprocessableEntityError(message string, err error) *AppError {
	return &AppError{
		Code:    ErrCodeUnprocessable,
		Message: message,
		Err:     err,
	}
}

// BadRequestError creates an AppError for invalid client requests.
func BadRequestError(message string, err error) *AppError {
	return &AppError{
		Code:    ErrCodeBadRequest,
		Message: message,
		Err:     err,
	}
}

// InternalError creates an AppError for unexpected internal failures.
func InternalError(err error) *AppError {
	return &AppError{
		Code:    ErrCodeInternal,
		Message: "An unexpected error occurred",
		Err:     err,
	}
}

// NotImplementedError creates an AppError for endpoints that are defined in
// schema but not yet wired up to business logic.
func NotImplementedError(message string) *AppError {
	return &AppError{
		Code:    ErrCodeNotImplemented,
		Message: message,
	}
}

// NewValidationError creates a ValidationAppError with structured field errors.
func NewValidationError(validationErrors validate.ValidationErrors, err error) *ValidationAppError {
	return &ValidationAppError{
		Errors: validationErrors,
		Err:    err,
	}
}

// UniqueConstraintError creates a ValidationAppError for a unique constraint violation.
func UniqueConstraintError(field, message string, err error) *ValidationAppError {
	validationErrors := validate.NewValidationErrors()
	validationErrors.AddFieldError(field, "unique", message)
	return &ValidationAppError{
		Errors: validationErrors,
		Err:    err,
	}
}
