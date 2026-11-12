package topcoat

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// argForm is a form over an operation's arguments: a struct of each
// argument as the browser sends it, its parse into the operation's Args
// struct, a submit that makes the in-process call, and a component that
// renders the arguments' controls. An operation with an input embeds the
// input's form, whose fields are named beside the arguments, so one form,
// one parse and one component cover every argument.
type argForm struct {
	Name      string // OrderCancelOrderArgsForm
	ArgsName  string // OrderCancelOrderArgs, the API crate's
	Namespace string
	Operation string
	Component string // order_cancel_order_args_fields
	Call      string // order_cancel_order, the in-process call
	Output    string // the call's result type
	// Method is the form's method: get for a GET operation, whose form is a
	// filter read from the query, post for any other.
	Method string
	// IDBase prefixes the ids of the form's controls: order-cancel-order.
	IDBase string
	Fields []*argField
	// Input is the input's form, which the argument form embeds; nil for an
	// operation without an input.
	Input *argInput
	// Paged marks a @paginated operation's form (D60): it holds the page
	// asked for, `cursor` and `limit`, as the query sends them, renders no
	// control for either, and reads them by the route's rules. Sort is a
	// sorted one's sort (D60, amended), which it holds the same way, nil
	// for an unsorted one.
	Paged bool
	Sort  *argSort
	// Filter is the Args' field of the operation's FilterBy (D106), empty
	// without one, FilterType the API crate's struct it holds, and Filters
	// a control per field the list filters by, after the arguments': the
	// form holds what each sent, reads it back from the query's bracket
	// form, and its parse builds the typed filter of them.
	Filter     string
	FilterType string
	Filters    []*filterControl
	// ServiceOnly marks the form of an operation whose route admits only a
	// service caller (@requireService): it has `submit_as(cx, service)`,
	// which calls it through operations::as_service, in place of
	// `submit(cx)` (D37, amended).
	ServiceOnly bool
	// Choosable reports whether a control takes the app's choices,
	// UsesForm whether the component's view reads the form, and Body is
	// the view.
	Choosable bool
	UsesForm  bool
	Body      string
	// Picks reports whether a control, an argument's or its input's, is a
	// search picker (D61, amended): the form holds every picker's text in
	// finds, and a post's "Find" acts on it as a row button does.
	Picks bool
}

// Hidden reports whether the component renders a hidden submit button
// ahead of every other, which Enter presses: ahead of its input's row
// buttons, or of a picker's "Find".
func (f argForm) Hidden() bool { return f.Rows() || f.Picks }

// Acts reports whether a button other than the submit acts on the form, so
// it has apply_action: its input's row buttons, or a post's "Find". A
// filter's "Find" sends its query.
func (f argForm) Acts() bool { return f.Rows() || f.Picks && !f.IsGet() }

// InputActs reports whether its input's form has apply_action, which the
// argument form's applies, the input reading the button pressed.
func (f argForm) InputActs() bool { return f.Input != nil && f.Input.Struct.Acts() }

// OwnAction reports whether the form reads the button pressed itself,
// row_action: a post whose "Find" acts on it, and whose input does not
// read it.
func (f argForm) OwnAction() bool { return f.Acts() && !f.InputActs() }

// filterControl is one field of a filtered list's FilterBy in its argument
// form (D106): a control named for the field, which writes one operator
// of it in the bracket form, `filter[field]=value` or
// `filter[field][operator]=value`. A field of an enum is a group of its
// members' checkboxes, written as `in`; a field of text is a text box,
// written as `contains`; and any other is its type's control, a boolean's
// a select of Yes and No, written as equality. The form has no control for
// a range or for isNull.
type filterControl struct {
	// The control, as an argument's of the field's type is: Name is the
	// form's member (filter_status) and JSONName where its errors are
	// keyed, the route's parameter, filter[status].
	*formField
	// Field is the item's field, by its JSON name, and Member the typed
	// filter's member of it.
	Field  string
	Member string
	// Operator is the operator the control writes: in, contains or eq.
	Operator string
	// Shape is the runtime's type of the field's filter: Equals, Ordered
	// or Text, and ValueType the Rust type of one value of the field, as
	// the crate names it: crate::api::types::OrderStatus.
	Shape     string
	ValueType string
}

