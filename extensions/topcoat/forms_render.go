package topcoat

import (
	"fmt"
	"strings"
)

// formRenderer writes the view of a form's fields: each control at its
// name, a nested object's fields in a fieldset, and a list's rows with the
// buttons that add and remove them.
type formRenderer struct {
	strings.Builder
	// choosable reports whether a control takes the app's choices, and
	// visible whether a field renders at all.
	choosable bool
	visible   bool
}

// scope is where a struct's fields render.
type scope struct {
	// form is the Rust expression of the struct: form, form.guest, row0.
	form string
	// path is the struct's place in the form: guest, or rooms then a row's
	// index variable.
	path []pathSegment
	// idBase prefixes every id: booking-input.
	idBase string
	// optional marks fields inside an optional nested object, which no
	// control requires: the object is sent only when a field is.
	optional bool
	// depth counts the rows the struct sits in; its own rows' index is
	// i<depth>.
	depth int
}

// pathSegment is a field's JSON name or a row's index variable.
type pathSegment struct {
	key   string
	index string
}

// controlName is a path as a control's name, `rooms[{i0}].roomId`, as a
// format string when it holds an index (dynamic) and as the name itself
// otherwise.
func controlName(path []pathSegment) (name string, dynamic bool) {
	var b strings.Builder
	for i, segment := range path {
		if segment.index != "" {
			b.WriteString("[{" + segment.index + "}]")
			dynamic = true
			continue
		}
		if i > 0 {
			b.WriteString(".")
		}
		b.WriteString(segment.key)
	}
	if !dynamic {
		return b.String(), false
	}
	return formatName(path), true
}

// formatName is controlName's format string, a field name's braces
// escaped.
func formatName(path []pathSegment) string {
	var b strings.Builder
	for i, segment := range path {
		if segment.index != "" {
			b.WriteString("[{" + segment.index + "}]")
			continue
		}
		if i > 0 {
			b.WriteString(".")
		}
		b.WriteString(escapeBraces(segment.key))
	}
	return b.String()
}

// controlID is a path as an id under base, `booking-input-rooms-{i0}-room-id`:
// each name in kebab case and each row's number.
func controlID(base string, path []pathSegment) string {
	id := base
	for _, segment := range path {
		if segment.index != "" {
			id += "-{" + segment.index + "}"
			continue
		}
		id += "-" + kebabCase(segment.key)
	}
	return id
}

// choicePath is a path with its rows' indexes left out, as the app names a
// field when it supplies choices: rooms.roomId.
func choicePath(path []pathSegment) string {
	var keys []string
	for _, segment := range path {
		if segment.index == "" {
			keys = append(keys, segment.key)
		}
	}
	return strings.Join(keys, ".")
}

func escapeBraces(text string) string {
	return strings.NewReplacer("{", "{{", "}", "}}").Replace(text)
}

func with(path []pathSegment, segment pathSegment) []pathSegment {
	out := make([]pathSegment, 0, len(path)+1)
	return append(append(out, path...), segment)
}

// name is how a control's view refers to its name and id: literals for a
// field outside any row, else the `name` and `id` a `let` binds.
type name struct {
	attr string // name="guest.name" or name=(&name)
	arg  string // "guest.name" or &name
}

func (r *formRenderer) line(indent int, text string) {
	r.WriteString(strings.Repeat("    ", indent))
	r.WriteString(text)
	r.WriteString("\n")
}

// String is the view, without its last newline.
func (r *formRenderer) String() string {
	return strings.TrimSuffix(r.Builder.String(), "\n")
}

// bind writes `let <variable> = format!(...)` for a dynamic text and
// returns how the view refers to it, or the literal for a static one.
func (r *formRenderer) bind(indent int, variable, text string, dynamic bool) name {
	if !dynamic {
		return name{attr: rustString(text), arg: rustString(text)}
	}
	r.line(indent, "let "+variable+" = format!("+rustString(text)+");")
	return name{attr: "(&" + variable + ")", arg: "&" + variable}
}

