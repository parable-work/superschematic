package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
)

// MCPInvocation is a visible tool's invocation policy: whether an MCP
// client should run the tool as soon as a model calls it or ask the person
// first. Key is the name the policy is written under, in @mcp({...}), in the
// IR and in every tool document; Value is one of the values the registry
// allows under that key. The core's key is invocationPolicy, with the
// values auto (the default) and ask; an extension may register another key,
// other values and another default. The pair travels together so the IR
// needs no registry to encode or decode it.
//
// In JSON and YAML the policy is one more key of the mcp record, right
// after hiddenReason:
//
//	"mcp": { "handle": "delete_order", "hidden": false, "invocationPolicy": "ask" }
type MCPInvocation struct {
	Key   string
	Value string
}

// IsZero reports whether no policy is set.
func (p MCPInvocation) IsZero() bool {
	return p.Key == "" && p.Value == ""
}

// keyName names the policy in an error: its key, or a generic phrase when
// the key is missing.
func (p MCPInvocation) keyName() string {
	if p.Key == "" {
		return "an invocation policy"
	}
	return p.Key
}

// validate checks the policy's shape: nothing, or a valid key with a
// non-empty value.
func (p MCPInvocation) validate() error {
	if p.IsZero() {
		return nil
	}
	if p.Key == "" {
		return fmt.Errorf("invocation policy %q has no key", p.Value)
	}
	if err := ValidateMCPInvocationKey(p.Key); err != nil {
		return err
	}
	if p.Value == "" {
		return fmt.Errorf("%s must be non-empty", p.Key)
	}
	return nil
}

var mcpInvocationKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// mcpRecordKeys are the keys OperationMCP writes itself.
var mcpRecordKeys = []string{"handle", "hidden", "hiddenReason", "_meta", "name", "description", "icon"}

// mcpReservedKeys are the keys an invocation policy key cannot be: the
// record's own and the authoring key @mcp reads the hidden reason from.
var mcpReservedKeys = append(slices.Clone(mcpRecordKeys), "reason")

// ValidateMCPInvocationKey checks a key an invocation policy may be written
// under: a letter, then letters, digits or underscores, and not one of the
// keys the mcp record or @mcp already use (handle, hidden, hiddenReason,
// reason, _meta, name, description, icon).
func ValidateMCPInvocationKey(key string) error {
	if !mcpInvocationKeyPattern.MatchString(key) {
		return fmt.Errorf("invocation policy key %q must be a letter followed by letters, digits or underscores", key)
	}
	if slices.Contains(mcpReservedKeys, key) {
		return fmt.Errorf("invocation policy key %q is a key @mcp already uses", key)
	}
	return nil
}

// operationMCPHead and operationMCPTail are the keys OperationMCP writes
// before and after the invocation policy, in order.
type operationMCPHead struct {
	Handle       string `json:"handle,omitempty" yaml:"handle,omitempty"`
	Hidden       bool   `json:"hidden" yaml:"hidden"`
	HiddenReason string `json:"hiddenReason,omitempty" yaml:"hiddenReason,omitempty"`
}

type operationMCPTail struct {
	Meta        map[string]any `json:"_meta,omitempty" yaml:"_meta,omitempty"`
	Name        string         `json:"name,omitempty" yaml:"name,omitempty"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty"`
	Icon        *MCPIcon       `json:"icon,omitempty" yaml:"icon,omitempty"`
}

// operationMCPFields is every key but the invocation policy, for decoding.
type operationMCPFields struct {
	operationMCPHead `yaml:",inline"`
	operationMCPTail `yaml:",inline"`
}

func (m OperationMCP) head() operationMCPHead {
	return operationMCPHead{Handle: m.Handle, Hidden: m.Hidden, HiddenReason: m.HiddenReason}
}

func (m OperationMCP) tail() operationMCPTail {
	return operationMCPTail{Meta: m.Meta, Name: m.Name, Description: m.Description, Icon: m.Icon}
}

func (m *OperationMCP) setFields(f operationMCPFields) {
	m.Handle, m.Hidden, m.HiddenReason = f.Handle, f.Hidden, f.HiddenReason
	m.Meta, m.Name, m.Description, m.Icon = f.Meta, f.Name, f.Description, f.Icon
}

// encodable reports a policy the encoders cannot write: a value with no
// key has nowhere to go.
func (m OperationMCP) encodable() error {
	if m.Invocation.Value != "" && m.Invocation.Key == "" {
		return fmt.Errorf("ir: mcp invocation policy %q has no key", m.Invocation.Value)
	}
	return nil
}

// MarshalJSON writes handle, hidden and hiddenReason, then the invocation
// policy under its key when it has a value, then _meta, name, description
// and icon: the order the struct declares them in.
func (m OperationMCP) MarshalJSON() ([]byte, error) {
	if err := m.encodable(); err != nil {
		return nil, err
	}
	head, err := marshalJSONUnescaped(m.head())
	if err != nil {
		return nil, err
	}
	tail, err := marshalJSONUnescaped(m.tail())
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.Write(head[:len(head)-1])
	if m.Invocation.Value != "" {
		key, err := marshalJSONUnescaped(m.Invocation.Key)
		if err != nil {
			return nil, err
		}
		value, err := marshalJSONUnescaped(m.Invocation.Value)
		if err != nil {
			return nil, err
		}
		buf.WriteByte(',')
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(value)
	}
	if len(tail) > 2 {
		buf.WriteByte(',')
		buf.Write(tail[1:])
	} else {
		buf.WriteByte('}')
	}
	return buf.Bytes(), nil
}

