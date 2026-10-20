package topcoat

import (
	"fmt"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// view is the display components of one record: `<type>_detail` renders a
// record as a description list, `<type>_table` records as a table.
type view struct {
	Record   string // OrderViewRecord
	TypeName string // OrderView
	Detail   string // order_view_detail
	Table    string // order_view_table
	// LabelLet binds `label`, the detail's accessible name, from the type's
	// @display titleField; LabelAttr is the attribute that carries it, or a
	// literal noun. Both are empty when the display gives no name.
	LabelLet  string
	LabelAttr string
	// Caption is the table's caption, the display's plural; empty for none.
	Caption string
	// Fields are the detail's entries, every field of the record in order;
	// Columns the table's, the display's summaryFields when it declares
	// them.
	Fields  []viewField
	Columns []viewField
	// Boxed reports whether the type nests itself, through any depth of
	// records, so its components box their views: a recursive component
	// needs a view of a known size.
	Boxed bool
	// DetailChoices and TableChoices report whether the detail and the
	// table read the app's choices: a value they label, or a nested
	// record's component they hand them to.
	DetailChoices bool
	TableChoices  bool
	// LinksTitle reports whether a row's link is on its title's cell,
	// which the columns hold; else it is on its first.
	LinksTitle bool
	// UntitledLet binds `untitled`, the text of a linked row whose linked
	// cell has none: the type's noun and the row's id. It is empty when
	// that cell always has text.
	UntitledLet string
	// SortFields are the columns whose headers sort a list the pages show
	// in the table (D44, amended), by their JSON names; none when no
	// sorted list does, and the table takes no `Sorted`.
	SortFields map[string]bool
}

// Sorted reports whether a sorted list's page shows the table, so it
// takes the headers' `Sorted`.
func (v view) Sorted() bool { return len(v.SortFields) > 0 }

// Sorts reports whether the header of the column of field sorts a list.
func (v view) Sorts(field string) bool { return v.SortFields[field] }

// viewField is one field of a record as its components render it.
type viewField struct {
	JSONName string
	Label    string
	// Entry is the field's entry in the detail, and Cell its cell in a row
	// of the table: view! markup, indented to its place.
	Entry string
	Cell  string
	// value is the markup of the field's value in a cell, and blank the
	// Rust of whether that cell has no text, from `row`; empty when it
	// always has some.
	value []string
	blank string
	// choices reports whether the field's value reads the app's choices.
	choices bool
}

// enumLabel is the function that labels the values of an enum a record
// holds.
type enumLabel struct {
	Name    string // OrderStatus
	Func    string // order_status_label
	Options []formOption
	enum    *ir.EnumDef
}

// detailIndent and cellIndent are where a field's entry and cell sit in
// views.tmpl.
const (
	detailIndent = 16
	cellIndent   = 24
)

// valueKind is how a component renders one value.
type valueKind int

const (
	// textValue is a string, rendered as its text, or the label the app's
	// choices give it.
	textValue valueKind = iota
	// numberValue is a number, rendered as textValue is.
	numberValue
	booleanValue
	enumValue
	// timeValue is a date, a time or a date-time, rendered as a <time>
	// with its text for a reader.
	timeValue
	// jsonValue is a union or any JSON value, held as its JSON text.
	jsonValue
	objectValue
	listValue
	mapValue
)

// valueShape is how a value of a field's type renders: its kind, and what
// the kind needs.
type valueShape struct {
	kind valueKind
	// enumFunc labels an enum's value.
	enumFunc string
	// timeFunc writes a time value's text for a reader: date_text,
	// time_text or date_time_text.
	timeFunc string
	// object is a nested record's components and title.
	object *objectRef
	// elem is a list's element, or a map's value.
	elem *valueShape
}

// objectRef is a record another record nests: its components, and its
// title field's record field when its type's @display declares one, which
// a table cell shows in place of its detail.
type objectRef struct {
	typeName string
	detail   string
	table    string
	title    string
	// titleOptional reports whether the title field is optional.
	titleOptional bool
}

// viewBuilder builds the components of the records the crate writes.
type viewBuilder struct {
	schemas schemaSet
	records map[string]*record
	enums   map[string]*enumLabel
	// nests is each record type's nested record types, by type name.
	nests map[string]map[string]bool
}

// viewsOf is the display components of each record, in the records'
// order, and the label function of each enum they show, by name.
func viewsOf(schemas schemaSet, records []record) ([]view, []enumLabel, error) {
	b := &viewBuilder{
		schemas: schemas,
		records: map[string]*record{},
		enums:   map[string]*enumLabel{},
		nests:   map[string]map[string]bool{},
	}
	for i := range records {
		b.records[records[i].TypeName] = &records[i]
	}
	names := map[string]string{}
	var views []view
	for _, rec := range records {
		v, err := b.view(rec)
		if err != nil {
			return nil, nil, fmt.Errorf("topcoat: views of %s: %w", rec.TypeName, err)
		}
		if other, taken := names[v.Detail]; taken {
			return nil, nil, fmt.Errorf("topcoat: types %s and %s both name the component %s", other, rec.TypeName, v.Detail)
		}
		names[v.Detail] = rec.TypeName
		views = append(views, v)
	}
	for i := range views {
		views[i].Boxed = b.nestsItself(views[i].TypeName)
	}
	labels := make([]enumLabel, 0, len(b.enums))
	funcs := map[string]string{}
	for _, name := range sortedKeys(b.enums) {
		label := b.enums[name]
		if other, taken := funcs[label.Func]; taken {
			return nil, nil, fmt.Errorf("topcoat: enums %s and %s both name the function %s", other, name, label.Func)
		}
		funcs[label.Func] = name
		labels = append(labels, *label)
	}
	return views, labels, nil
}

// view builds the components of rec.
func (b *viewBuilder) view(rec record) (view, error) {
	typeDef := b.schemas.objectType(rec.TypeName)
	if typeDef == nil {
		return view{}, fmt.Errorf("no object type %s", rec.TypeName)
	}
	component := componentName(rec.TypeName)
	v := view{
		Record:   rec.Name,
		TypeName: rec.TypeName,
		Detail:   component + "_detail",
		Table:    component + "_table",
	}
	b.nests[rec.TypeName] = map[string]bool{}
	byJSON := map[string]viewField{}
	for _, rf := range rec.Fields {
		field := fieldNamed(typeDef, rf.JSONName)
		if field == nil {
			return view{}, fmt.Errorf("no field %s", rf.JSONName)
		}
		shape := b.shapeOf(field.TypeRef)
		b.collectNests(rec.TypeName, shape)
		vf := viewField{
			JSONName: rf.JSONName,
			Label:    labelOf(field),
			choices:  shape.readsChoices(),
		}
		vf.Entry = indentLines(detailIndent, fieldEntry(vf, shape.render("record."+rf.Name, rf.JSONName, rf.Optional, false)))
		vf.value = shape.render("row."+rf.Name, rf.JSONName, rf.Optional, true)
		vf.blank = shape.blank("row."+rf.Name, rf.Optional)
		v.DetailChoices = v.DetailChoices || vf.choices
		v.Fields = append(v.Fields, vf)
		byJSON[rf.JSONName] = vf
	}
	v.Columns = append([]viewField(nil), v.Fields...)
	display := typeDef.Display
	if display == nil {
		display = &ir.TypeDisplay{}
	}
	title := titleField(typeDef, rec)
	if title != nil {
		titleText := "Some(record." + title.Name + ".as_str())"
		if title.Optional {
			titleText = "record." + title.Name + ".as_deref()"
		}
		noun := "None"
		if display.Noun != "" {
			noun = "Some(" + rustString(display.Noun) + ")"
		}
		v.LabelLet = "let label = label_of(" + titleText + ", " + noun + ");"
		v.LabelAttr = " aria-label=(label)"
	} else if display.Noun != "" {
		v.LabelAttr = " aria-label=" + rustString(display.Noun)
	}
	v.Caption = display.Plural
	if len(display.SummaryFields) > 0 {
		var columns []viewField
		for _, name := range display.SummaryFields {
			// A behavior's field is not the record's: a type that composes
			// a behavior builds no record (D16).
			if field := fieldNamed(typeDef, name); field != nil {
				if vf, ok := byJSON[field.Name]; ok {
					columns = append(columns, vf)
				}
			}
		}
		if len(columns) > 0 {
			v.Columns = columns
		}
	}
	// A row's link, its entry of the table's hrefs, is on its title's
	// cell, or its first when the columns hold no title.
	linked := 0
	for i, column := range v.Columns {
		if title != nil && column.JSONName == title.JSONName {
			linked, v.LinksTitle = i, true
		}
	}
	// A linked cell without text links the type's noun and the row's id,
	// so every row can be reached.
	if len(v.Columns) > 0 && v.Columns[linked].blank != "" {
		v.UntitledLet = "let untitled = (href.is_some() && " + v.Columns[linked].blank + ").then" + untitledText(typeDef, rec, v.Columns[linked].JSONName) + ";"
	}
	for i, column := range v.Columns {
		v.TableChoices = v.TableChoices || column.choices
		// The title's cell heads its row.
		open, close := "<td data-field=", "</td>"
		if title != nil && column.JSONName == title.JSONName {
			open, close = `<th scope="row" data-field=`, "</th>"
		}
		value := column.value
		if i == linked {
			value = linkLines(value, v.UntitledLet != "")
		}
		v.Columns[i].Cell = indentLines(cellIndent, wrapLines(open+rustString(column.JSONName)+">", close, value))
	}
	return v, nil
}

// linkLines puts a cell's value in a link to its row's page, `href`, when
// the row has one; with untitled, the link's text is `untitled` when the
// row binds it, the cell having none.
func linkLines(value []string, untitled bool) []string {
	text := value
	if untitled {
		text = []string{"if let Some(untitled) = untitled {", "    (untitled)", "} else {"}
		text = append(text, indentEach(4, value)...)
		text = append(text, "}")
	}
	lines := []string{"if let Some(href) = &href {"}
	lines = append(lines, indentEach(4, wrapLines("<a href=(href)>", "</a>", text))...)
	lines = append(lines, "} else {")
	lines = append(lines, indentEach(4, value)...)
	return append(lines, "}")
}

// untitledText is the Rust of `then`'s argument that names a linked row
// whose cell, linked's, has no text: the type's @display noun, else its
// name in words, and the row's `id` when the record holds one as text or a
// whole number and it is not the linked cell (`Widget 7`), as the pages'
// trail names an item without a title.
func untitledText(typeDef *ir.TypeDef, rec record, linked string) string {
	noun := humanize(typeDef.Name)
	if typeDef.Display != nil && typeDef.Display.Noun != "" {
		noun = typeDef.Display.Noun
	}
	for _, field := range rec.Fields {
		if field.JSONName == "id" && linked != "id" && (field.Type == "String" || field.Type == "i64") {
			return "(|| format!(" + rustString(noun+" {}") + ", row." + field.Name + "))"
		}
	}
	return "_some(" + rustString(noun) + ")"
}

// componentName is the snake-case stem of a type's components: OrderView
// is order_view.
func componentName(typeName string) string {
	return strings.TrimPrefix(registry.RustIdentifier(typeName, "record"), "r#")
}

// fieldNamed is the type's own field a @display names, by its name or its
// JSON key, or nil.
func fieldNamed(typeDef *ir.TypeDef, name string) *ir.FieldDef {
	for _, field := range typeDef.Fields {
		if field.Name == name {
			return field
		}
	}
	for _, field := range typeDef.Fields {
		if field.JSONTag != "" && field.JSONTag == name {
			return field
		}
	}
	return nil
}

// titleField is the record field of the type's @display titleField, when
// it declares one the record holds as text; else nil.
func titleField(typeDef *ir.TypeDef, rec record) *recordField {
	if typeDef.Display == nil || typeDef.Display.TitleField == "" {
		return nil
	}
	field := fieldNamed(typeDef, typeDef.Display.TitleField)
	if field == nil {
		return nil
	}
	for i, rf := range rec.Fields {
		if rf.JSONName == field.Name && strings.TrimSuffix(strings.TrimPrefix(rf.Type, "Option<"), ">") == "String" {
			return &rec.Fields[i]
		}
	}
	return nil
}

// shapeOf is how a value of a field of type ref renders: its element, in
// each level of list, then a map's entries.
func (b *viewBuilder) shapeOf(ref ir.TypeRef) valueShape {
	shape := b.element(ref.Name)
	for range ref.ArrayDepth() {
		elem := shape
		shape = valueShape{kind: listValue, elem: &elem}
	}
	if ref.IsMap {
		elem := shape
		shape = valueShape{kind: mapValue, elem: &elem}
	}
	return shape
}

// element is how a single value of the named type renders.
func (b *viewBuilder) element(name string) valueShape {
	if rec := b.records[name]; rec != nil {
		ref := &objectRef{typeName: name, detail: componentName(name) + "_detail", table: componentName(name) + "_table"}
		if typeDef := b.schemas.objectType(name); typeDef != nil {
			if title := titleField(typeDef, *rec); title != nil {
				ref.title, ref.titleOptional = title.Name, title.Optional
			}
		}
		return valueShape{kind: objectValue, object: ref}
	}
	if enum := b.schemas.enum(name); enum != nil {
		label := b.enums[enum.Name]
		if label == nil {
			label = &enumLabel{
				Name:    enum.Name,
				Func:    strings.TrimPrefix(registry.RustIdentifier(enum.Name, "enum"), "r#") + "_label",
				Options: enumOptions(enum),
				enum:    enum,
			}
			b.enums[enum.Name] = label
		}
		return valueShape{kind: enumValue, enumFunc: label.Func}
	}
	switch b.schemas.leafOf(name) {
	case stringLeaf:
		if format := timeFunc(b.schemas.scalar(name)); format != "" {
			return valueShape{kind: timeValue, timeFunc: format}
		}
		return valueShape{kind: textValue}
	case integerLeaf, numberLeaf:
		return valueShape{kind: numberValue}
	case booleanLeaf:
		return valueShape{kind: booleanValue}
	}
	return valueShape{kind: jsonValue}
}

// timeFunc is the views.rs function that writes a value of a string
// scalar holding a date (date_text), a time (time_text) or a date-time
// (date_time_text) for a reader, the <time> element's datetime attribute
// keeping it as its JSON sends it; empty for any other scalar.
func timeFunc(scalar *ir.ScalarDef) string {
	switch temporalControl(scalar) {
	case "date":
		return "date_text"
	case "time":
		return "time_text"
	case "datetime-local":
		return "date_time_text"
	}
	return ""
}

// collectNests records the record types a value of shape nests under
// typeName.
func (b *viewBuilder) collectNests(typeName string, shape valueShape) {
	switch {
	case shape.object != nil:
		b.nests[typeName][shape.object.typeName] = true
	case shape.elem != nil:
		b.collectNests(typeName, *shape.elem)
	}
}

// nestsItself reports whether a record of typeName nests one of its own
// type at some depth.
func (b *viewBuilder) nestsItself(typeName string) bool {
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(name string) bool {
		for _, nested := range sortedKeys(b.nests[name]) {
			if nested == typeName {
				return true
			}
			if !seen[nested] {
				seen[nested] = true
				if walk(nested) {
					return true
				}
			}
		}
		return false
	}
	return walk(typeName)
}

// readsChoices reports whether a value of the shape reads the app's
// choices: text or a number, which they may label, or a record, whose
// components they are handed to, or a list or a map of them.
func (s valueShape) readsChoices() bool {
	switch s.kind {
	case textValue, numberValue, objectValue:
		return true
	case listValue, mapValue:
		return s.elem.readsChoices()
	}
	return false
}

// render is the markup of expr, a value of the shape (in an Option when
// optional) that the view moves, at path, the field's JSON key, by which
// the app's choices name it: an absent value renders nothing. In a table
// cell, a record of a type with a title is its title.
func (s valueShape) render(expr, path string, optional, cell bool) []string {
	if !optional {
		return s.markup(expr, path, cell, 0)
	}
	inner := s.markup("value", path, cell, 0)
	lines := []string{"match " + expr + " {"}
	if len(inner) == 1 {
		lines = append(lines, "    Some(value) => "+inner[0]+",")
	} else {
		lines = append(lines, "    Some(value) => {")
		lines = append(lines, indentEach(8, inner)...)
		lines = append(lines, "    },")
	}
	return append(lines, `    None => "",`, "}")
}

// markup is the markup of expr, a value of the shape at path, at depth
// levels of list or map inside a field, which name the loop bindings. Text
// and numbers show the label the app's choices give them at path, a list's
// items and a map's values theirs; a nested record's components get the
// choices under path.
func (s valueShape) markup(expr, path string, cell bool, depth int) []string {
	switch s.kind {
	case textValue:
		return []string{"(text_of(&choices, " + rustString(path) + ", &" + expr + "))"}
	case numberValue:
		return []string{"(number_of(&choices, " + rustString(path) + ", " + expr + "))"}
	case booleanValue:
		return []string{"(if " + expr + ` { "Yes" } else { "No" })`}
	case enumValue:
		return []string{"<data value=(&" + expr + ")>(" + s.enumFunc + "(&" + expr + "))</data>"}
	case timeValue:
		return []string{"<time datetime=(&" + expr + ")>(" + s.timeFunc + "(&" + expr + "))</time>"}
	case jsonValue:
		return []string{`<pre class="ss-json">(` + expr + ")</pre>"}
	case objectValue:
		if cell && s.object.title != "" {
			title, at := expr+"."+s.object.title, rustString(path+"."+s.object.title)
			if s.object.titleOptional {
				return []string{"match " + title + " {", "    Some(title) => (text_of(&choices, " + at + ", &title)),", `    None => "",`, "}"}
			}
			return []string{"(text_of(&choices, " + at + ", &" + title + "))"}
		}
		return []string{s.object.detail + "(record: " + expr + ", choices: choices.under(" + rustString(path) + "))"}
	case listValue:
		if s.elem.kind == objectValue {
			return []string{s.elem.object.table + "(rows: " + expr + ", choices: choices.under(" + rustString(path) + "))"}
		}
		item := binding("item", depth)
		lines := []string{`<ul class="ss-list">`, "    for " + item + " in " + expr + " {"}
		lines = append(lines, indentEach(8, wrapLines("<li>", "</li>", s.elem.markup(item, path, cell, depth+1)))...)
		return append(lines, "    }", "</ul>")
	case mapValue:
		key, entry := binding("key", depth), binding("entry", depth)
		lines := []string{`<dl class="ss-map">`, "    for (" + key + ", " + entry + ") in " + expr + " {", "        <div>", "            <dt>(" + key + ")</dt>"}
		lines = append(lines, indentEach(12, wrapLines("<dd>", "</dd>", s.elem.markup(entry, path, cell, depth+1)))...)
		return append(lines, "        </div>", "    }", "</dl>")
	}
	return []string{"(" + expr + ")"}
}

// blank is the Rust of whether expr, a value of the shape (in an Option
// when optional), renders no text in a table's cell: an absent value,
// blank text or time, an empty list or map, or a record whose title is
// blank; empty for a value that always renders text, a number, a boolean,
// an enum, JSON or a record's detail.
func (s valueShape) blank(expr string, optional bool) string {
	if !optional {
		return s.blankOf(expr)
	}
	inner := s.blankOf("value")
	if inner == "" {
		return expr + ".is_none()"
	}
	return expr + ".as_ref().is_none_or(|value| " + inner + ")"
}

// blankOf is blank of a value that is there, expr, or a reference to it.
func (s valueShape) blankOf(expr string) string {
	switch s.kind {
	case textValue, timeValue:
		return expr + ".trim().is_empty()"
	case listValue, mapValue:
		return expr + ".is_empty()"
	case objectValue:
		if s.object.title == "" {
			return ""
		}
		title := expr + "." + s.object.title
		if s.object.titleOptional {
			return title + ".as_deref().is_none_or(|title| title.trim().is_empty())"
		}
		return title + ".trim().is_empty()"
	}
	return ""
}

// binding is a loop binding's name at depth: item, then item_2.
func binding(name string, depth int) string {
	if depth == 0 {
		return name
	}
	return fmt.Sprintf("%s_%d", name, depth+1)
}

// fieldEntry is a field's entry in a detail: its label and its value.
func fieldEntry(vf viewField, value []string) []string {
	lines := []string{
		"<div data-field=" + rustString(vf.JSONName) + ">",
		"    <dt>" + rustString(vf.Label) + "</dt>",
	}
	lines = append(lines, indentEach(4, wrapLines("<dd>", "</dd>", value))...)
	return append(lines, "</div>")
}

// wrapLines puts lines inside an element: on its line when they are one.
func wrapLines(open, close string, lines []string) []string {
	if len(lines) == 1 {
		return []string{open + lines[0] + close}
	}
	out := []string{open}
	out = append(out, indentEach(4, lines)...)
	return append(out, close)
}

func indentEach(n int, lines []string) []string {
	pad := strings.Repeat(" ", n)
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = pad + line
	}
	return out
}

