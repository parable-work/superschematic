package topcoat

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestHTMLPattern keeps a pattern rule off an input when a browser, which
// reads the attribute with the v flag, would read it otherwise or refuse
// it.
func TestHTMLPattern(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		ok      bool
	}{
		{`^[a-z0-9]+$`, true},
		{`^[0-9]{5}$`, true},
		{`^\d{3}-\d{4}$`, true},
		{`^[A-Z][a-z]*( [A-Z][a-z]*)*$`, true},
		{`^[a-z\-]+$`, true},
		{"", false},
		// A class with a trailing or leading hyphen, which the v flag refuses.
		{`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+$`, false},
		{`^[-a-z]+$`, false},
		// Characters the v flag reserves in a class, and a doubled
		// punctuator.
		{`^[a-z(]+$`, false},
		{`^[a-z|]+$`, false},
		{`^[a-z&&]+$`, false},
		// Group syntax and an escape the v flag refuses outside a class.
		{`^(?:ab)+$`, false},
		{`^a\-b$`, false},
		{`^\p{L}+$`, false},
		// An unclosed class.
		{`^[a-z`, false},
	} {
		got, ok := htmlPattern(tc.pattern)
		if ok != tc.ok || (ok && got != tc.pattern) {
			t.Errorf("htmlPattern(%q) = %q, %v; want ok %v", tc.pattern, got, ok, tc.ok)
		}
	}
}

