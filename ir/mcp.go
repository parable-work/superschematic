package ir

import (
	"fmt"
	"regexp"
	"strings"
)

// MCPHandleMaxLength is the longest tool handle @mcp accepts.
const MCPHandleMaxLength = 48

var mcpHandlePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)

// OperationMCP is an operation's MCP classification, declared with @mcp: a
// visible tool with its wire handle, or a hidden operation with the reason
// it is not a tool. An operation without @mcp is not classified and is not
// published as a tool either.
//
// Name, Description and Icon are generated, never authored: for a visible
// tool the api generator fills them from @docs (title, description) and
// @icon. What an icon set adds to the icon (a family, a style) is an
// extension's tool hook.
type OperationMCP struct {
	// Handle is the tool's wire identifier: lowercase snake_case starting
	// with a letter, at most MCPHandleMaxLength characters, unique within
	// the API. Required on a visible tool, absent on a hidden one.
	Handle string `json:"handle,omitempty" yaml:"handle,omitempty"`

	// Hidden keeps the operation out of the published tools.
	Hidden bool `json:"hidden" yaml:"hidden"`

	// HiddenReason says why a hidden operation is not a tool. Required when
	// Hidden.
	HiddenReason string `json:"hiddenReason,omitempty" yaml:"hiddenReason,omitempty"`

	// Invocation is a visible tool's invocation policy, written under its
	// own key right after hiddenReason (see MCPInvocation). The loader
	// fills in the registry's default when @mcp omits it; a hidden record
	// has none.
	Invocation MCPInvocation `json:"-" yaml:"-"`

	// Additive declares that a visible tool that writes only adds: it never
	// changes or deletes data that exists. Its annotations then say
	// destructiveHint false. A read_only operation writes nothing, so the
	// api generator refuses it there, and a hidden record has none (D123).
	Additive bool `json:"additive,omitempty" yaml:"additive,omitempty"`

	// Meta is copied into the tool's MCP _meta object as written. Only a
	// visible tool takes it.
	Meta map[string]any `json:"_meta,omitempty" yaml:"_meta,omitempty"`

	// Name is the generated display name: the @docs title.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Description is the generated description: the @docs description.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Icon is the generated icon: the operation's @icon. Nil without one.
	Icon *MCPIcon `json:"icon,omitempty" yaml:"icon,omitempty"`

	// Annotations are the generated MCP tool annotations of a visible tool,
	// from @docs replayMode and Additive (ResolveMCPToolAnnotations). Nil
	// until the api generator resolves the tool; never authored (D123).
	Annotations *MCPToolAnnotations `json:"annotations,omitempty" yaml:"annotations,omitempty"`
}

// MCPToolAnnotations are the behavior hints an MCP host reads on a tool.
// They are hints: the operation's permissions and replay contract enforce.
type MCPToolAnnotations struct {
	// ReadOnlyHint: the operation declares replayMode read_only.
	ReadOnlyHint bool `json:"readOnlyHint" yaml:"readOnlyHint"`

	// DestructiveHint: the operation does not declare replayMode read_only,
	// and its @mcp does not declare it additive. The MCP specification reads
	// false as "only additive updates", so a tool that may write is marked
	// destructive, the specification's default, unless its schema says that
	// it only adds.
	DestructiveHint bool `json:"destructiveHint" yaml:"destructiveHint"`

	// IdempotentHint: the operation declares replayMode idempotent, so a
	// repeated call with the same idempotency key has no further effect.
	IdempotentHint bool `json:"idempotentHint" yaml:"idempotentHint"`
}

// ResolveMCPToolAnnotations derives a visible tool's annotations from its
// replay contract and its @mcp additive declaration. Safety is never
// inferred from the HTTP method or the operation's name (D123).
func ResolveMCPToolAnnotations(replayMode DocsReplayMode, additive bool) *MCPToolAnnotations {
	readOnly := replayMode == DocsReplayModeReadOnly
	return &MCPToolAnnotations{
		ReadOnlyHint:    readOnly,
		DestructiveHint: !readOnly && !additive,
		IdempotentHint:  replayMode == DocsReplayModeIdempotent,
	}
}

// MCPIcon names a tool's glyph. The core sets Name from @icon; Family and
// Style are empty unless an extension's tool hook sets them for its icon set.
type MCPIcon struct {
	Name   string `json:"name" yaml:"name"`
	Family string `json:"family,omitempty" yaml:"family,omitempty"`
	Style  string `json:"style,omitempty" yaml:"style,omitempty"`
}

// ValidateOperationMCP checks the shape of an authored @mcp record. nil is
// valid: @mcp is optional. A visible record needs a handle and no reason; a
// hidden one needs a reason and no handle, _meta or invocation policy. The
// generated fields must be empty. Which invocation policy key and values a
// build accepts is the registry's; this checks only the policy's shape.
func ValidateOperationMCP(mcp *OperationMCP) error {
	if mcp == nil {
		return nil
	}
	if mcp.Name != "" || mcp.Description != "" || mcp.Icon != nil {
		return fmt.Errorf("name, description and icon are generated from @docs and @icon and cannot be declared")
	}
	if mcp.Annotations != nil {
		return fmt.Errorf("annotations are generated from @docs replayMode and additive and cannot be declared")
	}
	if mcp.Hidden {
		if strings.TrimSpace(mcp.HiddenReason) == "" {
			return fmt.Errorf("reason must be non-empty for a hidden operation")
		}
		if strings.TrimSpace(mcp.HiddenReason) != mcp.HiddenReason {
			return fmt.Errorf("reason must not contain surrounding whitespace")
		}
		if mcp.Handle != "" {
			return fmt.Errorf("a hidden operation must not declare a handle")
		}
		if len(mcp.Meta) != 0 {
			return fmt.Errorf("a hidden operation must not declare _meta")
		}
		if !mcp.Invocation.IsZero() {
			return fmt.Errorf("a hidden operation must not declare %s", mcp.Invocation.keyName())
		}
		if mcp.Additive {
			return fmt.Errorf("a hidden operation must not declare additive")
		}
		return nil
	}
	if strings.TrimSpace(mcp.Handle) == "" {
		return fmt.Errorf("handle must be non-empty for a visible operation")
	}
	if err := mcp.Invocation.validate(); err != nil {
		return err
	}
	if mcp.HiddenReason != "" {
		return fmt.Errorf("a visible operation must not declare a reason")
	}
	if len(mcp.Handle) > MCPHandleMaxLength {
		return fmt.Errorf("handle %q must be at most %d characters", mcp.Handle, MCPHandleMaxLength)
	}
	if !mcpHandlePattern.MatchString(mcp.Handle) {
		return fmt.Errorf("handle %q must be lowercase snake_case and begin with a letter", mcp.Handle)
	}
	return nil
}

// ValidateOperationIcon checks the glyph name an operation's @icon stores:
// non-blank, with no surrounding whitespace. Which names exist is an icon
// set's rule, which an extension registers as a check.
func ValidateOperationIcon(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("icon name must be non-empty")
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("icon name must not contain surrounding whitespace")
	}
	return nil
}
