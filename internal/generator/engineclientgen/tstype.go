package engineclientgen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// refKey marks a JSON Schema node a narrowing replaced with a type of the
// module: {"$ts": "NoteState"} renders as NoteState. It never leaves the
// generator: the parity vector resolves it first.
const refKey = "$ts"

// jsonObjectType is the client's type of a JSON object whose members the
// schema does not name.
const jsonObjectType = "JSONObject"

var identifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// tsRenderer renders JSON Schemas as TypeScript type expressions and
// records whether one used JSONObject, which the module then imports.
type tsRenderer struct {
	usesJSONObject bool
}

// render renders a schema at an indent: the indent of the line the type
// starts on, which a multi-line object's closing brace returns to.
func (r *tsRenderer) render(schema any, indent string) string {
	switch s := schema.(type) {
	case bool:
		if s {
			return "unknown"
		}
		return "never"
	case map[string]any:
		return r.renderObjectSchema(s, indent)
	}
	return "unknown"
}

func (r *tsRenderer) renderObjectSchema(s map[string]any, indent string) string {
	if ref, ok := s[refKey].(string); ok {
		if nullable, _ := s["nullable"].(bool); nullable {
			return ref + " | null"
		}
		return ref
	}
	if value, ok := s["const"]; ok {
		return literal(value)
	}
	if values, ok := s["enum"].([]any); ok {
		parts := make([]string, 0, len(values))
		for _, value := range values {
			parts = append(parts, literal(value))
		}
		return union(parts)
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		members, ok := s[key].([]any)
		if !ok {
			continue
		}
		_, typed := s["type"]
		_, shaped := s["properties"]
		if !typed && !shaped {
			parts := make([]string, 0, len(members))
			for _, member := range members {
				parts = append(parts, r.render(member, indent))
			}
			return union(parts)
		}
		// Beside a type, the members constrain it: the value is the
		// schema and one of them. A member that only requires properties
		// makes them required.
		base := make(map[string]any, len(s))
		for k, v := range s {
			if k != key {
				base[k] = v
			}
		}
		parts := make([]string, 0, len(members))
		for _, member := range members {
			parts = append(parts, r.constraint(base, member, indent))
		}
		return r.render(base, indent) + " & (" + union(parts) + ")"
	}
	var types []string
	switch t := s["type"].(type) {
	case string:
		types = []string{t}
	case []any:
		for _, each := range t {
			if name, ok := each.(string); ok {
				types = append(types, name)
			}
		}
	}
	if len(types) == 0 {
		if _, ok := s["properties"]; ok {
			types = []string{"object"}
		} else {
			return "unknown"
		}
	}
	parts := make([]string, 0, len(types))
	for _, t := range types {
		switch t {
		case "string", "boolean", "null":
			parts = append(parts, t)
		case "integer", "number":
			parts = append(parts, "number")
		case "array":
			parts = append(parts, r.arrayOf(r.render(itemsOf(s), indent)))
		case "object":
			parts = append(parts, r.object(s, indent))
		default:
			parts = append(parts, "unknown")
		}
	}
	return union(parts)
}

// constraint renders one member of an anyOf or oneOf beside an object's
// own type: a member that only lists required properties is an object of
// those properties, typed as the base types them; any other is rendered
// on its own.
func (r *tsRenderer) constraint(base map[string]any, member any, indent string) string {
	m, ok := member.(map[string]any)
	if !ok {
		return r.render(member, indent)
	}
	for key := range m {
		if key != "required" && key != "description" {
			return r.render(member, indent)
		}
	}
	properties, _ := base["properties"].(map[string]any)
	required, _ := m["required"].([]any)
	picked := map[string]any{}
	for _, name := range required {
		if text, ok := name.(string); ok {
			if property, ok := properties[text]; ok {
				picked[text] = withoutDescription(property)
			} else {
				picked[text] = true
			}
		}
	}
	return r.render(map[string]any{"type": "object", "properties": picked, "required": required}, indent)
}

// itemsOf is an array schema's items; an array without them holds any JSON.
func itemsOf(s map[string]any) any {
	if items, ok := s["items"]; ok {
		return items
	}
	return true
}

