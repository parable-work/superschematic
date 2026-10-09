// Type-level checks of outputs.ci (docs/stack-model.md, section 11.3, D47).
// Nothing imports this file: `bun run typecheck`, which `make ts` runs,
// checks it, and fails on an @ts-expect-error that no longer errors.
import { defineConfig, SchemaKind } from "./index";

export const github = defineConfig({
  name: "shop-stack",
  kind: SchemaKind.Stack,
  outputs: { ci: { github: { branch: "main", install: ".github/workflows" } } }
});

// A renderer an extension registers takes the same options.
export const extensionRenderer = defineConfig({
  name: "shop-stack",
  kind: SchemaKind.Stack,
  outputs: { ci: { gitlab: {} } }
});

export const refusedBranch = defineConfig({
  name: "shop-stack",
  kind: SchemaKind.Stack,
  // @ts-expect-error a branch is a string
  outputs: { ci: { github: { branch: 1 } } }
});

export const refusedOption = defineConfig({
  name: "shop-stack",
  kind: SchemaKind.Stack,
  // @ts-expect-error a renderer takes branch and install only
  outputs: { ci: { github: { brnach: "main" } } }
});