// fields writes the view of st's fields in sc.
func (r *formRenderer) fields(sc scope, st *formStruct, indent int) {
	for _, f := range st.Fields {
		if f.Hidden {
			continue
		}
		r.visible = true
		path := with(sc.path, pathSegment{key: f.JSONName})
		switch f.Kind {
		case kindValue:
			r.value(sc, f, path, indent)
		case kindObject:
			r.object(sc, f, path, indent)
		case kindObjectRows, kindValueRows:
			r.rows(sc, f, path, indent)
		case kindGroup:
			r.group(sc, f, path, indent)
		}
	}
}

// value writes one control with its label and its errors.
func (r *formRenderer) value(sc scope, f *formField, path []pathSegment, indent int) {
	text, dynamic := controlName(path)
	r.line(indent, `<div class="field">`)
	field := r.bind(indent+1, "name", text, dynamic)
	id := r.bind(indent+1, "id", controlID(sc.idBase, path), dynamic)
	r.line(indent+1, "<label for="+id.attr+">"+rustString(f.Label)+"</label>")
	described := r.hint(indent+1, f, controlID(sc.idBase, path)+"-hint", dynamic)
	r.control(indent+1, f, sc.form+"."+f.Name, field, id, f.Required && !sc.optional, choicePath(path), described)
	r.errors(indent+1, f.Control == "textarea", field.arg)
	r.line(indent, "</div>")
}

// hint writes a field's description under its label, and returns the
// attribute that ties its control to it, or nothing for a field without
// one.
func (r *formRenderer) hint(indent int, f *formField, id string, dynamic bool) string {
	if f.Hint == "" {
		return ""
	}
	hint := r.bind(indent, "hint", id, dynamic)
	r.line(indent, `<p class="field-hint" id=`+hint.attr+">"+rustString(f.Hint)+"</p>")
	return " aria-describedby=" + hint.attr
}

// errors writes the messages of a control, or of a value and what is
// under it: a JSON value's members, a group's members.
func (r *formRenderer) errors(indent int, under bool, arg string) {
	of := "of"
	if under {
		of = "under"
	}
	r.line(indent, "for message in errors."+of+"("+arg+") {")
	r.line(indent+1, `<p class="field-error">(message)</p>`)
	r.line(indent, "}")
}

// control writes f's control over value, an Option<String> place, tied to
// its hint by described.
func (r *formRenderer) control(indent int, f *formField, value string, field, id name, required bool, choices, described string) {
	req := ""
	if required && f.Control != "checkbox" {
		req = " required=(true)"
	}
	// The hint ties in after the rules' attributes.
	attrs := f.Attrs + described
	invalid := " aria-invalid=(errors.invalid(" + field.arg + "))"
	open := "id=" + id.attr + " name=" + field.attr
	switch f.Control {
	case "select":
		r.line(indent, "<select "+open+req+described+invalid+">")
		if !required {
			r.line(indent+1, `<option value="" selected=(`+value+`.is_none())>""</option>`)
		}
		for _, option := range f.Options {
			r.line(indent+1, "<option value="+rustString(option.Value)+" selected=("+value+".as_deref() == Some("+rustString(option.Value)+"))>"+rustString(option.Label)+"</option>")
		}
		r.line(indent, "</select>")
	case "checkbox":
		r.line(indent, "<input "+open+` type="checkbox" checked=(`+value+".is_some())"+described+invalid+">")
	case "password":
		// A secret is never rendered back: a refused form asks for it again.
		r.line(indent, "<input "+open+` type="password"`+req+attrs+` autocomplete="off"`+invalid+">")
	case "datetime-local":
		r.line(indent, "<input "+open+` type="datetime-local"`+req+attrs+" value=(local_date_time("+value+".as_deref()))"+invalid+">")
	case "textarea":
		r.line(indent, "<textarea "+open+` rows="4" spellcheck="false"`+req+described+invalid+">("+value+".clone())</textarea>")
	default:
		input := "<input " + open + " type=" + rustString(f.Control) + req + attrs + " value=(" + value + ".clone())" + invalid + ">"
		if !f.Choosable {
			r.line(indent, input)
			return
		}
		// The app's choices, when it names the field, make it a select.
		r.choosable = true
		r.line(indent, "match choices.of("+field.arg+", "+rustString(choices)+") {")
		r.line(indent+1, fmt.Sprintf("Some(options) => choice_select(id: %s, name: %s, required: %t, invalid: errors.invalid(%s), options: choice_options(options, %s.as_deref())),", id.arg, field.arg, required, field.arg, value))
		r.line(indent+1, "None => "+input+",")
		r.line(indent, "}")
	}
}