// The operators a filter's control writes.
const (
	filterIn       = "in"
	filterContains = "contains"
	filterEq       = "eq"
)

// ControlName is the control's name, the query parameter it sends:
// filter[status][in], filter[name][contains], or filter[quantity] for
// equality.
func (c *filterControl) ControlName() string {
	if c.Operator == filterEq {
		return c.JSONName
	}
	return c.JSONName + "[" + c.Operator + "]"
}

// IsGroup reports whether the control is a group of choices, whose member
// holds each one chosen.
func (c *filterControl) IsGroup() bool { return c.Operator == filterIn }

// Decoded reports whether the parse decodes what the control sent into
// the field's type: every control's but a text box's, whose text is the
// filter's as it is.
func (c *filterControl) Decoded() bool { return c.Operator != filterContains }

// Says is what the member holds, for its doc comment.
func (c *filterControl) Says() string {
	switch c.Operator {
	case filterIn:
		return "the values chosen, any one of them"
	case filterContains:
		return "text it holds, in any case"
	}
	return "the value it equals"
}

// PostedExpr reads what a query holds of the field, under `filter`: an
// in's values and an equality as one more (filter_choices), a contains's
// text, or an equality's value, sent either way (filter_equal).
func (c *filterControl) PostedExpr() string {
	field := `posted.get("filter").get(` + rustString(c.Field) + ")"
	switch c.Operator {
	case filterIn:
		return "filter_choices(" + field + ")"
	case filterContains:
		return field + `.get("contains").first()`
	}
	return "filter_equal(" + field + ")"
}

// ReadExpr is the parse's read of a decoded control: its values, or its
// value, as the JSON a request carries, decoded into the field's type,
// none sent as none.
func (c *filterControl) ReadExpr() string {
	key := rustString(c.JSONName)
	if c.IsGroup() {
		return fmt.Sprintf("list::<Option<Vec<%s>>>(&mut errors, %s, &self.%s, %s)", c.ValueType, key, c.Name, c.Read)
	}
	return fmt.Sprintf("single::<Option<%s>>(&mut errors, %s, %s(self.%s.as_deref()))", c.ValueType, key, c.Read, c.Name)
}

// MemberExpr is the typed filter's member of the field: the runtime's
// filter of its shape with the control's operator set, or None when the
// control sent nothing.
func (c *filterControl) MemberExpr() string {
	shape := "crate::api::runtime::filter::" + c.Shape
	switch c.Operator {
	case filterIn:
		return c.Name + ".map(|in_| " + shape + " { in_, ..Default::default() })"
	case filterContains:
		return "self." + c.Name + ".clone().map(|contains| " + shape + " { contains: Some(contains), ..Default::default() })"
	}
	return c.Name + ".map(|eq| " + shape + " { eq: Some(eq), ..Default::default() })"
}

// FilterNames are the fields the form filters by, as the alternatives of
// a Rust pattern: "status" | "placedAt".
func (f argForm) FilterNames() string {
	names := make([]string, len(f.Filters))
	for i, c := range f.Filters {
		names[i] = rustString(c.Field)
	}
	return strings.Join(names, " | ")
}

// argSort is the sort of a sorted operation's form (D60, amended): the
// query parameter its SortBy argument names, and the form's field, which
// holds it as sent.
type argSort struct {
	Param string // sort
	Field string // sort
}

// argInput is the input form an argument form embeds.
type argInput struct {
	Struct   *formStruct
	Optional bool // the Args struct holds the input in an Option
}

