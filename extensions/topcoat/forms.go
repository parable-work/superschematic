package topcoat

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// form is a form over an operation's input type whose fields a form holds:
// a struct of the fields as the browser sends them, its parse into the
// input, and a component that renders them.
type form struct {
	Name      string // SignupInputForm
	TypeName  string // SignupInput
	Component string // signup_input_fields
	Parse     string // types::validators::parse_signup_input
	Fields    []formField
	// HasNumbers reports whether a field is read as a number, which the
	// parse refuses with that field's error when it is not one.
	HasNumbers bool
}

// formField is one field of a form.
type formField struct {
	Name     string // the struct's field: display_name
	JSONName string // the input's and the form's: displayName
	Label    string
	ID       string // signup-input-display-name
	// Control is the input's kind: text, email, url, tel, number,
	// checkbox, or select for an enum.
	Control string
	// Put is the form helper that writes the field into the input's JSON.
	Put string
	// Attrs are the input's static attributes from the input type's rules:
	// ` required=(true) minlength="2"`.
	Attrs   string
	Options []formOption
	// Hidden marks an @uiHidden field: in the struct and its parse, not in
	// the rendered fields.
	Hidden   bool
	Required bool
}

// formOption is one member of an enum: an option of a form's select, and
// the label a display component shows for its value.
type formOption struct {
	Value string
	Label string
}

// formsOf is a form per input type an operation of inProcess takes whose
// fields are all a form holds (a string, a number, a boolean or an enum,
// each alone), declared by the service itself, sorted by name. Any other
// input type gets none, and log says why. A form submits through the
// operation's in-process call, so an operation without one, a webhook's or
// one the service mounts, gives its input none, as it gives its result no
// record.
func formsOf(schemas schemaSet, inProcess []declared, log func(format string, args ...any)) ([]form, error) {
	seen := map[string]bool{}
	var forms []form
	for _, d := range inProcess {
		input := d.endpoint.Input
		if input == nil || seen[input.RustType] {
			continue
		}
		seen[input.RustType] = true
		name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(input.RustType, "Option<"), "types::"), ">")
		parse, ok := strings.CutPrefix(input.Parse, "types::validators::")
		if !ok {
			log("  - topcoat: no form for %s: the service's own types crate does not declare it\n", name)
			continue
		}
		typeDef := schemas.objectType(name)
		if typeDef == nil {
			continue
		}
		f, reason, err := schemas.form(typeDef, "types::validators::"+parse)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			log("  - topcoat: no form for %s: %s\n", name, reason)
			continue
		}
		forms = append(forms, f)
	}
	sort.Slice(forms, func(i, j int) bool { return forms[i].Name < forms[j].Name })
	return forms, nil
}

// form builds the form of typeDef, or the reason it has none.
func (s schemaSet) form(typeDef *ir.TypeDef, parse string) (form, string, error) {
	prefix := kebabCase(typeDef.Name)
	f := form{
		Name:      typeDef.Name + "Form",
		TypeName:  typeDef.Name,
		Component: registry.RustIdentifier(typeDef.Name, "input") + "_fields",
		Parse:     parse,
	}
	seen := map[string]string{}
	for _, field := range typeDef.Fields {
		ref := field.TypeRef
		if ref.IsArray || ref.IsMap {
			return form{}, fmt.Sprintf("field %s is a list or a map", field.Name), nil
		}
		if s.objectType(ref.Name) != nil {
			return form{}, fmt.Sprintf("field %s is an object", field.Name), nil
		}
		rustName := registry.RustIdentifier(field.Name, "value")
		if other, taken := seen[rustName]; taken {
			return form{}, "", fmt.Errorf("topcoat: input %s: fields %s and %s are both the form field %s", typeDef.Name, other, field.Name, rustName)
		}
		seen[rustName] = field.Name
		ff := formField{
			Name:     rustName,
			JSONName: field.Name,
			Label:    labelOf(field),
			ID:       prefix + "-" + kebabCase(field.Name),
			Hidden:   field.UIHidden,
			Required: field.Required && field.Default == nil,
		}
		if !s.control(&ff, field) {
			return form{}, fmt.Sprintf("field %s holds a value a form field does not (a union, or any JSON value)", field.Name), nil
		}
		f.HasNumbers = f.HasNumbers || ff.Put == "put_integer" || ff.Put == "put_number"
		f.Fields = append(f.Fields, ff)
	}
	return f, "", nil
}

