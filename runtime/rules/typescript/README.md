# The behaviors' core (TypeScript)

`@superschematic/rules` is the pure core of the behaviors (D103 in
`docs/DECISIONS.md`) for TypeScript: `superschematic-rules` with every
behavior ported onto it (`superschematic-rules-behaviors`), built for
`wasm32-unknown-unknown` from `runtime/engine/rust/rules-wasm`, and run over
its JSON contract (`src/contract.ts`; `runtime/engine/rust/rules/README.md`
is its prose). It runs in Bun, Node, Workers and a browser, has no
dependencies, and drives the module through its C ABI (`rules_alloc`,
`rules_<op>`, `rules_free`, `rules_dealloc`) with no generated glue, as
`@superschematic/versiongraph` drives its own.

The core is pure: it opens no connection, reads no clock and makes no id. A
host runs a command by calling `run` with the answers it has, answering the
reads and taking the locks a step asks for, and running it again until the
step is done. The HTTP runtime's adapter (`@superschematic/http-runtime`,
`src/rules.ts`) is that host over a database; a generated `orm.ts` passes it
this package's core.

```ts
import { init } from '@superschematic/rules';

const core = await init();
const order = { name: 'Order', uses: [{ behavior: 'Workflow', config, anchor: { field: 'status' } }] };
const step = core.run(order, { caller: { subject: 'sam' }, now: Date.now(), request: 'r1', action: { kind: 'create', id: 'o1', own: { title: 'Desk' } } });
// { done: { changed: { rows: [{ kind: 'create', type: 'Order', key: 'o1', own: { title: 'Desk' }, owned: { status: { status: 'draft', enteredAt: ... } } }], ... } } }
```

- `init(source?, options?)` compiles and instantiates the module. `source`
  is its bytes, a URL (a `file:` URL is read from disk under Bun and Node;
  any other is fetched), a `Response` or a promise of one, or a compiled
  `WebAssembly.Module`. Without it, `init` loads
  `superschematic_rules_wasm.wasm` (`WASM_FILE`) from next to `index.js`,
  which a bundler that understands `new URL(..., import.meta.url)` copies
  into the build; the file is also exported as
  `@superschematic/rules/superschematic_rules_wasm.wasm`.
- `initSync(source?, options?)` does the same without awaiting, from the
  module's bytes or a compiled module, or, under Bun and Node, the bundled
  file through the `node:fs` that `process.getBuiltinModule` returns. The
  generated `orm.ts` passes it to the runtime, which calls it when a
  repository first runs a behavior's command.
- `run(spec, command, answers?)` returns one step: `{ read }`, `{ lock }`
  or `{ done: { refused } | { changed } }`. A refusal is a step, not an
  error.
- `declarations()` returns each registered behavior's declaration file,
  each field's column type and the value field among them.
- `check(specs, { caller, now }?)` returns each spec's define check: null,
  or its refusal.
- An input the module cannot read throws `RulesError` (`invalid_json`,
  `invalid_input`). A trap, a panic in the core, throws `RulesError`
  (`internal`), and the next call runs on a fresh instance of the module.
- `options.parse` and `options.stringify` replace the JSON codec, for values
  whose numbers do not fit a double (the core keeps a number's text).

## Building and testing

```
bun install
bun run build   # tsc, then scripts/build-wasm.mjs
bun run test    # build, typecheck the tests, bun test test, node test/node.mjs
```

`scripts/build-wasm.mjs` builds `superschematic-rules-wasm` with the engine
workspace's `dist` profile, with the toolchain `tools.env` names and the
`wasm32-unknown-unknown` target, and copies the module into `dist/`. No
prebuilt module is published yet (D82), so the build needs cargo.
`test/contract.test.ts` holds `src/contract.ts` to the module's real JSON,
`test/load.test.ts` every source `init` and `initSync` take, and
`test/node.mjs` that the package loads in Node and its entry imports
nothing.
