package stacktest

import (
	"os"
	"path/filepath"

	ir "github.com/parable-work/superschematic/ir"
)

// The fake bundle platform's fixture (D64): BundlePlatform runs a
// TypeScript server whose artifact is a bundle, the files its package's
// bundle.ts writes. WithBundle places shop-storefront on it, and
// WriteBundlePackage writes the package a deploy runs `bun bundle.ts
// <dir>` in.

// BundleFiles are the files the fixture's bundle.ts writes, and the
// modules BundlePlatform's script reads, the first its main module.
var BundleFiles = []string{"main.js", "main.js.map"}

// StorefrontServer is the deployable of shop-storefront, which WithBundle
// places on BundlePlatform.
const StorefrontServer = "shop-storefront"

// WithBundle returns s deploying shop-storefront as well, a TypeScript
// server whose artifact is a bundle in Staging and in Preview, which
// extends it, where it is placed on BundlePlatform (D64), and an image on
// RunPlatform in Production. It resolves over AcmeShop's services, which
// hold shop-storefront.
func WithBundle(s *ir.Stack) *ir.Stack {
	s.Deploy = append(s.Deploy, ShopStorefront)
	for _, env := range s.Environments {
		if env.Name == "Staging" {
			env.Settings = append(env.Settings, &ir.DeployableSettings{Of: Of(ShopStorefront), Platform: BundlePlatform})
		}
	}
	return s
}

// BundleScript is the fixture package's bundle.ts: it writes main.js, the
// package's main.ts as it is, and a source map into the directory it is
// given, as a generated bundle.ts writes a Worker's bundle (D64).
const BundleScript = `// The fake bundle platform's bundle.ts: bun bundle.ts <dir> writes the
// bundle's files into dir.
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const dir = process.argv[2];
if (!dir) {
  console.error("usage: bun bundle.ts <dir>");
  process.exit(2);
}
mkdirSync(dir, { recursive: true });
writeFileSync(join(dir, "main.js"), readFileSync(join(import.meta.dir, "main.ts"), "utf8"));
writeFileSync(join(dir, "main.js.map"), JSON.stringify({ version: 3, sources: ["main.ts"], mappings: "" }) + "\n");
`

// FailingBundleScript is a bundle.ts that fails, as a bundle that does not
// build does.
const FailingBundleScript = `console.error("bundle.ts: main.ts does not build");
process.exit(1);
`

// WriteBundlePackage writes the fixture package of deployable, where a
// deploy of stack runs its bundle.ts: server/<stack>/<deployable>/ under
// outputRoot, with script as bundle.ts (BundleScript, or
// FailingBundleScript) and main.ts holding source, which the bundle's
// main.js is. It returns the package's directory.
func WriteBundlePackage(outputRoot, stack, deployable, script, source string) (string, error) {
	dir := filepath.Join(outputRoot, "server", stack, deployable)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "bundle.ts"), []byte(script), 0o644); err != nil {
		return "", err
	}
	return dir, os.WriteFile(filepath.Join(dir, "main.ts"), []byte(source), 0o644)
}
