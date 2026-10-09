package topcoat

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// form is a form over an operation's input type: the struct of its fields
// (formStruct) and the component that renders them.
type form struct {
	Name      string // BookingInputForm
	TypeName  string // BookingInput
	Component string // booking_input_fields
	Parse     string // types::validators::parse_booking_input
	// Rows reports whether the form holds a list rendered as rows, so a
	// row button submits it: it then has row_action and apply_action.
	Rows bool
	// Choosable reports whether a field takes the app's choices.
	Choosable bool
	// Visible reports whether the component renders a field.
	Visible bool
	// Body is the component's view, the fields rendered at their names.
	Body string
	// Struct is the input type's struct.
	Struct *formStruct
}

// formStruct is the struct of one object type's fields as a form holds
// them: an input type's, or one an input nests. A type has one struct
// wherever it appears.
type formStruct struct {
	Name     string // GuestForm
	TypeName string // Guest
	Fields   []*formField
	// Input marks an operation's input type: its struct decodes a post and
	// parses into the input.
	Input bool
	// Rows reports whether it or a struct it nests holds rows, so a row
	// button can act on it.
	Rows bool
	// Sent reports whether a form asks it whether any of its fields was
	// sent: it is an optional nested object, or nested in one.
	Sent bool
	// Shown marks a struct the component renders: an input's, or one a
	// rendered field nests. Only it says which errors it shows.
	Shown bool
	// IDBase is an input's ids' prefix, booking-input, and Parse its
	// generated parse.
	IDBase string
	Parse  string
}

// The kinds of field a form holds.
const (
	// kindValue is one control: an input, a select or a JSON textarea.
	kindValue = "value"
	// kindObject is a nested object, a fieldset of its own fields.
	kindObject = "object"
	// kindObjectRows is a list of objects, a fieldset per row.
	kindObjectRows = "objectRows"
	// kindValueRows is a list of single values, a control per row.
	kindValueRows = "valueRows"
	// kindGroup is a list of enum members, a checkbox per member.
	kindGroup = "group"
)

// formField is one field of a form struct.
type formField struct {
	Name     string // the struct's field: display_name
	JSONName string // the input's and the form's: displayName
	Label    string
	// Hint is the field's description, its first line, shown under its
	// label.
	Hint string
	Kind string
	// Hidden marks an @uiHidden field: in the struct and its parse, not in
	// the rendered fields.
	Hidden bool
	// Required reports whether the type requires the field and has no
	// default for it.
	Required bool

	// Control is a value's or a row's control: text, email, url, tel,
	// number, checkbox, select (an enum), password (a secret), date, time,
	// datetime-local, or textarea (JSON text).
	Control string
	// Read is the helper that reads the control's value as the input's
	// JSON: text, integer, number, boolean, date_time or json_text.
	Read string
	// Attrs are the control's static attributes from the field's rules,
	// `required` aside: ` minlength="2"`.
	Attrs string
	// Options are an enum's members: a select's options, or a group's
	// checkboxes.
	Options []formOption
	// Choosable marks a control the app's choices turn into a select.
	Choosable bool
	// Default is the Rust expression of the field's value in a new form.
	Default string

	// Child is a nested object's or an object row's struct.
	Child *formStruct
	// Min and Max bound a list's rows (listMin, listMax); Start is how
	// many a new form has.
	Min   int
	Max   *int
	Start int
	// RowLabel names a row, with its number: the row type's @display noun,
	// else the list's label. AddLabel is the add button's text.
	RowLabel string
	AddLabel string
}

// formOption is one member of an enum: an option of a form's select, and
// the label a display component shows for its value.
type formOption struct {
	Value string
	Label string
}

