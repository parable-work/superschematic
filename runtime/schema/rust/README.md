# Rust schema runtime

`superschematic-schema-runtime` is what the validators in a generated Rust
types crate share. A generated `validators.rs` checks a JSON value against
its schema type the way the generated Go, TypeScript and Python validators
do (D14): presence, then the JSON type, then the type's rules, with one
error per failing value at a path such as `field`, `field[i]` or
`field.key`. The rules are generated; this crate holds the pieces every
such module calls:

- `ValidationErrors`, the path-keyed error map. It serializes as
  superscalar's Go `ValidationErrors` marshals: sorted keys, each holding
  `[{validator, message}]` or a nested map for a nested object.
  `flatten()` gives every error under one dotted path, as the
  cross-language parity corpus spells them.
- `expect_string`, `expect_number`, `expect_integer`, `expect_boolean`,
  `expect_list` and `expect_object`: one `type` error ("expected a
  string", ...) for a present value of another JSON type, and nothing for
  a missing or null one, which is the presence check's to report.
- `code_points`: lengths count Unicode code points.
- `Pattern`: a `pattern` rule compiled once. `\d`, `\w`, `\s` and `\b` are
  read as ASCII, as Go's RE2 reads them, where the `regex` crate would read
  them as Unicode.
- `index_path`, `key_path` and `bracket_key_path`.

Unlike the Go, TypeScript and Python schema runtimes, it reads no IR and
does not assert `../testdata/validation_parity.json`: the generated Rust
validators run that corpus in `internal/generator/parity`, as the generated
Go, TypeScript and Python ones do.

The crate does not depend on superscalar. A generated validator calls the
scalar crate the naming file names (`scalar_rust_crate`, and
`scalar_rust_registry` for its registry) for the checks only the scalar
core makes.

superscalar turns on serde_json's `arbitrary_precision` and
`preserve_order` features, and Cargo unifies them into every crate of a
build that uses it. Nothing here depends on them: the serializers are
written by hand and number checks read `serde_json::Number` through its
accessors. `make rust` runs the tests with and without both features.

```sh
cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test
cargo test --features serde_json/arbitrary_precision,serde_json/preserve_order
```
