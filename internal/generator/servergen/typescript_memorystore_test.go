package servergen_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/servergen"
)

// TestTypeScriptMemoryStoreWiring: ts-shop lists the memory store ts-cache
// and counts its rate limits there, and ts-pricing lists it too (D75).
// ts-merged-stack's one server serves both, so its main.ts opens the store
// once, from the first listing API's config field, hands it to both Deps,
// gives ts-shop's router the shared rate limit, closes it on shutdown and
// keeps /readyz from asking for it; its package depends on the Redis
// client. The Workers entry of ts-workers-stack's ts-pricing holds the store
// disabled and imports no Redis entry.
func TestTypeScriptMemoryStoreWiring(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptFixture(t)
	f.build(t, repoRoot, tsFakePaths(repoRoot), append(slices.Clone(tsAPIs), tsMergedStack, tsWorkersStack)...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	read := func(dir, file string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	merged := servergen.ServerDir(out, tsMergedStack, tsMergedServer)
	main := read(merged, servergen.TypeScriptMainFile)
	if n := strings.Count(main, "= await openMemoryStore('ts-cache', 'TS_CACHE_MEMORY_STORE', "); n != 1 {
		t.Errorf("main.ts opens ts-cache %d times, want once:\n%s", n, main)
	}
	for _, want := range []string{
		"import { openRedisMemoryStore } from '@superschematic/http-runtime/redis';",
		"tsCache: tsCacheStore,",
		"rateLimit: { store: memoryStoreRateLimitStore(tsCacheStore, 'ts-shop') }",
		".finally(() => Promise.allSettled(memoryStores.map(store => store.close())))",
		"return disabledMemoryStore(service);",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.ts holds no %q", want)
		}
	}
	if n := strings.Count(main, "tsCache: tsCacheStore,"); n != 2 {
		t.Errorf("main.ts hands ts-cache to %d Deps, want 2", n)
	}
	if strings.Count(main, "memoryStoreRateLimitStore(") != 1 {
		t.Errorf("only ts-shop names a rateLimitStore, yet main.ts gives another router the shared limit")
	}
	if i := strings.Index(main, "const dependencies"); i >= 0 {
		end := strings.Index(main[i:], "];")
		if strings.Contains(main[i:i+end], "ts-cache") {
			t.Errorf("/readyz asks for the memory store: %s", main[i:i+end])
		}
	}
	if pkg := read(merged, servergen.TypeScriptPackageFile); !strings.Contains(pkg, `"@redis/client": "5.12.1"`) {
		t.Errorf("the server's package.json does not depend on the Redis client:\n%s", pkg)
	}

	workers := servergen.ServerDir(out, tsWorkersStack, "ts-pricing")
	fetch := read(workers, servergen.TypeScriptFetchFile)
	if !strings.Contains(fetch, "const tsCacheStore = disabledMemoryStore('ts-cache');") || strings.Contains(fetch, "/redis") {
		t.Errorf("fetch.ts does not hold ts-cache disabled, or imports the Redis entry:\n%s", fetch)
	}
}