func indentLines(n int, lines []string) string {
	return strings.Join(indentEach(n, lines), "\n")
}

// ViewsUse reports whether a component calls fn, a helper of views.rs:
// text_of, number_of, date_text, time_text or date_time_text, so views.rs
// declares it and what it needs.
func (c *crate) ViewsUse(fn string) bool {
	call := "(" + fn + "("
	for _, v := range c.Views {
		for _, f := range v.Fields {
			if strings.Contains(f.Entry, call) {
				return true
			}
		}
		for _, f := range v.Columns {
			if strings.Contains(f.Cell, call) {
				return true
			}
		}
	}
	return false
}

// ViewsLabel reports whether a detail names itself by its title, so
// views.rs declares label_of.
func (c *crate) ViewsLabel() bool {
	for _, v := range c.Views {
		if v.LabelLet != "" {
			return true
		}
	}
	return false
}

// SortedTables reports whether a table takes the headers' `Sorted`, so
// views.rs declares it.
func (c *crate) SortedTables() bool {
	for _, v := range c.Views {
		if v.Sorted() {
			return true
		}
	}
	return false
}

// ViewsBoxed reports whether a component boxes its view.
func (c *crate) ViewsBoxed() bool {
	for _, v := range c.Views {
		if v.Boxed {
			return true
		}
	}
	return false
}

// ViewRecords is the records views.rs renders, for its use.
func (c *crate) ViewRecords() string {
	names := make([]string, 0, len(c.Views))
	for _, v := range c.Views {
		names = append(names, v.Record)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
