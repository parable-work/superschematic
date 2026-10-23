// Package packages carries the core authoring packages' TypeScript sources
// into the binary, so `superschematic catalog decorators` prints each
// decorator's declaration and the type wrappers from the sources the
// schemas it builds import.
package packages

import "embed"

// Sources holds each package's src/*.ts files, at api/src, db/src,
// schema/src, schema-config/src and stack/src.
//
//go:embed api/src/*.ts db/src/*.ts schema/src/*.ts schema-config/src/*.ts stack/src/*.ts
var Sources embed.FS