// argField is one argument of an argument form: a form field, by the
// rules an input type's field of the same type gets, and where the
// argument comes from.
type argField struct {
	*formField
	// Local is the parse's local: arg_product_id.
	Local string
	// Split marks a query list, whose values the router splits on commas.
	Split bool
	// Default is the argument's declared default, which a blank field
	// reads as the router reads an absent parameter; HasDefault reports
	// whether it has one.
	Default    string
	HasDefault bool
	// Search marks the operation's search text (D62): a search box, the
	// form's first control.
	Search bool
}

// The kinds of argument besides an input form's (kindValue, kindGroup).
const (
	// kindPath is a path argument, a hidden input the page fills.
	kindPath = "path"
	// kindRepeated is a list of values, an input per value sent and one
	// more, all of the argument's name.
	kindRepeated = "repeated"
)

// IsGet reports whether the form is a filter, sent by GET.
func (f argForm) IsGet() bool { return f.Method == "get" }

// Shown reports whether the component renders a control: an argument's
// that its path does not carry, or the input's fields. A paged list's
// form without a filter holds its page alone, and a page renders no form
// for it.
func (f argForm) Shown() bool { return f.ShownArgs() || f.Input != nil }

// HasSearch reports whether an argument is the operation's search text
// (D62), which a page's filter form marks as a search.
func (f argForm) HasSearch() bool {
	for _, field := range f.Fields {
		if field.Search {
			return true
		}
	}
	return false
}

// QueryFields are the arguments a filter's query carries, in control
// order: every argument its path does not carry. A paged list's links
// carry them (query_pairs).
func (f argForm) QueryFields() []*argField {
	var out []*argField
	for _, field := range f.Fields {
		if field.Kind != kindPath {
			out = append(out, field)
		}
	}
	return out
}

// IsPath and IsRepeated are kind tests for the template.
func (f *argField) IsPath() bool     { return f.Kind == kindPath }
func (f *argField) IsRepeated() bool { return f.Kind == kindRepeated }

// IsList reports whether the field holds a list's values.
func (f *argField) IsList() bool { return f.Kind == kindRepeated || f.Kind == kindGroup }

// PathFields are the arguments the operation's path carries, which the
// form holds as hidden inputs a page fills (`new`).
func (f argForm) PathFields() []*argField {
	var out []*argField
	for _, field := range f.Fields {
		if field.Kind == kindPath {
			out = append(out, field)
		}
	}
	return out
}

// ShownArgs reports whether the component renders an argument's control,
// or a filter's (D106): a path argument's is hidden.
func (f argForm) ShownArgs() bool {
	return len(f.PathFields()) < len(f.Fields) || len(f.Filters) > 0
}

// Rows reports whether the embedded input holds rows: the form then
// applies its row buttons (apply_action).
func (f argForm) Rows() bool { return f.Input != nil && f.Input.Struct.Rows }

// FormsUseTypes reports whether forms.rs names the types module: an input
// form parses into its type, and an argument form's call returns one.
func (c *crate) FormsUseTypes() bool {
	if len(c.Forms) > 0 {
		return true
	}
	for _, f := range c.ArgForms {
		if strings.Contains(f.Output, "types::") {
			return true
		}
	}
	return false
}

// argFormsOf is an argument form per operation of inProcess that a form
// submits, sorted by name: a GET operation with a query argument, whose
// form is a filter (a GET whose arguments its path carries alone is a
// link), a paged one (D60), whose page its query carries, one with a
// FilterBy argument (D106), whose filters its query carries, and any other
// operation with an argument beside its input (one with only a path
// argument is a button). inputs are the input forms' structs by type: an
// operation whose input has none gets no argument form, nor does one with
// an argument named as one of its input's fields, and log says why.
func argFormsOf(schemas schemaSet, inProcess []declared, inputs map[string]*formStruct, log func(format string, args ...any)) ([]argForm, error) {
	var forms []argForm
	for _, d := range inProcess {
		e := d.endpoint
		// A page and a filter are each read from the query, as a query
		// argument is.
		paged := e.Page != nil || e.Filter != nil
		if len(e.PathArgs)+len(e.QueryArgs)+len(e.BodyArgs) == 0 && !paged {
			continue
		}
		if strings.EqualFold(e.Method, "GET") && len(e.QueryArgs) == 0 && !paged {
			log("  - topcoat: no argument form for %s.%s: a GET whose arguments its path carries is a link\n", e.Namespace, e.Name)
			continue
		}
		f, reason, err := schemas.argForm(e, d.op, inputs)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			log("  - topcoat: no argument form for %s.%s: %s\n", e.Namespace, e.Name, reason)
			continue
		}
		forms = append(forms, f)
	}
	sort.Slice(forms, func(i, j int) bool { return forms[i].Name < forms[j].Name })
	return forms, nil
}

