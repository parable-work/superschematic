package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

func validOperationDocs() OperationDocs {
	return OperationDocs{
		Title:         "Create a return",
		Description:   "Opens a return for one delivered order.",
		Capability:    "orders.returns.create",
		Lifecycle:     DocsLifecycleActive,
		Visibility:    DocsVisibilityPreview,
		MappingStatus: DocsMappingStatusMapped,
		Sunset:        "2026-12-31",
	}
}

func TestValidateOperationDocs(t *testing.T) {
	valid := validOperationDocs()
	if err := ValidateOperationDocs(&valid); err != nil {
		t.Fatalf("valid docs: %v", err)
	}
	if err := ValidateOperationDocs(nil); err != nil {
		t.Fatalf("nil docs: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*OperationDocs)
		want   string
	}{
		{name: "title", mutate: func(d *OperationDocs) { d.Title = "" }, want: "title must be non-empty"},
		{name: "padded title", mutate: func(d *OperationDocs) { d.Title = " Create a return" }, want: "title must not contain surrounding whitespace"},
		{name: "description", mutate: func(d *OperationDocs) { d.Description = " " }, want: "description must be non-empty"},
		{name: "capability", mutate: func(d *OperationDocs) { d.Capability = "CreateReturn" }, want: "dot-separated lowercase segments"},
		{name: "one segment", mutate: func(d *OperationDocs) { d.Capability = "returns" }, want: "dot-separated lowercase segments"},
		{name: "lifecycle", mutate: func(d *OperationDocs) { d.Lifecycle = "legacy" }, want: "lifecycle"},
		{name: "visibility", mutate: func(d *OperationDocs) { d.Visibility = "hidden" }, want: "public, internal, or preview"},
		{name: "mapping", mutate: func(d *OperationDocs) { d.MappingStatus = "unknown" }, want: "mappingStatus"},
		{name: "replacement", mutate: func(d *OperationDocs) { d.Replacement = "  " }, want: "replacement must be non-empty"},
		{name: "sunset", mutate: func(d *OperationDocs) { d.Sunset = "December 31" }, want: "YYYY-MM-DD"},
		{name: "blank audience", mutate: func(d *OperationDocs) { d.Audience = " " }, want: "audience must be non-empty"},
		{name: "padded audience", mutate: func(d *OperationDocs) { d.Audience = "staff " }, want: "audience must not contain surrounding whitespace"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validOperationDocs()
			test.mutate(&candidate)
			err := ValidateOperationDocs(&candidate)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateOperationDocs() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestValidateOperationDocsAcceptsAnyAudience: the core has no audience
// vocabulary. A value set is an extension's check.
func TestValidateOperationDocsAcceptsAnyAudience(t *testing.T) {
	for _, audience := range []DocsAudience{"", "staff", "partner-integrations", "Anyone at all"} {
		docs := validOperationDocs()
		docs.Audience = audience
		if err := ValidateOperationDocs(&docs); err != nil {
			t.Errorf("audience %q: %v", audience, err)
		}
	}
}

func TestSchemaValidateChecksOperationDocs(t *testing.T) {
	s := NewSchema("shop", SchemaKindAPI)
	s.Types["Order"] = &TypeDef{Name: "Order", Role: RoleAPIView, Fields: []*FieldDef{
		{Name: "id", TypeRef: TypeRef{Name: "string"}, Required: true},
	}}
	docs := validOperationDocs()
	s.OperationSets = []*OperationSet{{Name: "OrderQueries", Operations: []*FieldDef{
		{Name: "getOrder", HTTPMethod: "GET", TypeRef: TypeRef{Name: "Order"}, Docs: &docs},
	}}}
	known := WithKnownExternals(map[string]bool{"string": true})
	if errs := s.Validate(known); len(errs) != 0 {
		t.Fatalf("valid schema: %v", errs)
	}

	docs.Capability = "Orders"
	errs := s.Validate(known)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "OrderQueries.getOrder has invalid docs: capability") {
		t.Fatalf("invalid operation docs: %v", errs)
	}

	docs.Capability = "orders.get"
	s.Types["Order"].Fields[0].Docs = &docs
	errs = s.Validate(known)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "Order.id carries operation docs") {
		t.Fatalf("docs on a data field: %v", errs)
	}
}

// TestOperationDocsMarshalsWithoutEmptyOptionals: an operation without
// @docs has no docs key, and an unset audience, replacement or sunset is
// left out.
func TestOperationDocsMarshalsWithoutEmptyOptionals(t *testing.T) {
	raw, err := json.Marshal(&FieldDef{Name: "getOrder", TypeRef: TypeRef{Name: "Order"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"docs"`) {
		t.Fatalf("operation without docs marshals a docs key: %s", raw)
	}
	docs := validOperationDocs()
	docs.Sunset = ""
	raw, err = json.Marshal(&docs)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"title":"Create a return","description":"Opens a return for one delivered order.","capability":"orders.returns.create","lifecycle":"active","visibility":"preview","mappingStatus":"mapped"}`
	if string(raw) != want {
		t.Fatalf("docs JSON = %s\nwant       %s", raw, want)
	}
}

func TestValidateOperationDocsGuidance(t *testing.T) {
	base := func() OperationDocs {
		d := validOperationDocs()
		d.UseWhen = "Use when you have an order identifier."
		d.DoNotUseWhen = "Do not use to list orders."
		d.Success = "Returns the requested order."
		d.Errors = []OperationDocsError{{
			Code:             "order_not_found",
			Description:      "No order has that identifier.",
			CommonCorrection: "Take the identifier from a listOrders result.",
		}}
		return d
	}
	valid := base()
	if err := ValidateOperationDocs(&valid); err != nil {
		t.Fatalf("valid guidance: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*OperationDocs)
		want   string
	}{
		{name: "blank useWhen", mutate: func(d *OperationDocs) { d.UseWhen = " \t" }, want: "useWhen must be non-empty when provided"},
		{name: "padded doNotUseWhen", mutate: func(d *OperationDocs) { d.DoNotUseWhen = " Do not use to list orders." }, want: "doNotUseWhen must not contain surrounding whitespace"},
		{name: "padded success", mutate: func(d *OperationDocs) { d.Success = "Returns the order. " }, want: "success must not contain surrounding whitespace"},
		{name: "blank error correction", mutate: func(d *OperationDocs) { d.Errors[0].CommonCorrection = " " }, want: "errors commonCorrection must be non-empty"},
		{name: "missing error code", mutate: func(d *OperationDocs) { d.Errors[0].Code = "" }, want: "errors code must be non-empty"},
		{name: "padded error description", mutate: func(d *OperationDocs) { d.Errors[0].Description = " No order has that identifier." }, want: "errors description must not contain surrounding whitespace"},
		{name: "duplicate error code", mutate: func(d *OperationDocs) {
			duplicate := d.Errors[0]
			duplicate.Code = "ORDER_NOT_FOUND"
			d.Errors = append(d.Errors, duplicate)
		}, want: `errors code "ORDER_NOT_FOUND" must not be duplicated`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base()
			test.mutate(&candidate)
			err := ValidateOperationDocs(&candidate)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateOperationDocs() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSchemaValidateRejectsBlankFieldPresentation(t *testing.T) {
	s := NewSchema("shop", SchemaKindGeneral)
	s.Types["Settings"] = &TypeDef{Name: "Settings", Role: RoleEmbeddedStruct, Fields: []*FieldDef{
		{Name: "region", TypeRef: TypeRef{Name: "string"}, Required: true, Title: "Region", Purpose: "Where orders ship from.", Icon: "globe"},
	}}
	known := WithKnownExternals(map[string]bool{"string": true})
	if errs := s.Validate(known); len(errs) != 0 {
		t.Fatalf("valid presentation: %v", errs)
	}
	field := s.Types["Settings"].Fields[0]
	field.Title, field.Purpose, field.Icon = " ", "\n", "\t"
	errs := s.Validate(known)
	if len(errs) != 3 {
		t.Fatalf("errors = %v, want one per blank value", errs)
	}
	for i, name := range []string{"title", "purpose", "icon"} {
		if want := "Settings.region " + name + " must be non-empty"; !strings.Contains(errs[i].Error(), want) {
			t.Errorf("errors[%d] = %v, want %q", i, errs[i], want)
		}
	}
}

func TestValidateOperationDocsReplay(t *testing.T) {
	tests := []struct {
		name     string
		mode     DocsReplayMode
		keys     []string
		revision []string
		want     string
	}{
		{name: "none"},
		{name: "read only", mode: DocsReplayModeReadOnly},
		{name: "idempotent", mode: DocsReplayModeIdempotent, keys: []string{"/requestId"}},
		{name: "compare and swap", mode: DocsReplayModeCompareAndSwap, revision: []string{"/revision"}},
		{name: "compare and swap with key", mode: DocsReplayModeCompareAndSwap, keys: []string{"/requestId"}, revision: []string{"/order/revision"}},
		{name: "escaped pointer", mode: DocsReplayModeIdempotent, keys: []string{"/a~1b/c~0d"}},
		{name: "pointers without mode", keys: []string{"/requestId"}, want: "replay pointers require replayMode"},
		{name: "read only with pointers", mode: DocsReplayModeReadOnly, revision: []string{"/revision"}, want: "read_only replayMode cannot declare replay pointers"},
		{name: "idempotent without key", mode: DocsReplayModeIdempotent, want: "idempotent replayMode requires idempotencyKeyPointers"},
		{name: "idempotent with revision", mode: DocsReplayModeIdempotent, keys: []string{"/requestId"}, revision: []string{"/revision"}, want: "cannot declare expectedRevisionPointers"},
		{name: "compare and swap without revision", mode: DocsReplayModeCompareAndSwap, want: "compare_and_swap replayMode requires expectedRevisionPointers"},
		{name: "unknown mode", mode: "retry", want: `replayMode "retry" must be read_only, idempotent, or compare_and_swap`},
		{name: "relative pointer", mode: DocsReplayModeIdempotent, keys: []string{"requestId"}, want: "must be an RFC 6901 JSON pointer"},
		{name: "bad escape", mode: DocsReplayModeIdempotent, keys: []string{"/a~2"}, want: "must be an RFC 6901 JSON pointer"},
		{name: "duplicate pointer", mode: DocsReplayModeIdempotent, keys: []string{"/requestId", "/requestId"}, want: "must not be duplicated"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			docs := validOperationDocs()
			docs.ReplayMode = test.mode
			docs.IdempotencyKeyPointers = test.keys
			docs.ExpectedRevisionPointers = test.revision
			err := ValidateOperationDocs(&docs)
			if test.want == "" {
				if err != nil {
					t.Fatalf("ValidateOperationDocs() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateOperationDocs() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestOperationDocsReplayKeyOrder: the replay keys sit between sunset and
// useWhen in the persisted IR.
func TestOperationDocsReplayKeyOrder(t *testing.T) {
	docs := validOperationDocs()
	docs.ReplayMode = DocsReplayModeIdempotent
	docs.IdempotencyKeyPointers = []string{"/requestId"}
	docs.UseWhen = "Use when a delivered order needs a return."
	raw, err := json.Marshal(&docs)
	if err != nil {
		t.Fatal(err)
	}
	want := `"sunset":"2026-12-31","replayMode":"idempotent","idempotencyKeyPointers":["/requestId"],"useWhen":`
	if !strings.Contains(string(raw), want) {
		t.Fatalf("docs JSON = %s\nwant it to contain %s", raw, want)
	}
}
