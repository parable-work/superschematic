package registry

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// TestTheMemoryStoreKind: a MemoryStore service gets a sentinel and the
// memorystore generator, may not import the API, DB or Stack authoring
// packages, may reference a General, API or DB dependency's types, and
// declares nothing but channels and keys (D75).
func TestTheMemoryStoreKind(t *testing.T) {
	spec, ok := New(naming.Naming{}).Kind(string(ir.SchemaKindMemoryStore))
	if !ok {
		t.Fatal("no MemoryStore kind")
	}
	if spec.NoSentinel || len(spec.Pipeline) != 1 || spec.Pipeline[0] != memoryStoreGenerator ||
		!spec.ForbiddenPackages[pkgAPI] || !spec.ForbiddenPackages[pkgDB] || !spec.ForbiddenPackages[pkgStack] {
		t.Errorf("MemoryStore kind = %+v", spec)
	}
	for _, kind := range []ir.SchemaKind{ir.SchemaKindGeneral, ir.SchemaKindAPI, ir.SchemaKindDB} {
		if !spec.MayReference(string(kind)) {
			t.Errorf("a MemoryStore may not reference a %s's types", kind)
		}
	}
	if spec.MayReference(string(ir.SchemaKindMemoryStore)) {
		t.Error("a MemoryStore may reference another store's types")
	}

	schema := ir.NewSchema("shop-cache", ir.SchemaKindMemoryStore)
	schema.MemoryStoreDeclarations = []*ir.MemoryStoreDeclaration{
		{Name: "stock-changed", Symbol: "StockChanged", Kind: ir.MemoryStoreChannel, Parts: []ir.MemoryStorePart{{Name: "productId", Type: ir.TypeRef{Name: "string"}}}},
		{Name: "receipt-sent", Symbol: "ReceiptSent", Kind: ir.MemoryStoreClaim, TTLSeconds: 600},
	}
	schema.AddReservedMemoryStoreDeclarations()
	var clean findings
	spec.Verify(schema, &clean)
	if len(clean.errs) != 0 {
		t.Errorf("a sound MemoryStore schema: %v", clean.errs)
	}

	schema.Types = map[string]*ir.TypeDef{"Loose": {Name: "Loose"}}
	schema.Enums = map[string]*ir.EnumDef{"Mode": {Name: "Mode"}}
	schema.Jobs = []*ir.Job{{Name: "Sweep"}}
	var got findings
	spec.Verify(schema, &got)
	if len(got.errs) != 3 || !strings.HasSuffix(got.errs[0], "(enum Mode)") || !strings.HasSuffix(got.errs[1], "(job Sweep)") || !strings.HasSuffix(got.errs[2], "(type Loose)") ||
		!strings.Contains(got.errs[0], "shop-cache is a MemoryStore service, whose schema declares only channels and keys") {
		t.Errorf("a MemoryStore schema with other declarations: %v", got.errs)
	}
}

// TestMemoryStoreDeclarationChecks: each rule a declaration is held to in
// every form refuses it with its reason.
func TestMemoryStoreDeclarationChecks(t *testing.T) {
	ok := func() *ir.MemoryStoreDeclaration {
		return &ir.MemoryStoreDeclaration{Name: "views", Symbol: "Views", Kind: ir.MemoryStoreCounter, MaxTTLSeconds: 60,
			Parts: []ir.MemoryStorePart{{Name: "productId", Type: ir.TypeRef{Name: "string"}}}}
	}
	if err := CheckMemoryStoreDeclaration(ok()); err != nil {
		t.Fatalf("a sound declaration: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*ir.MemoryStoreDeclaration)
		want   string
	}{
		{"no symbol", func(d *ir.MemoryStoreDeclaration) { d.Symbol = "" }, "has no symbol"},
		{"name with a colon", func(d *ir.MemoryStoreDeclaration) { d.Name = "a:b" }, "':'"},
		{"name over 128 bytes", func(d *ir.MemoryStoreDeclaration) { d.Name = strings.Repeat("a", 129) }, "at most 128"},
		{"unknown kind", func(d *ir.MemoryStoreDeclaration) { d.Kind = "list" }, `kind is "list"`},
		{"no TTL", func(d *ir.MemoryStoreDeclaration) { d.MaxTTLSeconds = 0 }, "sets neither"},
		{"both TTLs", func(d *ir.MemoryStoreDeclaration) { d.TTLSeconds = 5 }, "sets both"},
		{"TTL past 7 days", func(d *ir.MemoryStoreDeclaration) { d.MaxTTLSeconds = 604801 }, "from 1 to 604800"},
		{"channel with a TTL", func(d *ir.MemoryStoreDeclaration) { d.Kind = ir.MemoryStoreChannel }, "sets neither ttlSeconds nor maxTtlSeconds"},
		{"key with a payload", func(d *ir.MemoryStoreDeclaration) { d.Payload = &ir.TypeRef{Name: "StockLevel"} }, "a counter has a payload"},
		{"list payload", func(d *ir.MemoryStoreDeclaration) {
			d.Kind, d.MaxTTLSeconds, d.Payload = ir.MemoryStoreChannel, 0, &ir.TypeRef{Name: "StockLevel", IsArray: true}
		}, "is a list or a map"},
		{"part twice", func(d *ir.MemoryStoreDeclaration) { d.Parts = append(d.Parts, d.Parts[0]) }, "part productId is given twice"},
		{"list part", func(d *ir.MemoryStoreDeclaration) { d.Parts[0].Type.IsArray = true }, "is a list or a map"},
		{"unknown format", func(d *ir.MemoryStoreDeclaration) { d.Parts[0].Format = "float" }, `format "float"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decl := ok()
			tc.change(decl)
			if err := CheckMemoryStoreDeclaration(decl); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestMemoryStoreReservedDeclaration: a schema may not take rate-limit,
// nor mark a declaration of its own reserved.
func TestMemoryStoreReservedDeclaration(t *testing.T) {
	spec, _ := New(naming.Naming{}).Kind(string(ir.SchemaKindMemoryStore))
	schema := ir.NewSchema("shop-cache", ir.SchemaKindMemoryStore)
	schema.MemoryStoreDeclarations = []*ir.MemoryStoreDeclaration{
		{Name: ir.MemoryStoreRateLimit, Symbol: "Limits", Kind: ir.MemoryStoreCounter, MaxTTLSeconds: 60},
		{Name: "mine", Symbol: "Mine", Kind: ir.MemoryStoreCounter, MaxTTLSeconds: 60, Reserved: true},
	}
	var got findings
	spec.Verify(schema, &got)
	if len(got.errs) != 2 || !strings.Contains(got.errs[0], "takes the name rate-limit") || !strings.Contains(got.errs[1], "only the core's rate-limit is") {
		t.Errorf("errs = %v", got.errs)
	}
}
