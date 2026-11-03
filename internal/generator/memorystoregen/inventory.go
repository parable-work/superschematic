package memorystoregen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	ir "github.com/parable-work/superschematic/ir"
)

// InventoryFile is the inventory's name in Dir.
const InventoryFile = "inventory.json"

// InventoryVersion is the version of the inventory's format.
const InventoryVersion = 1

// Inventory is what a MemoryStore service's build writes to
// <output-root>/memorystore/<service>/inventory.json, in no language: every
// declaration the store carries, the core's reserved ones among them, so a
// reviewer reads each name, its parts and how long each key lives (D75).
type Inventory struct {
	// Version is InventoryVersion.
	Version int `json:"version"`

	// Store is the MemoryStore service's name.
	Store string `json:"store"`

	// Declarations are sorted by name.
	Declarations []InventoryDeclaration `json:"declarations"`
}

// InventoryDeclaration is one channel or key.
type InventoryDeclaration struct {
	// Symbol is the class's name, which each language's definition is
	// named after.
	Symbol string `json:"symbol"`

	// Kind is channel, counter or claim.
	Kind ir.MemoryStoreKind `json:"kind"`

	// Name is the declaration's name, one segment.
	Name string `json:"name"`

	// NamePattern is the full name with a placeholder per part:
	// `{store}:stock-changed:{productId}`, where {store} is the store's
	// name, or `<store>@<member>` in a member of a parameterized
	// environment.
	NamePattern string `json:"namePattern"`

	// Parts are the parts in order.
	Parts []InventoryPart `json:"parts"`

	// Payload is a channel's message type; Signal marks a channel whose
	// name is the whole message. A key has neither.
	Payload *InventoryPayload `json:"payload,omitempty"`
	Signal  bool              `json:"signal,omitempty"`

	// TTLSeconds is a key's TTL; MaxTTLSeconds the longest a call may give
	// it.
	TTLSeconds    int `json:"ttlSeconds,omitempty"`
	MaxTTLSeconds int `json:"maxTtlSeconds,omitempty"`

	// File is the schema file that declares it, relative to the service's
	// directory; empty for a reserved one.
	File string `json:"file,omitempty"`

	// Doc is its doc comment.
	Doc string `json:"doc,omitempty"`

	// Reserved marks a declaration the core makes in every store.
	Reserved bool `json:"reserved,omitempty"`
}

// InventoryPart is one part: a field of the class.
type InventoryPart struct {
	// Name is the field's name.
	Name string `json:"name"`

	// Type is its scalar, enum or primitive (`Identity.UUID`, `string`).
	Type string `json:"type"`

	// Format is how its value is written into the name.
	Format ir.MemoryStorePartFormat `json:"format"`
}

// InventoryPayload is a channel's message type.
type InventoryPayload struct {
	// Type is the object type's name.
	Type string `json:"type"`

	// Service is the dependency service that declares it.
	Service string `json:"service"`
}

// NewInventory lists the declarations of in.Schema, reserved ones
// included, with each payload's service from in.Payloads.
func NewInventory(in Input) Inventory {
	inv := Inventory{Version: InventoryVersion, Store: in.Store, Declarations: []InventoryDeclaration{}}
	decls := append([]*ir.MemoryStoreDeclaration(nil), in.Schema.MemoryStoreDeclarations...)
	ir.SortMemoryStoreDeclarations(decls)
	for _, decl := range decls {
		if decl == nil {
			continue
		}
		entry := InventoryDeclaration{
			Symbol:        decl.Symbol,
			Kind:          decl.Kind,
			Name:          decl.Name,
			NamePattern:   decl.NamePattern(),
			Parts:         []InventoryPart{},
			TTLSeconds:    decl.TTLSeconds,
			MaxTTLSeconds: decl.MaxTTLSeconds,
			File:          decl.File,
			Doc:           decl.Doc,
			Reserved:      decl.Reserved,
		}
		for _, part := range decl.Parts {
			entry.Parts = append(entry.Parts, InventoryPart{Name: part.Name, Type: part.Type.Name, Format: part.Format})
		}
		if decl.Kind == ir.MemoryStoreChannel {
			if p, ok := in.PayloadOf(decl); ok {
				entry.Payload = &InventoryPayload{Type: p.Type, Service: p.Service}
			} else if decl.Payload == nil {
				entry.Signal = true
			}
		}
		inv.Declarations = append(inv.Declarations, entry)
	}
	return inv
}

// WriteInventory writes in's inventory to dir/inventory.json, replacing
// the directory's contents.
func WriteInventory(dir string, in Input) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(NewInventory(in)); err != nil {
		return fmt.Errorf("memory store %s: inventory: %w", in.Store, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("memory store %s: %w", in.Store, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("memory store %s: %w", in.Store, err)
	}
	if err := os.WriteFile(filepath.Join(dir, InventoryFile), buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("memory store %s: %w", in.Store, err)
	}
	return nil
}
