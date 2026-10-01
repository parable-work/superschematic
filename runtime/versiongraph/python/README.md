# Version graph (Python)

`superschematic-versiongraph` (module `superschematic_versiongraph`) is the
version-graph core (`runtime/versiongraph/rust`) for Python: a PyO3
extension module over the crate, built with maturin, with typed `compose`,
`merge`, `diff`, `content_hash` and `validate` over its JSON contract
(`runtime/versiongraph/README.md`, D19 in `docs/DECISIONS.md`). It runs on
CPython 3.9 and newer; one abi3 wheel per platform serves them all.

| Path | Holds |
| --- | --- |
| `src/lib.rs` | The crate `superschematic-versiongraph-python`: the extension module `superschematic_versiongraph._native`, whose `run(operation, input)` takes the JSON input as bytes and returns `(ok, output)` with the core's output or error document. It calls the core natively, with the GIL released. PyO3 is its dependency, not the core's. |
| `superschematic_versiongraph/__init__.py` | `VersionGraph`, the module-level operations, `run` and `VersionGraphError`. |
| `superschematic_versiongraph/contract.py` | The contract's types, as `TypedDict`s and `Literal`s, every member named as the contract names it. |
| `tests/` | Every core vector through the package, and the binding's own tests. |

## Use

```python
import superschematic_versiongraph as vg

result = vg.compose({"descriptor": descriptor, "base": base, "overlay": overlay})
tree, findings = result["tree"], result["findings"]
merged = vg.merge({"descriptor": descriptor, "base": base, "ours": ours, "theirs": theirs})
changes = vg.diff({"descriptor": descriptor, "from": base, "to": tree})["changes"]
content_hash = vg.content_hash({"descriptor": descriptor, "tree": tree})["contentHash"]

try:
    vg.validate({"descriptor": descriptor, "tree": {"recipe_step": []}})
except vg.VersionGraphError as error:
    print(error.code)  # "unknown_kind"
```

- The operations are synchronous. Inputs and outputs are plain dicts and
  lists, keyed as the contract names them; `contract.py` types them.
- A refused input raises `VersionGraphError` with the contract's `code` and
  the core's `message`.
- `run(operation, json)` takes JSON text or bytes and returns the output
  document as text, for a caller with its own JSON handling. An operation
  the core does not have raises `ValueError`.
- The module-level functions use `json`, which keeps an integer's digits
  but reads a number with a fraction or an exponent as a float. For rows
  whose numbers a double does not hold, build a
  `VersionGraph(loads=..., dumps=...)` with a codec that keeps them, or use
  `run`.

## Development

```
cd runtime/versiongraph/python
uv run pytest -q                            # builds the extension with maturin, then the tests
uv run --python 3.9 --isolated pytest -q    # the same on the 3.9 floor
make python                                 # from the repository root: both, with cargo fmt and clippy
```

uv builds the package with maturin into `.venv` and rebuilds it when the
binding's or the core's Rust sources change (`[tool.uv] cache-keys`). The
build needs cargo. `tests/test_vectors.py` runs every vector in
`runtime/versiongraph/testdata/vectors` as JSON text through `run` and
through the typed operations with an exact codec, comparing each output
with the vector's `expect`, member order and number digits included, and
through the module-level operations when `json` reads the vector without
loss. It also checks every vector's input and output against the types
in `contract.py` and fails when a member or literal there appears in no
vector. `tests/test_binding.py` checks the errors, that many calls do not
grow the process, and that the core runs with the GIL released.

The reference page is "Version graphs" in the docs site
(`docs/src/content/docs/reference/version-graphs.md`).