// control sets the field's control, its form helper and its attributes
// from its type and rules; false for a type a form field cannot hold.
func (s schemaSet) control(ff *formField, field *ir.FieldDef) bool {
	ref := field.TypeRef
	var attrs []string
	if ff.Required {
		attrs = append(attrs, "required=(true)")
	}
	if enum := s.enum(ref.Name); enum != nil {
		ff.Control, ff.Put = "select", "put_string"
		ff.Options = enumOptions(enum)
		ff.Attrs = joinAttrs(attrs)
		return true
	}
	scalar := s.scalar(ref.Name)
	switch s.leafOf(ref.Name) {
	case stringLeaf:
		ff.Control, ff.Put = textControl(scalar), "put_string"
		minLength, maxLength := field.ValidateMinLength, field.ValidateMaxLength
		if scalar != nil {
			minLength = orInt(minLength, scalar.MinLength)
			maxLength = orInt(maxLength, scalar.MaxLength)
		}
		if minLength != nil {
			attrs = append(attrs, fmt.Sprintf("minlength=%q", strconv.Itoa(*minLength)))
		}
		if maxLength != nil {
			attrs = append(attrs, fmt.Sprintf("maxlength=%q", strconv.Itoa(*maxLength)))
		}
		pattern := field.ValidatePattern
		if pattern == "" && scalar != nil {
			pattern = scalar.Pattern
		}
		// An email or URL input checks its own syntax; the scalar's pattern
		// is checked when the form is parsed.
		if html, ok := htmlPattern(pattern); ok && ff.Control == "text" {
			attrs = append(attrs, "pattern="+rustString(html))
		}
	case integerLeaf, numberLeaf:
		ff.Control, ff.Put = "number", "put_number"
		step := "any"
		if s.leafOf(ref.Name) == integerLeaf {
			ff.Put, step = "put_integer", "1"
		}
		attrs = append(attrs, fmt.Sprintf("step=%q", step))
		minimum, maximum := field.ValidateMin, field.ValidateMax
		if scalar != nil {
			minimum = orFloat(minimum, scalar.Minimum)
			maximum = orFloat(maximum, scalar.Maximum)
		}
		if minimum != nil {
			attrs = append(attrs, fmt.Sprintf("min=%q", strconv.FormatFloat(*minimum, 'f', -1, 64)))
		}
		if maximum != nil {
			attrs = append(attrs, fmt.Sprintf("max=%q", strconv.FormatFloat(*maximum, 'f', -1, 64)))
		}
	case booleanLeaf:
		// A checkbox is sent only when checked, so it is never required.
		ff.Control, ff.Put, attrs = "checkbox", "put_boolean", nil
	default:
		return false
	}
	if field.Placeholder != "" && ff.Control != "checkbox" {
		attrs = append(attrs, "placeholder="+rustString(field.Placeholder))
	}
	ff.Attrs = joinAttrs(attrs)
	return true
}

// textControl is the input type of a string scalar: email or url by its
// format, or else its name, tel for a phone number, text otherwise.
func textControl(scalar *ir.ScalarDef) string {
	if scalar == nil {
		return "text"
	}
	switch strings.ToLower(scalar.Format) {
	case "email":
		return "email"
	case "uri", "url":
		return "url"
	}
	name := scalar.Name
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	switch strings.ToLower(name) {
	case "email":
		return "email"
	case "url", "uri":
		return "url"
	case "phonenumber", "phone":
		return "tel"
	}
	return "text"
}

// htmlPattern is a pattern rule as the pattern attribute of an input, which
// a browser reads anchored and with the v flag, and ok only when the browser
// reads it as the server does: no group syntax, no escape the v flag
// refuses outside a class, and in a class no character the v flag reserves
// unescaped nor a `-` that is not a range. Any other pattern is checked
// only when the form is parsed.
func htmlPattern(pattern string) (string, bool) {
	if pattern == "" || strings.Contains(pattern, "(?") {
		return "", false
	}
	const outsideEscapes = `dDwWsSbBntrfv0\/^$.*+?()[]{}|`
	const classEscapes = `dDwWsSntrfv\^$.*+?()[]{}|/-&!#%,:;<=>@` + "`~"
	inClass, classStart := false, false
	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' {
			if i+1 >= len(runes) {
				return "", false
			}
			next := runes[i+1]
			allowed := outsideEscapes
			if inClass {
				allowed = classEscapes
			}
			if !strings.ContainsRune(allowed, next) {
				return "", false
			}
			i++
			classStart = false
			continue
		}
		if !inClass {
			if r == '[' {
				inClass, classStart = true, true
				if i+1 < len(runes) && runes[i+1] == '^' {
					i++
				}
			}
			continue
		}
		switch {
		case r == ']' && !classStart:
			inClass = false
			continue
		case strings.ContainsRune("()[{}/|", r):
			return "", false
		case r == '-':
			// A range needs a character on each side.
			if classStart || i+1 >= len(runes) || runes[i+1] == ']' {
				return "", false
			}
		case i+1 < len(runes) && runes[i+1] == r && strings.ContainsRune("&!#$%*+,.:;<=>?@^`~", r):
			return "", false
		}
		classStart = false
	}
	if inClass {
		return "", false
	}
	return pattern, true
}

// kebabCase is a name in lower-case words joined by hyphens, for an id.
func kebabCase(name string) string {
	id := strings.TrimSuffix(strings.TrimPrefix(registry.RustIdentifier(name, "field"), "r#"), "_")
	return strings.ReplaceAll(id, "_", "-")
}

func joinAttrs(attrs []string) string {
	if len(attrs) == 0 {
		return ""
	}
	return " " + strings.Join(attrs, " ")
}

func orInt(field *int, scalar int) *int {
	if field != nil || scalar <= 0 {
		return field
	}
	return &scalar
}

func orFloat(field *float64, scalar *int64) *float64 {
	if field != nil || scalar == nil {
		return field
	}
	value := float64(*scalar)
	return &value
}