// object writes a nested object as a fieldset of its fields. An optional
// one's fields are not required: the object is sent when any of them is.
func (r *formRenderer) object(sc scope, f *formField, path []pathSegment, indent int) {
	text, dynamic := controlName(path)
	r.line(indent, `<fieldset class="ss-object" data-field=`+rustString(f.JSONName)+">")
	field := r.bind(indent+1, "name", text, dynamic)
	r.line(indent+1, "<legend>"+rustString(f.Label)+"</legend>")
	r.errors(indent+1, false, field.arg)
	r.fields(scope{form: sc.form + "." + f.Name, path: path, idBase: sc.idBase, optional: sc.optional || !f.Required, depth: sc.depth}, f.Child, indent+1)
	r.line(indent, "</fieldset>")
}

// rows writes a list as a fieldset of rows, each numbered and with a
// button that removes it while the list holds more than its listMin, and
// a button that adds one while it holds fewer than its listMax. An object
// row is a fieldset of its fields, after a hidden input of the row's name
// that keeps a row whose controls send nothing; a value row is one
// control, a checkbox after a hidden input of its name, so an unchecked
// row is still a row.
func (r *formRenderer) rows(sc scope, f *formField, path []pathSegment, indent int) {
	text, dynamic := controlName(path)
	list := sc.form + "." + f.Name
	index, row := fmt.Sprintf("i%d", sc.depth), fmt.Sprintf("row%d", sc.depth)
	rowPath := with(path, pathSegment{index: index})
	rowLabel := "let label = format!(" + rustString(escapeBraces(f.RowLabel)+" {}") + ", " + index + " + 1);"
	remove := `<button type="submit" class="ss-remove" name="_action" value=(format!("remove:{%s}")) formnovalidate=(true) aria-label=(format!("Remove {label}"))>"Remove"</button>`

	r.line(indent, `<fieldset class="ss-list" data-field=`+rustString(f.JSONName)+">")
	field := r.bind(indent+1, "list", text, dynamic)
	r.line(indent+1, "<legend>"+rustString(f.Label)+"</legend>")
	r.errors(indent+1, false, field.arg)
	r.line(indent+1, "#[key("+index+")]")
	r.line(indent+1, "for ("+index+", "+row+") in "+list+".iter().enumerate() {")
	if f.Kind == kindObjectRows {
		r.line(indent+2, `<fieldset class="ss-row">`)
		r.line(indent+3, "let row = format!("+rustString(formatName(rowPath))+");")
		r.line(indent+3, rowLabel)
		r.line(indent+3, "<legend>(&label)</legend>")
		r.line(indent+3, `<input type="hidden" name=(&row) value="">`)
		r.errors(indent+3, false, "&row")
		r.fields(scope{form: row, path: rowPath, idBase: sc.idBase, depth: sc.depth + 1}, f.Child, indent+3)
		r.removeButton(indent+3, list, f.Min, fmt.Sprintf(remove, "row"))
		r.line(indent+2, "</fieldset>")
	} else {
		r.line(indent+2, `<div class="field ss-row">`)
		r.line(indent+3, "let name = format!("+rustString(formatName(rowPath))+");")
		r.line(indent+3, "let id = format!("+rustString(controlID(sc.idBase, rowPath))+");")
		r.line(indent+3, rowLabel)
		r.line(indent+3, "<label for=(&id)>(&label)</label>")
		if f.Control == "checkbox" {
			r.line(indent+3, `<input type="hidden" name=(&name) value="">`)
		}
		r.control(indent+3, f, row, name{attr: "(&name)", arg: "&name"}, name{attr: "(&id)", arg: "&id"}, true, choicePath(path), "")
		r.removeButton(indent+3, list, f.Min, fmt.Sprintf(remove, "name"))
		r.errors(indent+3, f.Control == "textarea", "&name")
		r.line(indent+2, "</div>")
	}
	r.line(indent+1, "}")
	add := "value=" + rustString("add:"+text)
	if dynamic {
		add = `value=(format!("add:{list}"))`
	}
	button := `<button type="submit" class="ss-add" name="_action" ` + add + ` formnovalidate=(true)>` + rustString(f.AddLabel) + `</button>`
	if f.Max != nil {
		r.line(indent+1, fmt.Sprintf("if %s.len() < %d {", list, *f.Max))
		r.line(indent+2, button)
		r.line(indent+1, "}")
	} else {
		r.line(indent+1, button)
	}
	r.line(indent, "</fieldset>")
}