// marshalJSONUnescaped encodes v without escaping HTML characters: the
// encoder that called MarshalJSON escapes the whole result the way it was
// configured to.
func marshalJSONUnescaped(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// UnmarshalJSON reads the record's own keys strictly and takes the one key
// that is not among them, when there is one, as the invocation policy. The
// IR does not know which key the registry allows; the loader checks that.
func (m *OperationMCP) UnmarshalJSON(data []byte) error {
	if string(bytes.TrimSpace(data)) == "null" {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var extra []string
	for key := range raw {
		if !slices.Contains(mcpRecordKeys, key) {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	invocation, err := invocationFromExtraKeys(extra, func(key string) (string, error) {
		var value string
		if err := json.Unmarshal(raw[key], &value); err != nil {
			return "", fmt.Errorf("mcp %s must be a string", key)
		}
		delete(raw, key)
		return value, nil
	})
	if err != nil {
		return err
	}
	rest, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var fields operationMCPFields
	dec := json.NewDecoder(bytes.NewReader(rest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fields); err != nil {
		return err
	}
	*m = OperationMCP{Invocation: invocation}
	m.setFields(fields)
	return nil
}

// invocationFromExtraKeys turns the keys of an mcp record that are not the
// record's own into its invocation policy: none, or one key whose value
// read returns.
func invocationFromExtraKeys(extra []string, read func(key string) (string, error)) (MCPInvocation, error) {
	switch len(extra) {
	case 0:
		return MCPInvocation{}, nil
	case 1:
	default:
		return MCPInvocation{}, fmt.Errorf("mcp record has unknown keys %q; only one invocation policy key may be added", extra)
	}
	key := extra[0]
	if err := ValidateMCPInvocationKey(key); err != nil {
		return MCPInvocation{}, fmt.Errorf("mcp record has unknown key %q: %w", key, err)
	}
	value, err := read(key)
	if err != nil {
		return MCPInvocation{}, err
	}
	return MCPInvocation{Key: key, Value: value}, nil
}

// MarshalYAML writes the same keys in the same order as MarshalJSON. The
// YAML encoder writes a struct's fields in order under their tags, and the
// policy's key is known only at run time, so the record is written as a
// struct type built for that key. The IR does not import a YAML package.
func (m OperationMCP) MarshalYAML() (any, error) {
	if err := m.encodable(); err != nil {
		return nil, err
	}
	if m.Invocation.Value != "" {
		if err := ValidateMCPInvocationKey(m.Invocation.Key); err != nil {
			return nil, fmt.Errorf("ir: %w", err)
		}
	}
	fields := []reflect.StructField{
		{Name: "Handle", Type: reflect.TypeFor[string](), Tag: `yaml:"handle,omitempty"`},
		{Name: "Hidden", Type: reflect.TypeFor[bool](), Tag: `yaml:"hidden"`},
		{Name: "HiddenReason", Type: reflect.TypeFor[string](), Tag: `yaml:"hiddenReason,omitempty"`},
	}
	values := []any{m.Handle, m.Hidden, m.HiddenReason}
	if m.Invocation.Value != "" {
		fields = append(fields, reflect.StructField{Name: "Invocation", Type: reflect.TypeFor[string](), Tag: reflect.StructTag(`yaml:"` + m.Invocation.Key + `"`)})
		values = append(values, m.Invocation.Value)
	}
	fields = append(fields,
		reflect.StructField{Name: "Meta", Type: reflect.TypeFor[map[string]any](), Tag: `yaml:"_meta,omitempty"`},
		reflect.StructField{Name: "Name", Type: reflect.TypeFor[string](), Tag: `yaml:"name,omitempty"`},
		reflect.StructField{Name: "Description", Type: reflect.TypeFor[string](), Tag: `yaml:"description,omitempty"`},
		reflect.StructField{Name: "Icon", Type: reflect.TypeFor[*MCPIcon](), Tag: `yaml:"icon,omitempty"`},
	)
	values = append(values, m.Meta, m.Name, m.Description, m.Icon)
	record := reflect.New(reflect.StructOf(fields)).Elem()
	for i, value := range values {
		record.Field(i).Set(reflect.ValueOf(value))
	}
	return record.Interface(), nil
}

// UnmarshalYAML reads the record the way UnmarshalJSON does. It takes the
// decoding function rather than a node, which the YAML decoder supports,
// so the IR does not import a YAML package.
func (m *OperationMCP) UnmarshalYAML(unmarshal func(any) error) error {
	var raw map[string]any
	if err := unmarshal(&raw); err != nil {
		return err
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("mcp record: %w", err)
	}
	return m.UnmarshalJSON(data)
}
