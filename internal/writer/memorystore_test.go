package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestMemoryStoreRoundTrip: fixture-cache's channels and keys survive
// every format (D75). The writers leave the core's reserved rate-limit
// declaration out, which the reload adds again, and the TypeScript writer
// writes each as the @channel or @key class the reader reads, its name
// only where the class's kebab case would not give it, with the payload
// and the enum a part names imported from the dependency.
func TestMemoryStoreRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-cache")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	want := normalizeIR(t, schema)
	// The reload reads the dependency whose type a channel carries from the
	// fixtures.
	deps := loader.WithDependencyLoader(func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	})
	for _, target := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(target), func(t *testing.T) {
			if got := normalizeIR(t, writeAndReloadWith(t, schema, target, deps)); got != want {
				t.Errorf("IR mismatch after TS -> %s round trip\nwant:\n%s\ngot:\n%s", target, want, got)
			}
		})
	}

	dir := t.TempDir()
	if _, err := WriteService(schema, FormatTS, dir); err != nil {
		t.Fatalf("WriteService: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "src"))
	var source []byte
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".schema.ts") {
			if source, err = os.ReadFile(filepath.Join(dir, "src", entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, want := range []string{
		`import { channel, key } from "@superschematic/memory-store";`,
		`import { Region, StockLevel } from "@schemas/fixture-cache-common";`,
		"@channel()\nexport abstract class StockChanged {\n  productId: Identity.UUID;\n}",
		"@channel({ payload: StockLevel })\nexport abstract class StockSamples {",
		`@key({ use: "claim", ttlSeconds: 600 })`,
		`@key({ use: "counter", maxTtlSeconds: 86400, name: "views" })`,
		"  region: Region;\n",
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("TS output missing %q:\n%s", want, source)
		}
	}
	if strings.Contains(string(source), "rate-limit") || strings.Contains(string(source), "RateLimit") {
		t.Errorf("TS output holds the reserved declaration:\n%s", source)
	}
}
