package typegen

import "testing"

// TestImportedGoTypesRewriteEachQualifierOnce: a service qualifier is
// rewritten to its alias only where a name begins, the longest service
// name first, so a service whose name ends another's never rewrites the
// other's qualifier, on any walk of the aliases (D94).
func TestImportedGoTypesRewriteEachQualifierOnce(t *testing.T) {
	aliases := map[string]string{"db": "coredb", "shop-db": "shopdb", "shop-db-archive": "shopdbarchive"}
	for in, want := range map[string]string{
		"[]shop-db.Item":          "[]shopdb.Item",
		"*db.Row":                 "*coredb.Row",
		"map[string]shop-db.Item": "map[string]shopdb.Item",
		"shop-db-archive.Item":    "shopdbarchive.Item",
		"string":                  "string",
	} {
		for range 50 {
			if got := normalizeImportedGoType(in, aliases); got != want {
				t.Fatalf("normalizeImportedGoType(%q) = %q, want %q", in, got, want)
			}
		}
	}
}