// argForm builds the argument form of the operation e serves, or the
// reason it has none.
func (s schemaSet) argForm(e registry.RustEndpoint, op *ir.FieldDef, inputs map[string]*formStruct) (argForm, string, error) {
	method := "post"
	if strings.EqualFold(e.Method, "GET") {
		method = "get"
	}
	o := operationOf(e)
	f := argForm{
		Name:      e.ArgsName + "Form",
		ArgsName:  e.ArgsName,
		Namespace: e.Namespace,
		Operation: e.Name,
		Component: e.SnakeName() + "_args_fields",
		Call:      e.SnakeName(),
		Output:    o.Output,
		Method:    method,
		IDBase:    kebabCase(e.Namespace) + "-" + kebabCase(e.Name),
		Paged:     e.Page != nil,
		Filter:    o.Filter,
		// The form of a @requireService operation submits as a service.
		ServiceOnly: o.ServiceOnly,
	}
	if e.Page != nil && e.Page.Sort != nil {
		f.Sort = &argSort{Param: e.Page.Sort.Param, Field: registry.RustIdentifier(e.Page.Sort.Param, "sort")}
	}
	// The input's fields and the arguments share the form's names.
	names := map[string]string{}
	if input := e.Input; input != nil {
		name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(input.RustType, "Option<"), "types::"), ">")
		st, ok := inputs[name]
		if !ok {
			return argForm{}, fmt.Sprintf("its input %s has no form", name), nil
		}
		f.Input = &argInput{Struct: st, Optional: !input.Required}
		for _, field := range st.Fields {
			names[field.JSONName] = "its input's field " + field.JSONName
		}
		if f.Sort != nil {
			if other, taken := names[f.Sort.Param]; taken {
				return argForm{}, fmt.Sprintf("its sort, %s, and %s share a form field's name", f.Sort.Param, other), nil
			}
		}
		if st.Rows {
			names[rowActionName] = "the name of its input's row buttons"
		}
		// An optional input is part of the arguments only when one of its
		// fields was sent, which its form says.
		if f.Input.Optional {
			st.markSent()
		}
	}
	args := map[string]*ir.ArgumentDef{}
	for _, arg := range op.Arguments {
		args[arg.Name] = arg
	}
	for _, place := range []struct {
		location string
		params   []registry.RustParam
	}{{"path", e.PathArgs}, {"query", e.QueryArgs}, {"body", e.BodyArgs}} {
		for _, param := range place.params {
			arg := args[param.Name]
			if arg == nil {
				return argForm{}, "", fmt.Errorf("topcoat: argument form of %s.%s: argument %s is not among the operation's", e.Namespace, e.Name, param.Name)
			}
			if other, taken := names[arg.Name]; taken {
				return argForm{}, fmt.Sprintf("argument %s and %s share a form field's name", arg.Name, other), nil
			}
			if param.Field == "overflow" {
				return argForm{}, fmt.Sprintf("argument %s is the form's overflow", arg.Name), nil
			}
			names[arg.Name] = "argument " + arg.Name
			f.Fields = append(f.Fields, s.argField(arg, param, place.location, method == "get"))
		}
	}
	// The search text is the form's first control (D62).
	sort.SliceStable(f.Fields, func(i, j int) bool { return f.Fields[i].Search && !f.Fields[j].Search })
	if e.Filter != nil {
		if reason, err := s.filterControls(&f, e, op, names); err != nil || reason != "" {
			return argForm{}, reason, err
		}
	}
	f.render(nil)
	return f, "", nil
}

