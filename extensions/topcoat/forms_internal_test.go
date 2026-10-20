package topcoat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
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
		// A number that ends a word is a word of its own, unless an
		// acronym or a single letter holds it or letters follow it.
		"line1":        "Line 1",
		"addressLine2": "Address line 2",
		"line1Text":    "Line 1 text",
		"Tier10":       "Tier 10",
		"step_2":       "Step 2",
		"awsS3Bucket":  "Aws S3 bucket",
		"HTTP2Enabled": "HTTP2 enabled",
		"userID2":      "User ID2",
		"v2":           "V2",
		"x509Cert":     "X509 cert",
		"md5sum":       "Md5sum",
		"2faCode":      "2fa code",
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
// holding it. A multiline hint makes text long or short whatever its
// maxLength, and the build log says when a control that is not text
// ignores it.
func TestHowAFormHoldsAField(t *testing.T) {
	min, max, long, short := 2, 4, 5000, 500
	yes, no := true, false
	str := func(name string) *ir.FieldDef {
		return &ir.FieldDef{Name: name, TypeRef: ir.TypeRef{Name: "string"}, Required: true}
	}
	schema := &ir.Schema{
		Scalars: map[string]*ir.ScalarDef{
			"Temporal.Date":     {Name: "Temporal.Date", LanguagePrimitive: ir.LanguageString},
			"Temporal.DateTime": {Name: "Temporal.DateTime", LanguagePrimitive: ir.LanguageString},
			"Stamp":             {Name: "Stamp", LanguagePrimitive: ir.LanguageString, Format: "time"},
			"Contact.Email":     {Name: "Contact.Email", LanguagePrimitive: ir.LanguageString},
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
				{Name: "story", TypeRef: ir.TypeRef{Name: "string"}, Display: &ir.FieldDisplay{Multiline: &yes}},
				{Name: "code", TypeRef: ir.TypeRef{Name: "string"}, ValidateMaxLength: &long, Display: &ir.FieldDisplay{Multiline: &no}},
				{Name: "reason", TypeRef: ir.TypeRef{Name: "string"}, ValidateMaxLength: &short},
				{Name: "email", TypeRef: ir.TypeRef{Name: "Contact.Email"}, Display: &ir.FieldDisplay{Multiline: &yes}},
				{Name: "paragraphs", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Display: &ir.FieldDisplay{Multiline: &yes}},
				{Name: "due", TypeRef: ir.TypeRef{Name: "Temporal.Date"}, Display: &ir.FieldDisplay{Multiline: &no}},
			}},
		},
	}
	var log strings.Builder
	b := &formBuilder{schemaSet: schemaSet{schema}, structs: map[string]*formStruct{}, log: func(format string, args ...any) {
		fmt.Fprintf(&log, format, args...)
	}}
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
		// Multiline text is long without a bound, and a hint of one line
		// keeps a bound of 5000 one line; without a hint, 500 is long.
		"story": "longtext", "code": "text", "reason": "longtext",
		// An email keeps its input, and each row of a list of text is long.
		"email": "email", "paragraphs": "longtext", "due": "date",
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
	if got, want := log.String(), "  - topcoat: Input.email: @display multiline is ignored: its control is an email input\n"; got != want {
		t.Errorf("the build log is %q, want %q: one line, for the email, and none for a date that asks for one line", got, want)
	}
}

func ptr(text string) *string { return &text }

