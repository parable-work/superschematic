package sqlmigrate

import "errors"

// errNotImplemented marks an entry point whose implementation lands in a
// later change. BuildModel (dialect.go) and Diff (diff.go) are built; the
// stubs in stubs_cli.go still return it.
var errNotImplemented = errors.New("sqlmigrate: not implemented")
