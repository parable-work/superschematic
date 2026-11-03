package memorystoregen

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestNewInventoryMarksSignalsAndPayloads: a channel with no payload is a
// signal, a channel with one names its type and service, a key is
// neither, and the reserved declaration is listed with no file.
func TestNewInventoryMarksSignalsAndPayloads(t *testing.T) {
	schema := ir.NewSchema("shop-cache", ir.SchemaKindMemoryStore)
	schema.MemoryStoreDeclarations = []*ir.MemoryStoreDeclaration{
		{Name: "stock-changed", Symbol: "StockChanged", Kind: ir.MemoryStoreChannel, File: "src/a.schema.ts",
			Parts: []ir.MemoryStorePart{{Name: "productId", Type: ir.TypeRef{Name: "string"}, Format: ir.MemoryStorePartString}}},
		{Name: "stock-samples", Symbol: "StockSamples", Kind: ir.MemoryStoreChannel, Payload: &ir.TypeRef{Name: "StockLevel"}},
		{Name: "receipt-sent", Symbol: "ReceiptSent", Kind: ir.MemoryStoreClaim, TTLSeconds: 600},
	}
	schema.AddReservedMemoryStoreDeclarations()
	in := Input{Store: "shop-cache", Schema: schema, Payloads: map[string]Payload{"StockLevel": {Type: "StockLevel", Service: "shop-common"}}}
	inv := NewInventory(in)
	byName := map[string]InventoryDeclaration{}
	for _, d := range inv.Declarations {
		byName[d.Name] = d
	}
	if len(inv.Declarations) != 4 || inv.Declarations[0].Name != "rate-limit" {
		t.Fatalf("declarations = %+v", inv.Declarations)
	}
	if d := byName["stock-changed"]; !d.Signal || d.Payload != nil || d.NamePattern != "{store}:stock-changed:{productId}" || d.File != "src/a.schema.ts" {
		t.Errorf("stock-changed = %+v", d)
	}
	if d := byName["stock-samples"]; d.Signal || d.Payload == nil || *d.Payload != (InventoryPayload{Type: "StockLevel", Service: "shop-common"}) {
		t.Errorf("stock-samples = %+v", d)
	}
	if d := byName["receipt-sent"]; d.Signal || d.Payload != nil || d.TTLSeconds != 600 || d.NamePattern != "{store}:receipt-sent" {
		t.Errorf("receipt-sent = %+v", d)
	}
	if d := byName["rate-limit"]; !d.Reserved || d.MaxTTLSeconds != ir.MemoryStoreRateLimitMaxTTLSeconds || d.NamePattern != "{store}:rate-limit:{service}:{operation}:{window}:{client}" {
		t.Errorf("rate-limit = %+v", d)
	}
}