// formsOf is a form per input type an operation of inProcess takes and the
// service itself declares, sorted by name, and the structs of the types
// they hold. A type declared elsewhere gets none, and log says why. A form
// submits through the operation's in-process call, so an operation
// without one, a webhook's or one the service mounts, gives its input
// none, as it gives its result no record.
func formsOf(schemas schemaSet, inProcess []declared, log func(format string, args ...any)) ([]form, []*formStruct, error) {
	b := &formBuilder{schemaSet: schemas, structs: map[string]*formStruct{}}
	seen := map[string]bool{}
	var roots []*formStruct
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
		st, err := b.structOf(typeDef)
		if err != nil {
			return nil, nil, err
		}
		st.Input = true
		st.IDBase = kebabCase(typeDef.Name)
		st.Parse = "types::validators::" + parse
		st.markShown()
		roots = append(roots, st)
		forms = append(forms, form{
			Name:      st.Name,
			TypeName:  typeDef.Name,
			Component: registry.RustIdentifier(typeDef.Name, "input") + "_fields",
			Parse:     "types::validators::" + parse,
			Struct:    st,
		})
	}
	structs := b.sorted()
	for _, st := range structs {
		st.Rows = st.rows()
	}
	for _, st := range structs {
		for _, f := range st.Fields {
			if f.Kind == kindObject && !f.Required {
				f.Child.markSent()
			}
		}
	}
	for i, st := range roots {
		if st.Rows {
			for _, f := range st.Fields {
				if f.Name == "row_action" {
					return nil, nil, fmt.Errorf("topcoat: input %s: field %s is the form's row_action", st.TypeName, f.JSONName)
				}
			}
		}
		r := &formRenderer{}
		r.fields(scope{form: "form", idBase: st.IDBase}, st, 2)
		forms[i].Rows = st.Rows
		forms[i].Choosable = r.choosable
		forms[i].Visible = r.visible
		forms[i].Body = r.String()
	}
	sort.Slice(forms, func(i, j int) bool { return forms[i].Name < forms[j].Name })
	return forms, structs, nil
}

// formBuilder builds the struct of each object type a form holds.
type formBuilder struct {
	schemaSet
	structs map[string]*formStruct
}

func (b *formBuilder) sorted() []*formStruct {
	out := make([]*formStruct, 0, len(b.structs))
	for _, name := range sortedKeys(b.structs) {
		out = append(out, b.structs[name])
	}
	return out
}

// structOf is the struct of typeDef's fields, built once per type.
func (b *formBuilder) structOf(typeDef *ir.TypeDef) (*formStruct, error) {
	if st, ok := b.structs[typeDef.Name]; ok {
		return st, nil
	}
	st := &formStruct{Name: typeDef.Name + "Form", TypeName: typeDef.Name}
	b.structs[typeDef.Name] = st
	seen := map[string]string{}
	for _, field := range typeDef.Fields {
		rustName := registry.RustIdentifier(field.Name, "value")
		if other, taken := seen[rustName]; taken {
			return nil, fmt.Errorf("topcoat: input %s: fields %s and %s are both the form field %s", typeDef.Name, other, field.Name, rustName)
		}
		seen[rustName] = field.Name
		f := &formField{
			Name:     rustName,
			JSONName: field.Name,
			Label:    labelOf(field),
			Hint:     firstLine(field.Description),
			Hidden:   field.UIHidden,
			Required: field.Required && field.Default == nil,
		}
		if err := b.kindOf(f, typeDef, field); err != nil {
			return nil, err
		}
		st.Fields = append(st.Fields, f)
	}
	return st, nil
}

// kindOf sets how the form holds field of owner: a nested object or rows
// of them, rows of values, a group of an enum's members, or one control.
// A value a control cannot hold (a union, any JSON value, a map, a list of
// lists, a type that nests the type holding it) is its JSON text.
func (b *formBuilder) kindOf(f *formField, owner *ir.TypeDef, field *ir.FieldDef) error {
	ref := field.TypeRef
	if ref.IsMap || ref.IsArrayOfArrays || field.TemporalFormat != "" {
		b.jsonText(f, field)
		return nil
	}
	if object := b.objectType(ref.Name); object != nil {
		if b.reaches(ref.Name, owner.Name, map[string]bool{}) {
			b.jsonText(f, field)
			return nil
		}
		child, err := b.structOf(object)
		if err != nil {
			return err
		}
		f.Child = child
		if !ref.IsArray {
			f.Kind = kindObject
			return nil
		}
		f.Kind = kindObjectRows
		b.bound(f, field)
		f.RowLabel, f.AddLabel = f.Label, "Add"
		if display := object.Display; display != nil {
			if display.Noun != "" {
				f.RowLabel = display.Noun
			}
			if display.CreateLabel != "" {
				f.AddLabel = display.CreateLabel
			}
		}
		return nil
	}
	if enum := b.enum(ref.Name); enum != nil {
		f.Options = enumOptions(enum)
		if ref.IsArray {
			f.Kind = kindGroup
			return nil
		}
		f.Kind, f.Control, f.Read = kindValue, "select", "text"
		if field.Default != nil {
			for _, value := range enum.Values {
				if value.Name == *field.Default || value.SerializedAs == *field.Default {
					f.Default = "Some(" + rustString(enumOptions(&ir.EnumDef{Values: []ir.EnumValueDef{value}})[0].Value) + ".to_owned())"
				}
			}
		}
		return nil
	}
	if !b.control(f, field) {
		b.jsonText(f, field)
		return nil
	}
	if ref.IsArray {
		f.Kind = kindValueRows
		b.bound(f, field)
		f.RowLabel, f.AddLabel = f.Label, "Add"
		return nil
	}
	f.Kind = kindValue
	if !field.Secret {
		f.Default = defaultOf(field, f)
	}
	return nil
}