// filterControls adds a control per field of the operation's FilterBy to
// its form (D106), or says why the form cannot hold them: an argument, or
// an input's field, is named filter, the query parameter every filter
// travels under, or one of the form's members is named as a filter's.
func (s schemaSet) filterControls(f *argForm, e registry.RustEndpoint, op *ir.FieldDef, names map[string]string) (string, error) {
	if other, taken := names[ir.FilterParam]; taken {
		return fmt.Sprintf("its filter and %s share a form field's name", other), nil
	}
	if op.Filter == nil || len(op.Filter.Fields) != len(e.Filter.Fields) {
		return "", fmt.Errorf("topcoat: argument form of %s.%s: its filter is not the operation's", e.Namespace, e.Name)
	}
	members := map[string]string{"cursor": "its page's cursor", "limit": "its page's limit", "input": "its input", "finds": "its pickers' texts", "row_action": "its row buttons", "overflow": "its overflow"}
	for _, field := range f.Fields {
		members[field.Name] = "argument " + field.JSONName
	}
	if f.Sort != nil {
		members[f.Sort.Field] = "its sort"
	}
	f.FilterType = e.Filter.TypeName
	for i, typed := range e.Filter.Fields {
		field := op.Filter.Fields[i]
		if field == nil || field.Name != typed.Name || field.TypeRef == nil {
			return "", fmt.Errorf("topcoat: argument form of %s.%s: filter field %s is not the operation's", e.Namespace, e.Name, typed.Name)
		}
		c := s.filterControl(field, typed.Field, typed.Shape, typed.Value.RustType)
		if other, taken := members[c.Name]; taken {
			return fmt.Sprintf("its filter of %s and %s share a form member's name, %s", field.Name, other, c.Name), nil
		}
		members[c.Name] = "its filter of " + field.Name
		f.Filters = append(f.Filters, c)
	}
	return "", nil
}

// filterControl builds the control of one field of a filter (D106). Its
// control and attributes are those a query argument of the field's type
// gets (argField), a list of it for an enum, whose members are a group of
// checkboxes, so a boolean is a select of Yes and No, a number a number
// input and a date a date input. A field of text is a plain text box:
// what it sends is text the field holds, which the field's own rules do
// not bind. No control takes the app's choices. member is the typed
// filter's member of the field, shape the runtime's type of its filter and
// rustType the Rust type of one value of it.
func (s schemaSet) filterControl(field *ir.FilterField, member, shape, rustType string) *filterControl {
	c := &filterControl{Field: field.Name, Member: member, Operator: filterEq, Shape: shape, ValueType: strings.ReplaceAll(rustType, "types::", "crate::api::types::")}
	ref := ir.TypeRef{Name: field.TypeRef.Name}
	if field.Kind == ir.FilterKindEnum && s.enum(ref.Name) != nil {
		c.Operator, ref.IsArray = filterIn, true
	}
	arg := &ir.ArgumentDef{Name: field.Name, TypeRef: ref}
	param := registry.RustParam{Name: field.Name, Field: "filter_" + strings.TrimPrefix(member, "r#"), RustType: "Option<" + rustType + ">"}
	c.formField = s.argField(arg, param, "query", true).formField
	c.JSONName, c.Hint, c.Choosable = ir.FilterParam+"["+field.Name+"]", "", false
	plain := func() {
		c.Kind, c.Control, c.Read, c.Attrs, c.Options = kindValue, "text", "text", "", nil
		c.Label = humanize(field.Name)
	}
	switch {
	case field.Kind == ir.FilterKindString:
		c.Operator = filterContains
		plain()
	case c.Control == "textarea" || c.Kind != kindValue && c.Kind != kindGroup:
		// A type no control holds is its text, compared for equality.
		c.Operator = filterEq
		plain()
	}
	return c
}

