# acme-schematic

Placeholder. This directory will hold the downstream example: a schemas root
with a DB, an API and a General service, its own `superschematic.toml`
(`@acme` scope, `example.com/acme` module root), and an extension that
registers one kind, one decorator and one auth provider, built by a binary
that links the extension. Its smoke script becomes a CI job, and adding a
second decorator to the extension must change nothing outside this
directory.

Until it lands, the fixture services under
`internal/loader/tsreader/testdata/services` (`fixture-db`, `fixture-api`,
`fixture-general`) are the runnable examples; `make cli-smoke` builds them.
