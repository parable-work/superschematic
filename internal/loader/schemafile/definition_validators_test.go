package schemafile

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
)

// TestRegistriesWithOneDocumentShareItsValidators holds what makes a
// registry a load cheap: two core registries generate the same schema-file
// JSON Schema, and the second takes the validators the first compiled.
func TestRegistriesWithOneDocumentShareItsValidators(t *testing.T) {
	first := ensureCompiled(registry.New(naming.Naming{}))
	second := ensureCompiled(registry.New(naming.Naming{}))
	if first.err != nil || second.err != nil {
		t.Fatalf("compile: %v, %v", first.err, second.err)
	}
	if first == second {
		t.Fatal("two registries share one cache entry, so this test checks nothing")
	}
	if len(first.defs) == 0 {
		t.Fatal("no definition was compiled")
	}
	if reflect.ValueOf(first.defs).Pointer() != reflect.ValueOf(second.defs).Pointer() {
		t.Error("the second registry compiled the document again")
	}
}

// TestRecentDefinitionsKeepsAFew holds the bound: a document of its own a
// registry is not kept for the life of the process.
func TestRecentDefinitionsKeepsAFew(t *testing.T) {
	core := ensureCompiled(registry.New(naming.Naming{}))
	if core.err != nil {
		t.Fatal(core.err)
	}
	url := naming.Naming{}.OrDefault().MetaSchemaURL(schemaResourceName)
	kept, err := definitionValidators(url, core.bytes)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.ValueOf(kept).Pointer() != reflect.ValueOf(core.defs).Pointer() {
		t.Fatal("the core document's validators are not the ones its registry compiled")
	}
	// Documents of their own, under urls of their own: more than are kept.
	for i := range recentDefinitionsKept {
		if _, err := definitionValidators(fmt.Sprintf("%s?test=%d", url, i), core.bytes); err != nil {
			t.Fatal(err)
		}
	}
	recentDefinitions.Lock()
	entries := len(recentDefinitions.entries)
	recentDefinitions.Unlock()
	if entries != recentDefinitionsKept {
		t.Errorf("%d documents are kept, want %d", entries, recentDefinitionsKept)
	}
	again, err := definitionValidators(url, core.bytes)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.ValueOf(again).Pointer() == reflect.ValueOf(kept).Pointer() {
		t.Error("the document used longest ago was not dropped")
	}
}
