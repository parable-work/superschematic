// Append `.js` to extensionless relative imports in the ESM build (dist/esm).
// tsc with moduleResolution: bundler emits extensionless specifiers, which Node
// ESM rejects at runtime (ERR_MODULE_NOT_FOUND). builtin-scalars.generated.js
// is @generated and must not be hand-edited at the source, so the fix is
// applied to the emitted output. Idempotent: a specifier that already ends in
// .js or .mjs is skipped. Same script as utils/parable-scalars/typescript
// minus that package's backend renames.
import { readdirSync, readFileSync, writeFileSync, statSync, existsSync } from "node:fs";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const esmDir = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "esm");

function fixFile(file) {
  const original = readFileSync(file, "utf8");
  const fromDir = dirname(file);
  // Match `from "./x"` / `from "../x"` without an extension. Append `/index.js`
  // when the specifier resolves to a directory, otherwise `.js`.
  const fixed = original.replace(
    /(from\s+["'])(\.\.?\/[^"']*?)(["'])/g,
    (match, prefix, spec, suffix) => {
      if (/\.(m?js|json)$/.test(spec)) {
        return match;
      }
      const resolved = resolve(fromDir, spec);
      if (existsSync(resolved) && statSync(resolved).isDirectory()) {
        return `${prefix}${spec}/index.js${suffix}`;
      }
      return `${prefix}${spec}.js${suffix}`;
    }
  );
  if (fixed !== original) {
    writeFileSync(file, fixed);
  }
}

function walk(dir) {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      walk(full);
    } else if (full.endsWith(".js")) {
      fixFile(full);
    }
  }
}

// Mark dist/esm as ESM so Node treats the .js files as modules without the
// MODULE_TYPELESS_PACKAGE_JSON reparse warning (the package root stays CJS).
writeFileSync(join(esmDir, "package.json"), '{\n  "type": "module"\n}\n');

walk(esmDir);
