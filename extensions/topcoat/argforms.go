package topcoat

import (
	"fmt"
	"sort"
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
	// Choosable reports whether a control takes the app's choices, and
	// Body is the component's view.
	Choosable bool
	Body      string
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

// ShownArgs reports whether the component renders an argument's control:
// a path argument's is hidden.
func (f argForm) ShownArgs() bool {
	return len(f.PathFields()) < len(f.Fields)
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
// link), and any other operation with an argument beside its input (one
// with only a path argument is a button). inputs are the input forms'
// structs by type: an operation whose input has none gets no argument
// form, nor does one with an argument named as one of its input's fields,
// and log says why.
func argFormsOf(schemas schemaSet, inProcess []declared, inputs map[string]*formStruct, log func(format string, args ...any)) ([]argForm, error) {
	var forms []argForm
	for _, d := range inProcess {
		e := d.endpoint
		if len(e.PathArgs)+len(e.QueryArgs)+len(e.BodyArgs) == 0 {
			continue
		}
		if strings.EqualFold(e.Method, "GET") && len(e.QueryArgs) == 0 {
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
			names[arg.Name] = "argument " + arg.Name
			f.Fields = append(f.Fields, s.argField(arg, param, place.location, method == "get"))
		}
	}
	r := &formRenderer{}
	r.arguments(f, 2)
	f.Choosable, f.Body = r.choosable, r.String()
	return f, "", nil
}

// argField builds the field of one argument. Its control and attributes
// are those an input type's field of the same type and rules gets
// (schemaSet.control); a list of an enum is a group of checkboxes, any
// other list of values an input per value; a path argument is a hidden
// input; and a value no control holds (a map, a list of lists, an object,
// a list of booleans, a union, any JSON value) is its JSON text.
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
		f.Options = enumOptions(enum)
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
	if location == "path" {
		f.Kind, f.Control, f.Attrs, f.Hint, f.Choosable = kindPath, "hidden", "", "", false
	}
	// A filter's optional flag, unchecked, filters nothing: it is absent,
	// where an action's unchecked box is false, as an input form's is.
	if f.Control == "checkbox" && optional && filter {
		f.Read = "flag"
	}
	return af
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
// its declared default.
func (af *argField) ReadExpr() string {
	key := rustString(af.JSONName)
	if af.IsList() {
		return fmt.Sprintf("list(&mut errors, %s, &self.%s, %s)", key, af.Name, af.Read)
	}
	value := "self." + af.Name + ".as_deref()"
	if af.HasDefault {
		value += ".filter(|value| !value.is_empty()).or(Some(" + rustString(af.Default) + "))"
	}
	return fmt.Sprintf("single(&mut errors, %s, %s(%s))", key, af.Read, value)
}

// NewValue is the field's value in a new form: its default, or nothing.
func (af *argField) NewValue() string {
	switch {
	case af.IsList():
		return "Vec::new()"
	case af.HasDefault && af.Kind != kindPath:
		return "Some(" + rustString(af.Default) + ".to_owned())"
	}
	return "None"
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
			r.line(indent, `<input type="hidden" name=`+rustString(a.JSONName)+" value=(form."+a.Name+".clone())>")
		case kindValue:
			r.value(sc, a.formField, path, indent)
		case kindGroup:
			r.group(sc, a.formField, path, indent)
		case kindRepeated:
			r.repeated(sc, a.formField, path, indent)
		}
	}
	if f.Input != nil {
		r.fields(scope{form: "form.input", idBase: f.IDBase}, f.Input.Struct, indent)
	}
}