// TestAMemberIsLabeledByItsTitle labels an enum's member by its title,
// else by its name in words, and keeps its serialized value: one rule for
// a select's options, a checkbox group's, a filter's and <enum>_label.
func TestAMemberIsLabeledByItsTitle(t *testing.T) {
	enum := &ir.EnumDef{Name: "OrderStatus", Values: []ir.EnumValueDef{
		{Name: "Placed", SerializedAs: "placed"},
		{Name: "OnHold", Title: "Held for a stock check", SerializedAs: "on_hold"},
		{Name: "InTransit", Title: "On its way"},
		{Name: "LostInPost"},
	}}
	want := []formOption{
		{Value: "placed", Label: "Placed"},
		{Value: "on_hold", Label: "Held for a stock check"},
		{Value: "InTransit", Label: "On its way"},
		{Value: "LostInPost", Label: "Lost in post"},
	}
	got := enumOptions(enum)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("enumOptions = %v, want %v", got, want)
	}
	// A form field and a group take the same labels.
	schema := &ir.Schema{
		Enums: map[string]*ir.EnumDef{"OrderStatus": enum},
		Types: map[string]*ir.TypeDef{"Input": {Name: "Input", Fields: []*ir.FieldDef{
			{Name: "status", TypeRef: ir.TypeRef{Name: "OrderStatus"}, Default: ptr("OnHold")},
			{Name: "statuses", TypeRef: ir.TypeRef{Name: "OrderStatus", IsArray: true}},
		}}},
	}
	b := &formBuilder{schemaSet: schemaSet{schema}, structs: map[string]*formStruct{}}
	st, err := b.structOf(schema.Types["Input"])
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range st.Fields {
		if fmt.Sprint(f.Options) != fmt.Sprint(want) {
			t.Errorf("%s: options %v, want %v", f.JSONName, f.Options, want)
		}
	}
	if got := st.Fields[0].New(); got != `Some("on_hold".to_owned())` {
		t.Errorf("a titled default is its serialized value: %s", got)
	}
}

// TestTwoMembersOneLabelIsABuildWarning: when two members of an enum the
// crate labels take one label, ignoring case, a title and another
// member's name in words, or two names in the same words, the build log
// says so, once per enum and pair, whichever of the forms, the argument
// forms and the display components shows it. The crate still builds.
func TestTwoMembersOneLabelIsABuildWarning(t *testing.T) {
	status := &ir.EnumDef{Name: "OrderStatus", Values: []ir.EnumValueDef{
		{Name: "OnHold", SerializedAs: "on_hold"},
		{Name: "Paused", Title: "On hold", SerializedAs: "paused"},
		{Name: "Shipped"},
		{Name: "ON_HOLD"},
	}}
	tier := &ir.EnumDef{Name: "Tier", Values: []ir.EnumValueDef{{Name: "Gold"}, {Name: "gold"}}}
	plain := &ir.EnumDef{Name: "Colour", Values: []ir.EnumValueDef{{Name: "Red", Title: "Warm"}, {Name: "Warmth"}}}
	schema := &ir.Schema{
		Enums: map[string]*ir.EnumDef{"OrderStatus": status, "Tier": tier, "Colour": plain},
		Types: map[string]*ir.TypeDef{"Input": {Name: "Input", Fields: []*ir.FieldDef{
			{Name: "status", TypeRef: ir.TypeRef{Name: "OrderStatus"}},
			{Name: "statuses", TypeRef: ir.TypeRef{Name: "OrderStatus", IsArray: true}},
			{Name: "colour", TypeRef: ir.TypeRef{Name: "Colour"}},
		}}},
	}
	b := &formBuilder{schemaSet: schemaSet{schema}, structs: map[string]*formStruct{}}
	st, err := b.structOf(schema.Types["Input"])
	if err != nil {
		t.Fatal(err)
	}
	c := &crate{
		FormStructs: []*formStruct{st},
		ArgForms:    []argForm{{Fields: []*argField{{formField: &formField{Options: enumOptions(status), enum: status}}}}},
		EnumLabels:  []enumLabel{{Name: "Tier", Options: enumOptions(tier), enum: tier}},
	}
	var log strings.Builder
	warnSharedLabels(c, func(format string, args ...any) { fmt.Fprintf(&log, format, args...) })
	want := `  - topcoat: enum OrderStatus: members OnHold and Paused share the label "On hold", so a select offers it twice; a @title tells them apart
  - topcoat: enum OrderStatus: members OnHold and ON_HOLD share the label "On hold", so a select offers it twice; a @title tells them apart
  - topcoat: enum Tier: members Gold and gold share the label "Gold", so a select offers it twice; a @title tells them apart
`
	if log.String() != want {
		t.Fatalf("build log:\n%s\nwant:\n%s", log.String(), want)
	}
}