// render writes the form's view, picks the search pickers by the paths
// of their controls (D61, amended).
func (f *argForm) render(picks map[string]pickRender) {
	r := newFormRenderer()
	r.picks = picks
	r.arguments(*f, 2)
	f.Choosable, f.UsesForm, f.Body = r.choosable, r.used["form"], r.String()
}

// argField builds the field of one argument. Its control and attributes
// are those an input type's field of the same type and rules gets
// (schemaSet.control); a list of an enum is a group of checkboxes, any
// other list of values an input per value; a path argument is a hidden
// input; and a value no control holds (a map, a list of lists, an object,
// a list of booleans, a union, any JSON value) is its JSON text. A
// boolean is a checkbox, as an input's is, but in a filter, whose
// boolean is a select of true and false (booleanSelect).
func (s schemaSet) argField(arg *ir.ArgumentDef, param registry.RustParam, location string, filter bool) *argField {
	ref := arg.TypeRef
	optional := strings.HasPrefix(param.RustType, "Option<")
	f := &formField{
		Name:     param.Field,
		JSONName: arg.Name,
		Label:    humanize(arg.Name),
		Hint:     firstLine(arg.Description),
		Required: !optional && arg.Default == nil && !ref.IsArray,
	}
	af := &argField{
		formField: f,
		Local:     "arg_" + strings.TrimPrefix(param.Field, "r#"),
		Split:     ref.IsArray && location == "query",
	}
	if arg.Default != nil {
		af.Default, af.HasDefault = *arg.Default, true
	}
	// A list's control is its element's: no list rule has an attribute,
	// and an element is never required.
	field := &ir.FieldDef{
		Name:              arg.Name,
		TypeRef:           ir.TypeRef{Name: ref.Name},
		Required:          f.Required,
		ValidateMin:       arg.ValidateMin,
		ValidateMax:       arg.ValidateMax,
		ValidateMinLength: arg.ValidateMinLength,
		ValidateMaxLength: arg.ValidateMaxLength,
		ValidatePattern:   arg.ValidatePattern,
	}
	enum := s.enum(ref.Name)
	switch {
	case ref.IsMap || ref.IsArrayOfArrays || s.objectType(ref.Name) != nil:
		af.jsonText()
	case enum != nil:
		f.Options, f.enum = enumOptions(enum), enum
		f.Kind, f.Control, f.Read = kindValue, "select", "text"
		if ref.IsArray {
			f.Kind = kindGroup
		}
	case !s.control(f, field):
		af.jsonText()
	case ref.IsArray && f.Control == "checkbox":
		af.jsonText()
	case ref.IsArray:
		f.Kind = kindRepeated
	default:
		f.Kind = kindValue
	}
	boolean := f.Control == "checkbox"
	if boolean && af.HasDefault {
		af.Default = strconv.FormatBool(strings.EqualFold(strings.TrimSpace(af.Default), "true"))
	}
	switch {
	case location == "path":
		f.Kind, f.Control, f.Attrs, f.Hint, f.Choosable = kindPath, "hidden", "", "", false
		if boolean {
			// A path's boolean is true or false, as the page fills it.
			f.Read = "yes_no"
		}
	case boolean && filter:
		af.booleanSelect()
	case arg.Search && f.Kind == kindValue && f.Read == "text":
		// The search text is a search box labeled Search, with its rules'
		// attributes, which no choices make a select (D62).
		af.Search = true
		f.Control, f.Label, f.Choosable = "search", "Search", false
	}
	return af
}

// booleanSelect makes a filter's boolean a select of true and false, after
// a blank option when the filter may leave it out, which reads its
// declared default or filters nothing. A checkbox sends nothing when it is
// not checked, which a filter could not tell from leaving the flag out:
// false could not be filtered on, and a flag whose default is true could
// not be turned off.
func (af *argField) booleanSelect() {
	f := af.formField
	f.Control, f.Read = "select", "yes_no"
	f.Options = []formOption{{Value: "true", Label: "Yes"}, {Value: "false", Label: "No"}}
}