// jsonText makes f a textarea holding its value's JSON text.
func (b *formBuilder) jsonText(f *formField, field *ir.FieldDef) {
	f.Kind, f.Control, f.Read, f.Attrs, f.Choosable = kindValue, "textarea", "json_text", "", false
	f.Label = labelOf(field) + " (JSON)"
	f.Default = defaultOf(field, f)
}

// bound sets a list's bounds and the rows a new form starts with:
// listMin, or one for a required list.
func (b *formBuilder) bound(f *formField, field *ir.FieldDef) {
	if field.ValidateListMin != nil && *field.ValidateListMin > 0 {
		f.Min = *field.ValidateListMin
	}
	if field.ValidateListMax != nil {
		limit := *field.ValidateListMax
		f.Max = &limit
	}
	f.Start = f.Min
	if field.Required && f.Start == 0 {
		f.Start = 1
	}
	if f.Max != nil && f.Start > *f.Max {
		f.Start = *f.Max
	}
}

// reaches reports whether the object type from nests the type to, itself
// included, through the fields a form nests as objects or rows of them.
func (b *formBuilder) reaches(from, to string, visited map[string]bool) bool {
	if from == to {
		return true
	}
	if visited[from] {
		return false
	}
	visited[from] = true
	typeDef := b.objectType(from)
	if typeDef == nil {
		return false
	}
	for _, field := range typeDef.Fields {
		ref := field.TypeRef
		if ref.IsMap || ref.IsArrayOfArrays || field.TemporalFormat != "" || b.objectType(ref.Name) == nil {
			continue
		}
		if b.reaches(ref.Name, to, visited) {
			return true
		}
	}
	return false
}

// rows reports whether st, or a struct it nests, holds rows.
func (st *formStruct) rows() bool {
	for _, f := range st.Fields {
		switch f.Kind {
		case kindObjectRows, kindValueRows:
			return true
		case kindObject:
			if f.Child.rows() {
				return true
			}
		}
	}
	return false
}

// markSent marks st, and each object it nests, as asked whether a field was
// sent.
func (st *formStruct) markSent() {
	if st.Sent {
		return
	}
	st.Sent = true
	for _, f := range st.Fields {
		if f.Kind == kindObject {
			f.Child.markSent()
		}
	}
}

// markShown marks st, and each struct a field it renders nests, as
// rendered.
func (st *formStruct) markShown() {
	if st.Shown {
		return
	}
	st.Shown = true
	for _, f := range st.Fields {
		if f.Child != nil && !f.Hidden {
			f.Child.markShown()
		}
	}
}

// WritesErrors reports whether writing st's JSON names a control or adds
// an error: every field but a group does.
func (st *formStruct) WritesErrors() bool {
	for _, f := range st.Fields {
		if f.Kind != kindGroup {
			return true
		}
	}
	return false
}

// SentExpr is the Rust expression that is true when a field of st was
// sent: a value, a row, a group's member or a nested object's field.
func (st *formStruct) SentExpr() string {
	terms := make([]string, 0, len(st.Fields))
	for _, f := range st.Fields {
		switch f.Kind {
		case kindObject:
			terms = append(terms, "self."+f.Name+".is_sent()")
		case kindObjectRows, kindValueRows, kindGroup:
			terms = append(terms, "!self."+f.Name+".is_empty()")
		default:
			terms = append(terms, "self."+f.Name+".is_some()")
		}
	}
	return strings.Join(terms, " || ")
}

// Visible reports whether the component renders a field of st.
func (st *formStruct) Visible() bool {
	for _, f := range st.Fields {
		if !f.Hidden {
			return true
		}
	}
	return false
}

// IsValue and the other kind tests are for the template.
func (f *formField) IsValue() bool      { return f.Kind == kindValue }
func (f *formField) IsObject() bool     { return f.Kind == kindObject }
func (f *formField) IsObjectRows() bool { return f.Kind == kindObjectRows }
func (f *formField) IsValueRows() bool  { return f.Kind == kindValueRows }
func (f *formField) IsGroup() bool      { return f.Kind == kindGroup }