// TestRowButtonsFollowTheDefaultButton reads every golden's forms.rs, as
// cargo never does, for the order Enter depends on (D44, amended): in each
// component whose view holds a row button (name="_action"), the first
// button is the hidden default, which submits the form without an
// _action. testdata/enter.rs holds every page the cargo tests render to
// the whole rule.
func TestRowButtonsFollowTheDefaultButton(t *testing.T) {
	const defaultButton = `<button type="submit" class="ss-submit" hidden=(true)></button>`
	goldens, err := filepath.Glob(filepath.Join("testdata", "golden", "*", "src", "forms.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(goldens) == 0 {
		t.Fatal("no golden forms.rs")
	}
	checked := 0
	for _, golden := range goldens {
		data, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		components := strings.Split(string(data), "\n#[component]\n")
		for _, component := range components[1:] {
			// The component's function ends at the first closing brace in
			// the file's first column.
			component, _, _ = strings.Cut(component, "\n}\n")
			name, _, _ := strings.Cut(strings.TrimPrefix(component, "pub async fn "), "(")
			if !strings.Contains(component, `name="_action"`) {
				continue
			}
			checked++
			first := strings.Index(component, "<button")
			if !strings.HasPrefix(component[first:], defaultButton) {
				line, _, _ := strings.Cut(component[first:], "\n")
				t.Errorf("%s: %s's first button is %s, not the default %s", golden, name, line, defaultButton)
			}
		}
	}
	if checked == 0 {
		t.Error("no golden component holds a row button")
	}
}

// TestAFormHoldsBoundedRows bounds every list a form holds: by its
// listMax, or by MAX_ROWS for a list without one or with one above it. A
// form reads one row past the limit, a row button adds none at it, and
// forms.rs declares MAX_ROWS only when a list's limit is it.
func TestAFormHoldsBoundedRows(t *testing.T) {
	three, many := 3, 5000
	schema := &ir.Schema{Types: map[string]*ir.TypeDef{
		"Line": {Name: "Line", Fields: []*ir.FieldDef{{Name: "sku", TypeRef: ir.TypeRef{Name: "string"}}}},
		"Input": {Name: "Input", Fields: []*ir.FieldDef{
			{Name: "lines", TypeRef: ir.TypeRef{Name: "Line", IsArray: true}, ValidateListMax: &three},
			{Name: "notes", TypeRef: ir.TypeRef{Name: "string", IsArray: true}},
			{Name: "codes", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, ValidateListMax: &many},
		}},
	}}
	b := &formBuilder{schemaSet: schemaSet{schema}, structs: map[string]*formStruct{}}
	st, err := b.structOf(schema.Types["Input"])
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []struct{ limit, bounds string }{
		{"3", "Bounds { min: 0, max: 3 }"},
		{"MAX_ROWS", "Bounds { min: 0, max: MAX_ROWS }"},
		{"MAX_ROWS", "Bounds { min: 0, max: MAX_ROWS }"},
	} {
		f := st.Fields[i]
		if f.Limit() != want.limit || f.Bounds() != want.bounds {
			t.Errorf("%s: limit %s bounds %s, want %s %s", f.JSONName, f.Limit(), f.Bounds(), want.limit, want.bounds)
		}
	}
	if maxRows != 1000 {
		t.Errorf("maxRows is %d, which forms.tmpl's MAX_ROWS and the D44 amendment say is 1000", maxRows)
	}
	st.Rows = st.rows()
	r := newFormRenderer()
	r.fields(scope{form: "form", idBase: "input"}, st, 0)
	for _, want := range []string{"if form.lines.len() < 3 {", "if form.notes.len() < MAX_ROWS {"} {
		if !strings.Contains(r.String(), want) {
			t.Errorf("the add button is bounded: no %q in\n%s", want, r.String())
		}
	}
	c := &crate{FormStructs: []*formStruct{st, b.structs["Line"]}}
	if !c.FormsUse("maxRows") {
		t.Error("forms.rs declares no MAX_ROWS for a list without a listMax")
	}
	st.Fields = st.Fields[:1]
	if c.FormsUse("maxRows") {
		t.Error("forms.rs declares MAX_ROWS, which no list's limit is")
	}
}

// TestASecretRowReadsNoValue binds `_` where a view reads no value: the
// rows of a list of secrets, which never render what was sent, and the
// form of a component whose every field is secret.
func TestASecretRowReadsNoValue(t *testing.T) {
	schema := &ir.Schema{Types: map[string]*ir.TypeDef{
		"Input": {Name: "Input", Fields: []*ir.FieldDef{
			{Name: "pins", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Secret: true},
			{Name: "pin", TypeRef: ir.TypeRef{Name: "string"}, Secret: true},
		}},
	}}
	b := &formBuilder{schemaSet: schemaSet{schema}, structs: map[string]*formStruct{}}
	st, err := b.structOf(schema.Types["Input"])
	if err != nil {
		t.Fatal(err)
	}
	r := newFormRenderer()
	r.fields(scope{form: "form", idBase: "input"}, st, 0)
	if !strings.Contains(r.String(), "for (i0, _) in form.pins.iter().enumerate() {") {
		t.Errorf("a secret row binds its value:\n%s", r.String())
	}
	if !r.used["form"] {
		t.Error("the list's rows read the form")
	}
	st.Fields = st.Fields[1:]
	r = newFormRenderer()
	r.fields(scope{form: "form", idBase: "input"}, st, 0)
	if r.used["form"] {
		t.Errorf("a form of one secret reads the form:\n%s", r.String())
	}
}

// TestReservedNames refuses an input whose form would hold a field under a
// name the form keeps: overflow always, and in a form with rows
// row_action and the JSON name _action, which its row buttons send. A
// nested object's _action is named under its object and is not refused.
func TestReservedNames(t *testing.T) {
	str := func(name string) *ir.FieldDef { return &ir.FieldDef{Name: name, TypeRef: ir.TypeRef{Name: "string"}} }
	rows := &ir.FieldDef{Name: "tags", TypeRef: ir.TypeRef{Name: "string", IsArray: true}}
	for _, tc := range []struct {
		fields []*ir.FieldDef
		want   string
	}{
		{[]*ir.FieldDef{rows, str("_action")}, "topcoat: input Input: field _action has the name of the form's row buttons"},
		{[]*ir.FieldDef{rows, str("rowAction")}, "topcoat: input Input: field rowAction is the form's row_action"},
		{[]*ir.FieldDef{str("overflow")}, "topcoat: input Input: field overflow is the form's overflow"},
		{[]*ir.FieldDef{str("_action"), str("rowAction")}, ""},
		{[]*ir.FieldDef{rows, {Name: "inner", TypeRef: ir.TypeRef{Name: "Inner"}}}, ""},
	} {
		schema := &ir.Schema{Types: map[string]*ir.TypeDef{
			"Input": {Name: "Input", Fields: tc.fields},
			"Inner": {Name: "Inner", Fields: []*ir.FieldDef{str("_action")}},
		}}
		endpoint := registry.RustEndpoint{
			Namespace: "things",
			Name:      "make",
			Input:     &registry.RustInput{RustType: "types::Input", Parse: "types::validators::parse_input", Required: true},
		}
		_, _, err := formsOf(schemaSet{schema}, []declared{{endpoint: endpoint}}, func(string, ...any) {})
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != tc.want {
			t.Errorf("%s: error %q, want %q", tc.fields[len(tc.fields)-1].Name, got, tc.want)
		}
	}
}
