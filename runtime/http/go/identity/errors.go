package identity

import (
	"errors"
	"net/http"

	"github.com/parable-work/superschematic/runtime/http/go/apperror"
	"github.com/parable-work/superschematic/runtime/http/go/requestctx"
	"github.com/parable-work/superschematic/runtime/http/go/response"
	"github.com/parable-work/superschematic/runtime/schema/go/validate"
	"go.uber.org/zap"
)

// The problem codes the identity runtime answers with, the code member of
// the RFC 9457 body. A refused input is response.ValidationCode
// (bad_request), with its field errors.
const (
	// CodeInvalidCredentials: a login, or changePassword's current
	// password, that does not verify, for whatever reason. 401.
	CodeInvalidCredentials = "invalid_credentials"
	// CodeUnauthorized: a route that needs a caller, without a usable
	// session: none, an unusable Authorization header, or a session that is
	// unknown, expired, revoked or idle, or whose user is disabled. 401.
	CodeUnauthorized = "unauthorized"
	// CodeForbidden: the caller lacks the route's permission, or grants
	// what they do not hold. 403.
	CodeForbidden = "forbidden"
	// CodeCrossOrigin: a cookie request, or a cookie login, the
	// cross-origin check refuses. 403.
	CodeCrossOrigin = "cross_origin"
	// CodeNotFound: no user or role has the id. 404.
	CodeNotFound = "not_found"
	// CodeConflict: the login or the role name is taken. 409.
	CodeConflict = "conflict"
	// CodeInvalidPermission: a role's permission is not dotted segments of
	// letters, digits, '_' and '-'. 422, with the permissions in details.
	CodeInvalidPermission = "invalid_permission"
)

func problem(category, code, message string, cause error) *apperror.AppError {
	return &apperror.AppError{Code: category, ErrorCode: code, Message: message, Err: cause}
}

func invalidCredentials(cause error) *apperror.AppError {
	return problem(apperror.ErrCodeUnauthorized, CodeInvalidCredentials, "Invalid login or password", cause)
}

func unauthenticated(cause error) *apperror.AppError {
	return problem(apperror.ErrCodeUnauthorized, CodeUnauthorized, "Authentication required", cause)
}

func forbidden(message string, details any) *apperror.AppError {
	e := problem(apperror.ErrCodeForbidden, CodeForbidden, message, nil)
	e.Details = details
	return e
}

func crossOrigin(cause error) *apperror.AppError {
	return problem(apperror.ErrCodeForbidden, CodeCrossOrigin, "Cross-origin request refused", cause)
}

func notFound(what string) *apperror.AppError {
	return problem(apperror.ErrCodeNotFound, CodeNotFound, what+" not found", nil)
}

func conflict(message string) *apperror.AppError {
	return problem(apperror.ErrCodeConflict, CodeConflict, message, nil)
}

func invalidPermissions(permissions []string) *apperror.AppError {
	e := problem(apperror.ErrCodeUnprocessable, CodeInvalidPermission, "A permission is dotted segments of letters, digits, '_' and '-'", nil)
	e.Details = map[string]any{"permissions": permissions}
	return e
}

// fieldErrors collects the refusals of an input's fields.
type fieldErrors struct {
	errs validate.ValidationErrors
}

func (f *fieldErrors) add(field, validator, message string) {
	if f.errs == nil {
		f.errs = validate.NewValidationErrors()
	}
	f.errs.AddFieldError(field, validator, message)
}

// err is the 400 the fields' refusals are, or nil without any.
func (f *fieldErrors) err() error {
	if f.errs == nil || !f.errs.HasErrors() {
		return nil
	}
	return apperror.NewValidationError(f.errs, nil)
}

// WriteError answers a request with a service error: an *apperror.AppError
// as its status, code and detail (and details), a validation error as 400
// bad_request with its field errors, and anything else as 500. It logs the
// refusal on the request's logger, Warn below 500 and Error from it, and
// never sends the cause.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	logger := requestctx.LoggerFromContext(r.Context())
	var validation *apperror.ValidationAppError
	if errors.As(err, &validation) {
		response.LoggedValidationErrors(w, r, validation.Errors)
		return
	}
	var appErr *apperror.AppError
	if !errors.As(err, &appErr) {
		appErr = apperror.InternalError(err)
	}
	status := appErr.HTTPStatus()
	if rec, ok := w.(interface{ SetErrorCode(string) }); ok && appErr.ErrorCode != "" {
		rec.SetErrorCode(appErr.ErrorCode)
	}
	fields := []zap.Field{zap.Int("status", status), zap.String("error_code", appErr.ErrorCode)}
	if appErr.Err != nil {
		fields = append(fields, zap.NamedError("cause", appErr.Err))
	}
	if status >= http.StatusInternalServerError {
		logger.Error("identity request failed", fields...)
	} else {
		logger.Warn("identity request refused", fields...)
	}
	if appErr.Details != nil {
		response.ErrorWithDetails(w, status, appErr.Message, appErr.ErrorCode, appErr.Details)
		return
	}
	response.RequestError(w, r, status, appErr.Message, appErr.ErrorCode)
}
