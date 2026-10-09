package topcoat

import (
	"fmt"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// argForm is a form over an operation's arguments other than its input:
// a struct of each argument as the browser sends it, its parse into the
// operation's Args struct, and a component that renders the arguments'
// controls. An operation with an input embeds the input's form, so one
// form, one parse and one component cover every argument.
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
	Fields []argField
	// Input is the input's form, which the argument form embeds; nil for an
	// operation without an input.
	Input *argInput
}

// argInput is the input form an argument form embeds.
type argInput struct {
	Form      string // WriteReviewInputForm
	Component string // write_review_input_fields
	Optional  bool   // the Args struct holds the input in an Option
}

// argField is one argument of an argument form.
type argField struct {
	Name     string // the form's field and the Args struct's: product_id
	Local    string // the parse's local: arg_product_id
	JSONName string // the argument's name, as a request and a refusal name it
	Label    string
	Hint     string // the argument's description, shown under its label
	ID       string
	// Control is hidden for a path argument, which the page fills;
	// checkboxes for a list of an enum; repeated for any other list, an
	// input per value sent and one more; else the control an input form's
	// field of the type gets (text, email, url, tel, number, select,
	// checkbox).
	Control string
	// Type is a repeated list's input type.
	Type    string
	Attrs   string
	Options []formOption
	List    bool
	// Split marks a query list, whose values the router splits on commas.
	Split bool
	// Read is how a value's text is written as the argument's JSON: text,
	// integer or number, or flag for a filter's optional boolean, true when
	// its checkbox is sent and absent when not.
	Read string
	// Default is the argument's declared default, which a blank field
	// reads as the router reads an absent parameter; HasDefault reports
	// whether it has one.
	Default    string
	HasDefault bool
	Required   bool
}

// IsGet reports whether the form is a filter, sent by GET.
func (f argForm) IsGet() bool { return f.Method == "get" }

// Helper is the parse helper that reads the field: list for a list,
// checkbox for a checkbox that is false unchecked, single for any other.
func (f argField) Helper() string {
	switch {
	case f.List:
		return "list"
	case f.Control == "checkbox" && f.Read != "flag":
		return "checkbox"
	}
	return "single"
}

// PathFields are the arguments the operation's path carries, which the
// form holds as hidden inputs a page fills (`new`).
func (f argForm) PathFields() []argField {
	var out []argField
	for _, field := range f.Fields {
		if field.Control == "hidden" {
			out = append(out, field)
		}
	}
	return out
}

// OnlyPath reports whether every field is a path argument and the form
// embeds no input, so `new` sets every field.
func (f argForm) OnlyPath() bool {
	return f.Input == nil && len(f.PathFields()) == len(f.Fields)
}

// ArgNames are the arguments' names as Rust string literals, which the
// component's errors are split by.
func (f argForm) ArgNames() string {
	names := make([]string, len(f.Fields))
	for i, field := range f.Fields {
		names[i] = rustString(field.JSONName)
	}
	return strings.Join(names, ", ")
}

// RenderedNames are the names of the fields the component renders a
// control for, as Rust string literals: a refusal of any other field (a
// path argument's) is shown with the form's own messages.
func (f argForm) RenderedNames() string {
	var names []string
	for _, field := range f.Fields {
		if field.Control != "hidden" {
			names = append(names, rustString(field.JSONName))
		}
	}
	return strings.Join(names, ", ")
}

// HasInputs reports whether an argument form embeds an input form.
func (c *crate) HasInputs() bool {
	for _, f := range c.ArgForms {
		if f.Input != nil {
			return true
		}
	}
	return false
}

// InputComponents are the components of the input forms the argument
// forms embed, each once, sorted.
func (c *crate) InputComponents() []string {
	seen := map[string]bool{}
	for _, f := range c.ArgForms {
		if f.Input != nil {
			seen[f.Input.Component] = true
		}
	}
	return sortedKeys(seen)
}

// HasForms reports whether the crate has a form of either kind, so it
// writes forms.rs, whose FormErrors both use.
func (c *crate) HasForms() bool { return len(c.Forms) > 0 || len(c.ArgForms) > 0 }

// ArgFormsUse reports whether an argument form needs the helper what, so
// arg_forms.rs declares it: a field read by text, integer, number or flag,
// a field parsed by single, list or checkbox (argField.Helper), a query
// list that splits its values, or a form read from the query.
func (c *crate) ArgFormsUse(what string) bool {
	for _, f := range c.ArgForms {
		if what == "query" && f.IsGet() {
			return true
		}
		for _, field := range f.Fields {
			switch {
			case what == field.Helper(),
				what == field.Read && field.Helper() != "checkbox",
				what == "split" && field.Split:
				return true
			}
		}
	}
	return false
}

