package apigen

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The core's invocation policy: the key @mcp reads it from and every
// output writes it under, its values and its default.
const (
	// DefaultToolInvocationKey is the core's policy key.
	DefaultToolInvocationKey = "invocationPolicy"
	// ToolInvocationAuto: a client runs the tool when a model calls it.
	// The core default.
	ToolInvocationAuto = "auto"
	// ToolInvocationAsk: a client asks the person before it runs the tool.
	ToolInvocationAsk = "ask"
)

// ToolInvocationPolicy is how @mcp spells a visible tool's invocation
// policy (ir.MCPInvocation): whether an MCP client runs the tool as soon as
// a model calls it or asks the person first. The core's is
// DefaultToolInvocationPolicy; an extension replaces it with
// Registry.RegisterToolInvocationPolicy, and one registry holds at most
// one. The policy decides what the loader accepts and fills in and what
// every output writes:
//
//   - @mcp({...}) and the data forms' mcp record accept Key, with a value
//     from Values; the core key is then unknown.
//   - A visible tool that omits Key gets Default when it loads.
//   - The IR, tools/schema.json, tools/mcp-audit.json and tools/index.ts
//     write the policy under Key, and tools/index.ts types it as the union
//     of Values in their order.
type ToolInvocationPolicy struct {
	// Extension is the registering extension's Name(); empty for the
	// core's policy.
	Extension string
	// Key is the policy's key: a letter followed by letters, digits or
	// underscores, and not a key @mcp already uses.
	Key string
	// Values are the allowed values, in the order tools/index.ts lists
	// them: lowercase letters, digits, underscores and hyphens, starting
	// with a letter, each once.
	Values []string
	// Default is the value a visible tool gets when @mcp omits Key. It is
	// one of Values.
	Default string
}

// DefaultToolInvocationPolicy returns the core's policy: invocationPolicy,
// auto or ask, auto by default.
func DefaultToolInvocationPolicy() ToolInvocationPolicy {
	return ToolInvocationPolicy{
		Key:     DefaultToolInvocationKey,
		Values:  []string{ToolInvocationAuto, ToolInvocationAsk},
		Default: ToolInvocationAuto,
	}
}

var toolInvocationValuePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Validate checks the policy's own shape: the key, the values and the
// default.
func (p ToolInvocationPolicy) Validate() error {
	if err := ir.ValidateMCPInvocationKey(p.Key); err != nil {
		return err
	}
	if len(p.Values) == 0 {
		return fmt.Errorf("invocation policy %s lists no values", p.Key)
	}
	for i, value := range p.Values {
		if !toolInvocationValuePattern.MatchString(value) {
			return fmt.Errorf("invocation policy %s value %q must be lowercase letters, digits, underscores or hyphens, starting with a letter", p.Key, value)
		}
		if slices.Contains(p.Values[:i], value) {
			return fmt.Errorf("invocation policy %s lists %q twice", p.Key, value)
		}
	}
	if !slices.Contains(p.Values, p.Default) {
		return fmt.Errorf("invocation policy %s default %q is not one of %s", p.Key, p.Default, p.QuotedValues())
	}
	return nil
}

// OrDefault returns p, or DefaultToolInvocationPolicy when p is the zero
// value.
func (p ToolInvocationPolicy) OrDefault() ToolInvocationPolicy {
	if p.Key == "" && len(p.Values) == 0 && p.Default == "" {
		return DefaultToolInvocationPolicy()
	}
	return p
}

// CheckValue explains why value is not one of the policy's values, or
// returns nil.
func (p ToolInvocationPolicy) CheckValue(value string) error {
	if slices.Contains(p.Values, value) {
		return nil
	}
	return fmt.Errorf("%s %q is not one of %s", p.Key, value, p.QuotedValues())
}

// Resolve returns a visible tool's policy: Default under Key when the
// record has none, the record's own when its key is Key and its value one
// of Values, and an error otherwise.
func (p ToolInvocationPolicy) Resolve(invocation ir.MCPInvocation) (ir.MCPInvocation, error) {
	if invocation.IsZero() {
		return ir.MCPInvocation{Key: p.Key, Value: p.Default}, nil
	}
	if invocation.Key != p.Key {
		return ir.MCPInvocation{}, fmt.Errorf("invocation policy key %q is not this build's key %q", invocation.Key, p.Key)
	}
	if err := p.CheckValue(invocation.Value); err != nil {
		return ir.MCPInvocation{}, err
	}
	return invocation, nil
}

// QuotedValues lists the values quoted and comma-separated, for messages.
func (p ToolInvocationPolicy) QuotedValues() string {
	quoted := make([]string, len(p.Values))
	for i, value := range p.Values {
		quoted[i] = strconv.Quote(value)
	}
	return strings.Join(quoted, ", ")
}
