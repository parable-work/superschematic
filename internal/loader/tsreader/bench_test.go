package tsreader

import (
	"path/filepath"
	"testing"
)

// BenchmarkLoadService measures a full cold load (program creation + check +
// walk) of one service. The PAR-16 target for the eventual full build is
// under ~2s for ~50 schema files; one small service should be far under that.
func BenchmarkLoadService(b *testing.B) {
	dir := filepath.Join("testdata", "services", "fixture-db")
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := LoadService(dir); err != nil {
			b.Fatal(err)
		}
	}
}