// arrayOf writes T[], or Array<T> where T is not one token.
func (r *tsRenderer) arrayOf(element string) string {
	if identifier.MatchString(element) || strings.HasSuffix(element, "[]") && identifier.MatchString(strings.TrimRight(element, "[]")) {
		return element + "[]"
	}
	return "Array<" + element + ">"
}

// object writes an object schema: its properties, each optional unless
// required; an index signature when it has no properties and its
// additionalProperties is a schema; JSONObject when it says nothing of its
// members; and an empty record when it is closed with none.
func (r *tsRenderer) object(s map[string]any, indent string) string {
	properties, _ := s["properties"].(map[string]any)
	additional := s["additionalProperties"]
	if len(properties) == 0 {
		switch a := additional.(type) {
		case bool:
			if !a {
				return "Record<string, never>"
			}
		case map[string]any:
			return "{ [key: string]: " + r.render(a, indent) + " }"
		}
		r.usesJSONObject = true
		return jsonObjectType
	}
	required := map[string]bool{}
	if list, ok := s["required"].([]any); ok {
		for _, name := range list {
			if text, ok := name.(string); ok {
				required[text] = true
			}
		}
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	if inline, ok := r.inlineObject(properties, names, required, additional); ok {
		return inline
	}
	inner := indent + "  "
	var b strings.Builder
	b.WriteString("{\n")
	for _, name := range names {
		property := properties[name]
		if doc := descriptionOf(property); doc != "" {
			b.WriteString(inner + jsDoc(doc) + "\n")
		}
		optional := "?"
		if required[name] {
			optional = ""
		}
		fmt.Fprintf(&b, "%s%s%s: %s;\n", inner, propertyKey(name), optional, r.render(property, inner))
	}
	// Members beside the named ones hold what additionalProperties allows,
	// which an index signature can only say as unknown, the named ones'
	// types being part of it.
	if _, ok := additional.(map[string]any); ok {
		b.WriteString(inner + "[key: string]: unknown;\n")
	}
	b.WriteString(indent + "}")
	return b.String()
}

// inlineWidth bounds an object type written on one line.
const inlineWidth = 72

// inlineObject writes a small object on one line, { over: true }, when no
// property has a doc comment, each property's type fits on one line and
// the whole fits inlineWidth; ok is false otherwise.
func (r *tsRenderer) inlineObject(properties map[string]any, names []string, required map[string]bool, additional any) (string, bool) {
	if _, ok := additional.(map[string]any); ok {
		return "", false
	}
	members := make([]string, 0, len(names))
	for _, name := range names {
		if descriptionOf(properties[name]) != "" {
			return "", false
		}
		probe := tsRenderer{}
		rendered := probe.render(properties[name], "")
		if strings.Contains(rendered, "\n") {
			return "", false
		}
		r.usesJSONObject = r.usesJSONObject || probe.usesJSONObject
		optional := "?"
		if required[name] {
			optional = ""
		}
		members = append(members, propertyKey(name)+optional+": "+rendered)
	}
	text := "{ " + strings.Join(members, "; ") + " }"
	if len(text) > inlineWidth {
		return "", false
	}
	return text, true
}

// descriptionOf is a schema's description, or "".
func descriptionOf(schema any) string {
	if s, ok := schema.(map[string]any); ok {
		if text, ok := s["description"].(string); ok {
			return text
		}
	}
	return ""
}

// union joins the members of a union, each once, in order.
func union(parts []string) string {
	seen := map[string]bool{}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	if len(out) == 0 {
		return "never"
	}
	return strings.Join(out, " | ")
}

// literal writes a JSON value as a TypeScript literal type: a string in
// single quotes, a number, a boolean or null.
func literal(value any) string {
	switch v := value.(type) {
	case string:
		return quote(v)
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(v)
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "unknown"
	}
	return string(encoded)
}

// quote writes a single-quoted TypeScript string literal.
func quote(text string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range text {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString(`\'`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// propertyKey writes a property name, quoted when it is not an identifier.
func propertyKey(name string) string {
	if identifier.MatchString(name) {
		return name
	}
	return quote(name)
}

// jsDoc writes a one-line doc comment, with a closing sequence in the text
// broken so it cannot end the comment, and its whitespace folded.
func jsDoc(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	text = strings.ReplaceAll(text, "*/", "* /")
	return "/** " + text + " */"
}
