package serviceauth

import (
	"errors"
	"net/http"

	"github.com/parable-work/superschematic/runtime/http/go/requestctx"
	"github.com/parable-work/superschematic/runtime/http/go/response"
	"go.uber.org/zap"
)

// The problem codes of a service refusal, the code member of the RFC 9457
// body. End-user refusals keep their own.
const (
	// CodeUnauthorized: a credential is required and missing, or present
	// and does not verify. 401.
	CodeUnauthorized = "service_unauthorized"
	// CodeForbidden: the credential verifies, but its identity is no caller
	// of this server, or the route does not list the caller. 403.
	CodeForbidden = "service_forbidden"
	// CodeUnavailable: a failure that is not the caller's, such as keys
	// that cannot be fetched with none cached. 503.
	CodeUnavailable = "service_unavailable"
)

// The detail of each refusal. They name no reason, which goes to the log.
const (
	DetailRequired    = "Service credential required"
	DetailInvalid     = "Invalid service credential"
	DetailForbidden   = "Service not permitted"
	DetailUnavailable = "Service credential could not be checked"
)

// Error is a service authenticator's refusal: the status, problem code and
// detail the client sees, and the cause, which only the log sees.
type Error struct {
	Status int
	Code   string
	Detail string
	// Err is why, for the server's log. It never reaches the client.
	Err error
}

func (e *Error) Error() string {
	msg := "serviceauth: " + e.Code + ": " + e.Detail
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error {
	return e.Err
}

// Missing is the refusal of a route that requires a service credential when
// the request carries none.
func Missing() *Error {
	return &Error{Status: http.StatusUnauthorized, Code: CodeUnauthorized, Detail: DetailRequired}
}

// Invalid is the refusal of a credential that does not verify.
func Invalid(cause error) *Error {
	return &Error{Status: http.StatusUnauthorized, Code: CodeUnauthorized, Detail: DetailInvalid, Err: cause}
}

// Forbidden is the refusal of a verified identity that is no caller of this
// server, or a caller the route does not list.
func Forbidden(cause error) *Error {
	return &Error{Status: http.StatusForbidden, Code: CodeForbidden, Detail: DetailForbidden, Err: cause}
}

// Unavailable is a failure that is not the caller's.
func Unavailable(cause error) *Error {
	return &Error{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Detail: DetailUnavailable, Err: cause}
}

// errorCodeRecorder is the request log middleware's response recorder, as
// apperror.Respond uses it, so the span carries the problem code.
type errorCodeRecorder interface {
	SetErrorCode(code string)
}

// WriteError answers a request with an authenticator's refusal: an *Error
// (or an error wrapping one) as its status, code and detail; any other
// error as 503 service_unavailable. The problem body is the response
// package's, with the request id; the cause is logged on the request's
// logger and never sent.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = Unavailable(err)
	}
	if rec, ok := w.(errorCodeRecorder); ok {
		rec.SetErrorCode(e.Code)
	}
	if e.Err != nil {
		ctx := r.Context()
		logger := requestctx.LoggerFromContext(ctx).With(zap.NamedError("cause", e.Err))
		r = r.WithContext(requestctx.ContextWithLogger(ctx, logger))
	}
	response.LoggedError(w, r, e.Status, e.Detail, e.Code)
}
