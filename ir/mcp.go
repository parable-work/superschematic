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

	// Meta is copied into the tool's MCP _meta object as written. Only a
	// visible tool takes it.
	Meta map[string]any `json:"_meta,omitempty" yaml:"_meta,omitempty"`

	// Name is the generated display name: the @docs title.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Description is the generated description: the @docs description.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Icon is the generated icon: the operation's @icon. Nil without one.
	Icon *MCPIcon `json:"icon,omitempty" yaml:"icon,omitempty"`
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
// hidden one needs a reason and no handle or _meta. The generated fields
// must be empty.
func ValidateOperationMCP(mcp *OperationMCP) error {
	if mcp == nil {
		return nil
	}
	if mcp.Name != "" || mcp.Description != "" || mcp.Icon != nil {
		return fmt.Errorf("name, description and icon are generated from @docs and @icon and cannot be declared")
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
		return nil
	}
	if strings.TrimSpace(mcp.Handle) == "" {
		return fmt.Errorf("handle must be non-empty for a visible operation")
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