func TestLabelsAndIDs(t *testing.T) {
	for name, want := range map[string]string{
		"displayName": "Display name",
		"email":       "Email",
		"userID":      "User ID",
		"postal_code": "Postal code",
		"Free":        "Free",
	} {
		if got := humanize(name); got != want {
			t.Errorf("humanize(%q) = %q, want %q", name, got, want)
		}
	}
	for name, want := range map[string]string{
		"SignupInput": "signup-input",
		"displayName": "display-name",
		"type":        "type",
	} {
		if got := kebabCase(name); got != want {
			t.Errorf("kebabCase(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestControlNames names a control by its field's path, a row by its index
// variable, and an id in kebab case; choices name a field without its
// rows' indexes.
func TestControlNames(t *testing.T) {
	static := []pathSegment{{key: "guest"}, {key: "name"}}
	if name, dynamic := controlName(static); name != "guest.name" || dynamic {
		t.Errorf("controlName(guest.name) = %q, %v", name, dynamic)
	}
	row := []pathSegment{{key: "rooms"}, {index: "i0"}, {key: "extras"}, {index: "i1"}}
	if name, dynamic := controlName(row); name != "rooms[{i0}].extras[{i1}]" || !dynamic {
		t.Errorf("controlName(row) = %q, %v", name, dynamic)
	}
	if got := controlID("booking-input", append(row, pathSegment{key: "roomId"})); got != "booking-input-rooms-{i0}-extras-{i1}-room-id" {
		t.Errorf("controlID = %q", got)
	}
	if got := choicePath(append(row, pathSegment{key: "roomId"})); got != "rooms.extras.roomId" {
		t.Errorf("choicePath = %q", got)
	}
	braced := []pathSegment{{key: "a{b}"}, {index: "i0"}}
	if name, _ := controlName(braced); name != "a{{b}}[{i0}]" {
		t.Errorf("controlName escapes a field's braces in a format string: %q", name)
	}
}

// TestHowAFormHoldsAField gives each kind of field its kind: a nested
// object, rows of objects and of values, a group, typed controls, and JSON
// text for a map, a list of lists, a union and a type that nests the type
// holding it.
func TestHowAFormHoldsAField(t *testing.T) {
	min, max := 2, 4
	str := func(name string) *ir.FieldDef {
		return &ir.FieldDef{Name: name, TypeRef: ir.TypeRef{Name: "string"}, Required: true}
	}
	schema := &ir.Schema{
		Scalars: map[string]*ir.ScalarDef{
			"Temporal.Date":     {Name: "Temporal.Date", LanguagePrimitive: ir.LanguageString},
			"Temporal.DateTime": {Name: "Temporal.DateTime", LanguagePrimitive: ir.LanguageString},
			"Stamp":             {Name: "Stamp", LanguagePrimitive: ir.LanguageString, Format: "time"},
		},
		Enums: map[string]*ir.EnumDef{
			"Color": {Name: "Color", Values: []ir.EnumValueDef{{Name: "Red", SerializedAs: "red"}, {Name: "DeepBlue"}}},
		},
		Types: map[string]*ir.TypeDef{
			"Category": {Name: "Category", Fields: []*ir.FieldDef{
				str("name"),
				{Name: "children", TypeRef: ir.TypeRef{Name: "Category", IsArray: true}},
			}},
			"Ping": {Name: "Ping", Fields: []*ir.FieldDef{{Name: "pong", TypeRef: ir.TypeRef{Name: "Pong"}}}},
			"Pong": {Name: "Pong", Fields: []*ir.FieldDef{{Name: "ping", TypeRef: ir.TypeRef{Name: "Ping"}}}},
			"Line": {Name: "Line", Fields: []*ir.FieldDef{str("sku")}},
			"Input": {Name: "Input", Fields: []*ir.FieldDef{
				{Name: "category", TypeRef: ir.TypeRef{Name: "Category"}, Required: true},
				{Name: "lines", TypeRef: ir.TypeRef{Name: "Line", IsArray: true}, ValidateListMin: &min, ValidateListMax: &max},
				{Name: "notes", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Required: true},
				{Name: "colors", TypeRef: ir.TypeRef{Name: "Color", IsArray: true}},
				{Name: "color", TypeRef: ir.TypeRef{Name: "Color"}, Default: ptr("DeepBlue")},
				{Name: "on", TypeRef: ir.TypeRef{Name: "Temporal.Date"}},
				{Name: "at", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}},
				{Name: "stamp", TypeRef: ir.TypeRef{Name: "Stamp"}},
				{Name: "pin", TypeRef: ir.TypeRef{Name: "string"}, Secret: true, Default: ptr("0000")},
				{Name: "labels", TypeRef: ir.TypeRef{Name: "string", IsMap: true}},
				{Name: "grid", TypeRef: ir.TypeRef{Name: "number", IsArray: true, IsArrayOfArrays: true}},
				{Name: "either", TypeRef: ir.TypeRef{Name: "Shape"}},
				{Name: "ping", TypeRef: ir.TypeRef{Name: "Ping"}},
				{Name: "flag", TypeRef: ir.TypeRef{Name: "boolean"}, Default: ptr("true")},
			}},
		},
	}
	b := &formBuilder{schemaSet: schemaSet{schema}, structs: map[string]*formStruct{}}
	st, err := b.structOf(schema.Types["Input"])
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]*formField{}
	for _, f := range st.Fields {
		fields[f.JSONName] = f
	}
	for name, want := range map[string]string{
		"category": kindObject, "lines": kindObjectRows, "notes": kindValueRows, "colors": kindGroup,
		"color": kindValue, "labels": kindValue, "grid": kindValue, "either": kindValue, "ping": kindObject,
	} {
		if got := fields[name].Kind; got != want {
			t.Errorf("%s: kind %s, want %s", name, got, want)
		}
	}
	for name, want := range map[string]string{
		"color": "select", "on": "date", "at": "datetime-local", "stamp": "time", "pin": "password",
		"labels": "textarea", "grid": "textarea", "either": "textarea", "flag": "checkbox",
	} {
		if got := fields[name].Control; got != want {
			t.Errorf("%s: control %s, want %s", name, got, want)
		}
	}
	if f := fields["lines"]; f.Min != 2 || *f.Max != 4 || f.Start != 2 || f.New() != "vec![LineForm::new(); 2]" {
		t.Errorf("lines: min %d max %d start %d new %s", f.Min, *f.Max, f.Start, f.New())
	}
	if f := fields["notes"]; f.Start != 1 || f.New() != "vec![None; 1]" {
		t.Errorf("notes, a required list: start %d new %s", f.Start, f.New())
	}
	for name, want := range map[string]string{
		"color": `Some("DeepBlue".to_owned())`, "flag": `Some("on".to_owned())`, "pin": "None", "category": "CategoryForm::new()",
	} {
		if got := fields[name].New(); got != want {
			t.Errorf("%s: new %s, want %s", name, got, want)
		}
	}
	if label := fields["at"].Label; label != "At (UTC)" {
		t.Errorf("a date-time's label says it is read as UTC: %q", label)
	}
	// A type that nests itself, directly or through another, holds the
	// nesting field as JSON text.
	children := b.structs["Category"].Fields[1]
	if children.Kind != kindValue || children.Control != "textarea" {
		t.Errorf("Category.children: %s %s, want JSON text", children.Kind, children.Control)
	}
	if pong := b.structs["Ping"].Fields[0]; pong.Control != "textarea" {
		t.Errorf("Ping.pong: %s, want JSON text", pong.Control)
	}
	if !st.rows() {
		t.Error("Input holds rows")
	}
}

func ptr(text string) *string { return &text }
