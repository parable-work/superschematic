package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ErrorCode is what a refused call was refused for (the engine's
// EngineErrorCode): a client branches on it, not on the message.
type ErrorCode string

// The engine's refusal codes, and internal for a panic the archive caught.
const (
	CodeInvalidSchema     ErrorCode = "invalid_schema"
	CodeIncompatible      ErrorCode = "incompatible_change"
	CodeNameTaken         ErrorCode = "name_taken"
	CodeNotFound          ErrorCode = "not_found"
	CodeUnknownNamespace  ErrorCode = "unknown_namespace"
	CodeNamespaceArchived ErrorCode = "namespace_archived"
	CodeCursorExpired     ErrorCode = "cursor_expired"
	CodeInvalidArgument   ErrorCode = "invalid_argument"
	CodeForbidden         ErrorCode = "forbidden"
	CodeInvalidInstance   ErrorCode = "invalid_instance"
	CodeConflict          ErrorCode = "conflict"
	CodeSeqMismatch       ErrorCode = "seq_mismatch"
	CodeVetoed            ErrorCode = "vetoed"
	CodeValueTooLarge     ErrorCode = "value_too_large"
	CodeUnavailable       ErrorCode = "unavailable"
	CodeRateLimited       ErrorCode = "rate_limited"
	CodeBusy              ErrorCode = "busy"
	CodeInternal          ErrorCode = "internal"
)

// Status is the HTTP status a refusal with the code answers with, as the
// engine's routes answer it: 500 for a code that is no refusal's.
func (c ErrorCode) Status() int {
	switch c {
	case CodeInvalidArgument:
		return http.StatusBadRequest
	case CodeForbidden:
		return http.StatusForbidden
	case CodeNotFound, CodeUnknownNamespace:
		return http.StatusNotFound
	case CodeNamespaceArchived, CodeConflict, CodeNameTaken, CodeIncompatible, CodeVetoed:
		return http.StatusConflict
	case CodeCursorExpired:
		return http.StatusGone
	case CodeSeqMismatch:
		return http.StatusPreconditionFailed
	case CodeValueTooLarge:
		return http.StatusRequestEntityTooLarge
	case CodeInvalidSchema, CodeInvalidInstance:
		return http.StatusUnprocessableEntity
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeUnavailable, CodeBusy:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// Error is a call the engine did not make, as the TypeScript engine
// throws it: its class, its code and message, and the class's other
// members.
type Error struct {
	// Class is the TypeScript engine's error class: EngineError or one of
	// its subclasses (SchemaDocumentError, InstanceValidationError,
	// BehaviorVetoError, ...), BehaviorError (a defect in a behavior's
	// code), BehaviorConfigError, SqliteError, TypeError (an argument or
	// option the engine cannot take) or Error.
	Class string
	// Code is a refusal's code; empty for a class with none.
	Code ErrorCode
	// SQLiteCode is a SqliteError's extended result code.
	SQLiteCode int
	Message    string
	// Details are the class's members beyond its code and message: a
	// refused document's or instance's issues, a veto's behavior, action,
	// reason, vetoCode and vetoDetails, an expired cursor's floor and head.
	// Nil when it has none.
	Details json.RawMessage
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("engine: %s %s: %s", e.Class, e.Code, e.Message)
	}
	return fmt.Sprintf("engine: %s: %s", e.Class, e.Message)
}

// Refused reports whether the error is a refusal (an EngineError or one of
// its subclasses, with a code), which a server answers with the code's
// status; anything else is a 500.
func (e *Error) Refused() bool {
	return e.Code != "" && e.Code != CodeInternal && e.Class != "SqliteError"
}

// Status is the HTTP status a server answers the error with.
func (e *Error) Status() int {
	if !e.Refused() {
		return http.StatusInternalServerError
	}
	return e.Code.Status()
}

// ErrClosed is a call on an engine that is closed.
var ErrClosed = errors.New("engine: the engine is closed")

// errBadArguments is the archive refusing a null pointer, which the binding
// never passes.
var errBadArguments = errors.New("engine: the archive refused its arguments")

// IsCode reports whether err is an engine Error with the code.
func IsCode(err error, code ErrorCode) bool {
	var refused *Error
	return errors.As(err, &refused) && refused.Code == code
}

// failure is the error an export's code and output say.
func failure(c code, out []byte) error {
	switch c {
	case codeClosed:
		return ErrClosed
	case codeBadArguments:
		return errBadArguments
	case codeError:
		return decodeError(out)
	default:
		return fmt.Errorf("engine: the archive returned %d", c)
	}
}

func decodeError(out []byte) error {
	var document struct {
		Error *struct {
			Class   string          `json:"class"`
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &document); err != nil || document.Error == nil {
		return fmt.Errorf("engine: an unreadable error document: %q", out)
	}
	thrown := document.Error
	refused := &Error{Class: thrown.Class, Message: thrown.Message, Details: thrown.Details}
	code := bytes.TrimSpace(thrown.Code)
	switch {
	case len(code) == 0 || bytes.Equal(code, []byte("null")):
	case code[0] == '"':
		var text string
		if json.Unmarshal(code, &text) == nil {
			refused.Code = ErrorCode(text)
		}
	default:
		var number int
		if json.Unmarshal(code, &number) == nil {
			refused.SQLiteCode = number
		}
	}
	return refused
}