// argFormsOf is an argument form per operation of inProcess with an
// argument other than its input, sorted by name. inputForms are the input
// forms the crate has: an operation whose input has none gets no argument
// form, nor does one with an argument a form field cannot hold, and log
// says why.
func argFormsOf(schemas schemaSet, inProcess []declared, inputForms []form, log func(format string, args ...any)) ([]argForm, error) {
	byType := map[string]form{}
	for _, f := range inputForms {
		byType[f.TypeName] = f
	}
	var forms []argForm
	for _, d := range inProcess {
		e := d.endpoint
		if len(e.PathArgs)+len(e.QueryArgs)+len(e.BodyArgs) == 0 {
			continue
		}
		f, reason, err := schemas.argForm(e, d.op, byType)
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
func (s schemaSet) argForm(e registry.RustEndpoint, op *ir.FieldDef, inputForms map[string]form) (argForm, string, error) {
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
	}
	// The input's fields and the arguments share the form's names.
	names := map[string]string{}
	if input := e.Input; input != nil {
		name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(input.RustType, "Option<"), "types::"), ">")
		in, ok := inputForms[name]
		if !ok {
			return argForm{}, fmt.Sprintf("its input %s has no form", name), nil
		}
		f.Input = &argInput{Form: in.Name, Component: in.Component, Optional: !input.Required}
		for _, field := range in.Struct.Fields {
			names[field.JSONName] = "its input's field " + field.JSONName
		}
	}
	args := map[string]*ir.ArgumentDef{}
	for _, arg := range op.Arguments {
		args[arg.Name] = arg
	}
	prefix := kebabCase(e.Namespace) + "-" + kebabCase(e.Name)
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
			names[arg.Name] = "argument " + arg.Name
			field, reason := s.argField(prefix, arg, param, place.location, method == "get")
			if reason != "" {
				return argForm{}, reason, nil
			}
			f.Fields = append(f.Fields, field)
		}
	}
	return f, "", nil
}

// argField builds the field of one argument, or the reason a form field
// cannot hold it. Its control and attributes are those an input form's
// field of the same type and rules gets (schemaSet.control).
func (s schemaSet) argField(prefix string, arg *ir.ArgumentDef, param registry.RustParam, location string, filter bool) (argField, string) {
	ref := arg.TypeRef
	optional := strings.HasPrefix(param.RustType, "Option<")
	af := argField{
		Name:     param.Field,
		Local:    "arg_" + strings.TrimPrefix(param.Field, "r#"),
		JSONName: arg.Name,
		Label:    humanize(arg.Name),
		Hint:     firstLine(arg.Description),
		ID:       prefix + "-" + kebabCase(arg.Name),
		List:     ref.IsArray,
		Split:    ref.IsArray && location == "query",
		Required: !optional && arg.Default == nil,
	}
	if arg.Default != nil {
		af.Default, af.HasDefault = *arg.Default, true
	}
	switch {
	case ref.IsMap:
		return af, fmt.Sprintf("argument %s is a map", arg.Name)
	case ref.IsArrayOfArrays:
		return af, fmt.Sprintf("argument %s is a list of lists", arg.Name)
	case s.objectType(ref.Name) != nil:
		return af, fmt.Sprintf("argument %s is an object", arg.Name)
	}
	// A list's control is its element's: no list rule has an attribute,
	// and an element is never required.
	field := &ir.FieldDef{
		Name:              arg.Name,
		TypeRef:           ir.TypeRef{Name: ref.Name},
		Required:          af.Required && !ref.IsArray,
		ValidateMin:       arg.ValidateMin,
		ValidateMax:       arg.ValidateMax,
		ValidateMinLength: arg.ValidateMinLength,
		ValidateMaxLength: arg.ValidateMaxLength,
		ValidatePattern:   arg.ValidatePattern,
	}
	ff := formField{Name: af.Name, JSONName: arg.Name, Label: af.Label, Required: field.Required}
	if enum := s.enum(ref.Name); enum != nil {
		ff.Control, ff.Read, ff.Options = "select", "text", enumOptions(enum)
	} else if !s.control(&ff, field) {
		return af, fmt.Sprintf("argument %s holds a value a form field does not (a union, or any JSON value)", arg.Name)
	}
	af.Attrs, af.Options = ff.Attrs, ff.Options
	if field.Required && ff.Control != "checkbox" {
		af.Attrs = " required=(true)" + af.Attrs
	}
	switch ff.Read {
	case "integer", "number":
		af.Read = ff.Read
	default:
		af.Read = "text"
	}
	switch {
	case location == "path":
		af.Control, af.Attrs, af.Options = "hidden", "", nil
	case ref.IsArray && ff.Control == "checkbox":
		return af, fmt.Sprintf("argument %s is a list of booleans", arg.Name)
	case ref.IsArray && ff.Control == "select":
		af.Control = "checkboxes"
	case ref.IsArray:
		af.Control, af.Type = "repeated", ff.Control
	default:
		af.Control = ff.Control
	}
	// A filter's optional flag, unchecked, filters nothing: it is absent,
	// where an action's unchecked box is false, as an input form's is.
	if af.Control == "checkbox" && optional && filter {
		af.Read = "flag"
	}
	if af.Hint != "" && !af.List && af.Control != "hidden" {
		af.Attrs += fmt.Sprintf(" aria-describedby=%q", af.ID+"-hint")
	}
	return af, ""
}