// jsonText makes the argument a textarea of its JSON text.
func (af *argField) jsonText() {
	f := af.formField
	f.Kind, f.Control, f.Read, f.Attrs, f.Options, f.Choosable = kindValue, "textarea", "json_text", "", nil, false
	f.Label += " (JSON)"
	af.Split = false
}

// ReadExpr is the parse's read of the field as its argument's JSON: a
// list's values, each by its reader; else the single value, a blank one
// its declared default. A checkbox has no blank value: it sends nothing
// when it is not checked, which is false, so its default is only what a
// new form shows (NewValue).
func (af *argField) ReadExpr() string {
	key := rustString(af.JSONName)
	if af.IsList() {
		return fmt.Sprintf("list(&mut errors, %s, &self.%s, %s)", key, af.Name, af.Read)
	}
	value := "self." + af.Name + ".as_deref()"
	if af.HasDefault && af.Control != "checkbox" {
		value += ".filter(|value| !value.is_empty()).or(Some(" + rustString(af.Default) + "))"
	}
	return fmt.Sprintf("single(&mut errors, %s, %s(%s))", key, af.Read, value)
}

// NewValue is the field's value in a new form: its default, or nothing; a
// checkbox is checked for a default of true, as an input's is.
func (af *argField) NewValue() string {
	switch {
	case af.IsList():
		return "Vec::new()"
	case af.Kind == kindPath || !af.HasDefault:
		return "None"
	case af.Control == "checkbox":
		if af.Default == "true" {
			return `Some("on".to_owned())`
		}
		return "None"
	}
	return "Some(" + rustString(af.Default) + ".to_owned())"
}

// arguments writes the view of an argument form: each argument's control,
// a path argument's hidden input, then the embedded input's fields.
func (r *formRenderer) arguments(f argForm, indent int) {
	sc := scope{form: "form", idBase: f.IDBase}
	for _, a := range f.Fields {
		r.visible = true
		path := []pathSegment{{key: a.JSONName}}
		switch a.Kind {
		case kindPath:
			r.use("form")
			r.line(indent, `<input type="hidden" name=`+rustString(a.JSONName)+" value=(form."+a.Name+".clone())>")
		case kindValue:
			r.value(sc, a.formField, path, indent)
		case kindGroup:
			r.group(sc, a.formField, path, indent)
		case kindRepeated:
			r.repeated(sc, a.formField, path, indent)
		}
	}
	for _, c := range f.Filters {
		r.visible = true
		r.filter(sc, c, indent)
	}
	if f.Input != nil {
		r.fields(scope{form: "form.input", idBase: f.IDBase}, f.Input.Struct, indent)
	}
}

// filter writes the control of one field of a filter (D106): a group of
// an enum's members, or one control with its label, named as the query
// parameter it sends, its errors those at the route's parameter of the
// field, filter[field], whichever operator the control writes.
func (r *formRenderer) filter(sc scope, c *filterControl, indent int) {
	value := sc.form + "." + c.Name
	sent, at := rustString(c.ControlName()), rustString(c.JSONName)
	if c.IsGroup() {
		r.use(value)
		r.line(indent, `<fieldset class="ss-choices" data-field=`+at+">")
		r.line(indent+1, "<legend>"+rustString(c.Label)+"</legend>")
		r.errors(indent+1, true, at)
		for _, option := range c.Options {
			r.line(indent+1, `<label class="ss-choice"><input type="checkbox" name=`+sent+" value="+rustString(option.Value)+" checked=(checked(&"+value+", "+rustString(option.Value)+"))>"+rustString(option.Label)+"</label>")
		}
		r.line(indent, "</fieldset>")
		return
	}
	id := rustString(controlID(sc.idBase, []pathSegment{{key: ir.FilterParam}, {key: c.Field}}))
	r.line(indent, `<div class="field">`)
	r.line(indent+1, "<label for="+id+">"+rustString(c.Label)+"</label>")
	r.control(indent+1, c.formField, value, name{attr: sent, arg: at}, name{attr: id, arg: id}, false, "", "")
	r.errors(indent+1, false, at)
	r.line(indent, "</div>")
}