// removeButton writes button, inside a test of list's length when it has
// a listMin.
func (r *formRenderer) removeButton(indent int, list string, min int, button string) {
	if min == 0 {
		r.line(indent, button)
		return
	}
	r.line(indent, fmt.Sprintf("if %s.len() > %d {", list, min))
	r.line(indent+1, button)
	r.line(indent, "}")
}

// group writes a list of an enum's members as a fieldset of checkboxes,
// one per member, all of the list's name.
func (r *formRenderer) group(sc scope, f *formField, path []pathSegment, indent int) {
	text, dynamic := controlName(path)
	value := sc.form + "." + f.Name
	r.line(indent, `<fieldset class="ss-choices" data-field=`+rustString(f.JSONName)+">")
	field := r.bind(indent+1, "name", text, dynamic)
	r.line(indent+1, "<legend>"+rustString(f.Label)+"</legend>")
	if f.Hint != "" {
		r.line(indent+1, `<p class="field-hint">`+rustString(f.Hint)+"</p>")
	}
	r.errors(indent+1, true, field.arg)
	for _, option := range f.Options {
		r.line(indent+1, `<label class="ss-choice"><input type="checkbox" name=`+field.attr+" value="+rustString(option.Value)+" checked=(checked(&"+value+", "+rustString(option.Value)+"))>"+rustString(option.Label)+"</label>")
	}
	r.line(indent, "</fieldset>")
}

// repeated writes an argument's list of values as a fieldset of inputs of
// the argument's name, one per value sent and a blank one for another, as
// a query list is sent: the router reads each value of a repeated key.
func (r *formRenderer) repeated(sc scope, f *formField, path []pathSegment, indent int) {
	text, dynamic := controlName(path)
	index, row := fmt.Sprintf("i%d", sc.depth), fmt.Sprintf("row%d", sc.depth)
	rowPath := with(path, pathSegment{index: index})
	r.line(indent, `<fieldset class="ss-list" data-field=`+rustString(f.JSONName)+">")
	field := r.bind(indent+1, "name", text, dynamic)
	r.line(indent+1, "<legend>"+rustString(f.Label)+"</legend>")
	if f.Hint != "" {
		r.line(indent+1, `<p class="field-hint">`+rustString(f.Hint)+"</p>")
	}
	r.errors(indent+1, true, field.arg)
	r.line(indent+1, "#[key("+index+")]")
	r.line(indent+1, "for ("+index+", "+row+") in "+sc.form+"."+f.Name+".iter().cloned().map(Some).chain([None]).enumerate() {")
	r.line(indent+2, `<div class="field ss-row">`)
	r.line(indent+3, "let id = format!("+rustString(controlID(sc.idBase, rowPath))+");")
	r.line(indent+3, "let label = format!("+rustString(escapeBraces(f.Label)+" {}")+", "+index+" + 1);")
	r.line(indent+3, "<label for=(&id)>(&label)</label>")
	r.control(indent+3, f, row, field, name{attr: "(&id)", arg: "&id"}, false, choicePath(path), "")
	r.line(indent+2, "</div>")
	r.line(indent+1, "}")
	r.line(indent, "</fieldset>")
}