// RustType is the field's type in its struct.
func (f *formField) RustType() string {
	switch f.Kind {
	case kindObject:
		return f.Child.Name
	case kindObjectRows:
		return "Vec<" + f.Child.Name + ">"
	case kindValueRows:
		return "Vec<Option<String>>"
	case kindGroup:
		return "Vec<String>"
	}
	return "Option<String>"
}

// New is the field's value in a new form: its default, a required nested
// object's new form, and a list's starting rows. An optional nested object
// starts empty, since a value in it would send it.
func (f *formField) New() string {
	switch f.Kind {
	case kindObject:
		if f.Required {
			return f.Child.Name + "::new()"
		}
		return f.Child.Name + "::default()"
	case kindObjectRows:
		if f.Start == 0 {
			return "Vec::new()"
		}
		return fmt.Sprintf("vec![%s::new(); %d]", f.Child.Name, f.Start)
	case kindValueRows:
		if f.Start == 0 {
			return "Vec::new()"
		}
		return fmt.Sprintf("vec![None; %d]", f.Start)
	case kindGroup:
		return "Vec::new()"
	}
	if f.Default != "" {
		return f.Default
	}
	return "None"
}

// Bounds is the Rust `Bounds` of a list's rows.
func (f *formField) Bounds() string {
	limit := "None"
	if f.Max != nil {
		limit = fmt.Sprintf("Some(%d)", *f.Max)
	}
	return fmt.Sprintf("Bounds { min: %d, max: %s }", f.Min, limit)
}

// defaultOf is the Rust expression of field's @default as its control holds
// it, or empty: a string, a number or JSON text as written, and a true
// boolean as a checked checkbox. An enum's is its member's serialized
// value (kindOf).
func defaultOf(field *ir.FieldDef, f *formField) string {
	if field.Default == nil {
		return ""
	}
	text := *field.Default
	if f.Read != "boolean" {
		return "Some(" + rustString(text) + ".to_owned())"
	}
	if strings.EqualFold(strings.TrimSpace(text), "true") {
		return `Some("on".to_owned())`
	}
	return ""
}

// control sets a value's control, its reader and its attributes from its
// type and rules; false for a type no control holds but JSON text.
func (s schemaSet) control(f *formField, field *ir.FieldDef) bool {
	ref := field.TypeRef
	scalar := s.scalar(ref.Name)
	var attrs []string
	switch s.leafOf(ref.Name) {
	case stringLeaf:
		f.Read = "text"
		f.Control = textControl(scalar)
		switch {
		case field.Secret:
			f.Control = "password"
		case temporalControl(scalar) != "":
			f.Control = temporalControl(scalar)
			if f.Control == "datetime-local" {
				f.Read = "date_time"
				f.Label += " (UTC)"
			}
			// A date or a time input takes no length or pattern.
			f.Attrs = ""
			return true
		}
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
		if html, ok := htmlPattern(pattern); ok && (f.Control == "text" || f.Control == "password") {
			attrs = append(attrs, "pattern="+rustString(html))
		}
	case integerLeaf, numberLeaf:
		f.Control, f.Read = "number", "number"
		step := "any"
		if s.leafOf(ref.Name) == integerLeaf {
			f.Read, step = "integer", "1"
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
		f.Control, f.Read, f.Attrs = "checkbox", "boolean", ""
		return true
	default:
		return false
	}
	switch f.Control {
	case "text", "email", "url", "tel", "number":
		f.Choosable = !f.Hidden
	}
	if field.Placeholder != "" {
		attrs = append(attrs, "placeholder="+rustString(field.Placeholder))
	}
	f.Attrs = joinAttrs(attrs)
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

// temporalControl is the input type of a date, a time or a date-time
// scalar, by its format or its name, or empty for any other scalar.
func temporalControl(scalar *ir.ScalarDef) string {
	if scalar == nil {
		return ""
	}
	switch strings.ToLower(scalar.Format) {
	case "date":
		return "date"
	case "time":
		return "time"
	case "date-time":
		return "datetime-local"
	}
	switch scalar.Name {
	case "Temporal.Date":
		return "date"
	case "Temporal.Time":
		return "time"
	case ir.CanonicalDateTimeScalarName, ir.LegacyDateTimeScalarName:
		return "datetime-local"
	}
	return ""
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
