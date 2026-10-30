package rules

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Code is a refusal's code, the core's (superschematic_rules::Code).
type Code string

// The core's refusal codes.
const (
	CodeInvalidSchema      Code = "invalid_schema"
	CodeIncompatibleChange Code = "incompatible_change"
	CodeNotFound           Code = "not_found"
	CodeInvalidArgument    Code = "invalid_argument"
	CodeForbidden          Code = "forbidden"
	CodeInvalidInstance    Code = "invalid_instance"
	CodeConflict           Code = "conflict"
	CodeVetoed             Code = "vetoed"
	CodeBusy               Code = "busy"
	CodeBehaviorError      Code = "behavior_error"
	CodeInternal           Code = "internal"
)

// Refusal is a refused command, whole: the core's, or the adapter's own
// (busy, conflict, internal, invalid_argument, the owned-write check). It
// is the error every adapter function returns for a refusal; a failure of
// the database comes back as the driver's error, wrapped. Class is the
// engine's class for it (BehaviorVetoError, InstanceValidationError,
// EngineError, ...), and a caller branches on Code (vetoed, then
// details.vetoCode).
type Refusal struct {
	Code    Code            `json:"code"`
	Status  int             `json:"status"`
	Class   string          `json:"class"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

func (r *Refusal) Error() string {
	return string(r.Code) + ": " + r.Message
}

// AsRefusal is the refusal in err's chain, if any.
func AsRefusal(err error) (*Refusal, bool) {
	var refusal *Refusal
	if errors.As(err, &refusal) && refusal != nil {
		return refusal, true
	}
	return nil, false
}

// IsNotFound reports whether err's chain holds a refusal of
// CodeNotFound: a command on a row, type, use or operation that does not
// exist, or on a row its table holds deleted.
func IsNotFound(err error) bool {
	refusal, ok := AsRefusal(err)
	return ok && refusal.Code == CodeNotFound
}

// Internal is the refusal of a host's own failure: internal, 500, the
// class Error, its message "rules: " and the text, as the core's
// Refusal::internal.
func Internal(format string, args ...any) *Refusal {
	return &Refusal{Code: CodeInternal, Status: 500, Class: "Error", Message: "rules: " + fmt.Sprintf(format, args...)}
}

// BusyRefusal is the refusal of a command whose row kept moving: busy,
// 503, of the class EngineError (D58), in the reference host's words
// (superschematic-rules-testkit's MemoryHost), so every adapter refuses it
// alike.
func BusyRefusal(typeName string, command Command, attempts int) *Refusal {
	action := command.Action
	key := ""
	switch action.Kind {
	case ActionUpdate, ActionDelete, ActionOperation, ActionDeadline:
		key = " " + action.Key
	case ActionCreate:
		if action.ID != "" {
			key = " " + action.ID
		}
	}
	return &Refusal{
		Code:    CodeBusy,
		Status:  503,
		Class:   "EngineError",
		Message: fmt.Sprintf("%s of %s%s: the row kept moving while the command ran; it ran %d times and nothing was written, so it may be tried again", action.name(), typeName, key, attempts),
	}
}

// conflictRefusal refuses a create of a key the table holds, live or not,
// as the reference host refuses it.
func conflictRefusal(typeName, key string) *Refusal {
	return &Refusal{Code: CodeConflict, Status: 409, Class: "EngineError", Message: fmt.Sprintf("%s %s already exists", typeName, key)}
}

// ownedIssue is one issue of the owned-write refusal's details.
type ownedIssue struct {
	Path    string `json:"path"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}
