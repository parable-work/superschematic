package memorystoregen_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// tsDefinitionsUse runs shop-cache's TypeScript definitions against the
// HTTP runtime's in-process store: a signal reaches its subscriber, a
// payload whose message holds keys StockLevel does not declare, at the top
// and in a nested StockBin, decodes without them, one that does not parse
// is dropped and counted, a claim is taken once, forTTL refuses a TTL
// above the maximum, a part that is no segment is refused, and each part's
// wire string is the vectors' (a string scalar's wire form as it is, an
// integer in base 10, an enum's value, a boolean's word).
const tsDefinitionsUse = `import { expect, test } from 'bun:test';
import { InProcessMemoryStore, MemoryStoreNameError, signal } from '@superschematic/http-runtime';
import { Region } from '@schemas/shop-cache-common-types/types';
import type { StockLevel } from '@schemas/shop-cache-common-types/types';
import { STORE, productViews, receiptSent, stockChanged, stockSamples } from './index';

const productId = '1dTTvbXa8LxwSmeC8pRVJx';

test('the definitions name each channel and key by its typed parts', () => {
  expect(STORE).toBe('shop-cache');
  expect(stockChanged.for(productId).ref).toEqual({ declaration: 'stock-changed', name: 'stock-changed:1dTTvbXa8LxwSmeC8pRVJx' });
  const views = productViews.forTTL(86_400_000, productId, 20261025, Region.North, true, 'a label');
  expect(views.ref.name).toBe('views:1dTTvbXa8LxwSmeC8pRVJx:20261025:north:true:a label');
  expect(views.ttlMs).toBe(86_400_000);
  expect(() => productViews.forTTL(86_400_001, productId, 1, Region.South, false, 'x')).toThrow(RangeError);
  expect(() => productViews.forTTL(1000, productId, 1.5, Region.South, false, 'x')).toThrow(MemoryStoreNameError);
  expect(() => productViews.forTTL(1000, productId, 1, Region.South, false, 'a:b')).toThrow(MemoryStoreNameError);
  expect(receiptSent.for('o-1').ttlMs).toBe(600_000);
});

test('the definitions run against the in-process store', async () => {
  const store = new InProcessMemoryStore(STORE);
  const signals: unknown[] = [];
  await stockChanged.for(productId).subscribe(store, m => void signals.push(m));
  await stockChanged.for(productId).publish(store, signal);

  const samples = stockSamples.for(productId);
  const levels: StockLevel[] = [];
  await samples.subscribe(store, m => void levels.push(m));
  const later = { productId, onHand: 3, bins: [{ bin: 'A1', count: 3, shelf: 'top' }], warehouse: 'north' };
  await store.publish(samples.ref, new TextEncoder().encode(JSON.stringify(later)));
  await store.publish(samples.ref, new TextEncoder().encode(JSON.stringify({ productId, bins: [] })));
  await Bun.sleep(0);
  expect(signals).toEqual([signal]);
  expect(levels).toEqual([{ productId, onHand: 3, bins: [{ bin: 'A1', count: 3 }] } as unknown as StockLevel]);
  expect(samples.decodeFailures).toBe(1);

  const receipt = receiptSent.for('o-1');
  expect(await receipt.claim(store)).toBe(true);
  expect(await receipt.claim(store)).toBe(false);
  const views = productViews.forTTL(60_000, productId, 1, Region.North, false, 'x');
  expect(await views.increment(store)).toBe(1);
  expect(await views.count(store)).toBe(1);
});
`

// TestTypeScriptDefinitionsCompileAndRun builds shop-cache-common's
// TypeScript types and shop-cache's definitions into one Bun workspace
// wired against this checkout's HTTP runtime and superscalar, type-checks
// the definitions with tsc, and runs them (tsDefinitionsUse). It skips
// without bun, or fails under SUPERSCHEMATIC_REQUIRE_TS_CHECKS=1.
func TestTypeScriptDefinitionsCompileAndRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	testpaths.BuildHTTPRuntime(t, bunPath, paths)

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "schemas", "dist")
	f := load(t)
	for _, name := range []string{"shop-cache-common", store} {
		cfg := *f.configs[name]
		cfg.Outputs = map[string]any{"types": map[string]any{"typescript": map[string]any{"enabled": true}}}
		if _, err := generator.Run(f.schemas[name], &cfg, generator.Options{
			OutputRoot:     out,
			Paths:          paths,
			Naming:         naming.Default(),
			LoadDependency: f.dependency,
		}); err != nil {
			t.Fatalf("build %s: %v", name, err)
		}
	}
	dir := generator.TypesDir(out, "typescript", store)
	if err := os.WriteFile(filepath.Join(dir, "definitions.test.ts"), []byte(tsDefinitionsUse), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(bunPath, args...)
		cmd.Dir = dir
		got, err := cmd.CombinedOutput()
		if err != nil {
			if args[0] == "install" {
				testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed (likely offline): %v\n%s", err, got))
			}
			t.Fatalf("bun %s in %s: %v\n%s", strings.Join(args, " "), dir, err, got)
		}
		return string(got)
	}
	run(out, "install")
	run(dir, "x", "tsc", "--noEmit", "-p", ".")
	if got := run(dir, "test", "definitions.test.ts"); !strings.Contains(got, " 0 fail") {
		t.Fatalf("the definitions' test did not pass:\n%s", got)
	}
}
