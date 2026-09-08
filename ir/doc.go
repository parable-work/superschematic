// Package ir defines the format-agnostic intermediate representation (IR) for
// schema documents.
//
// This is the breaking restructure of the original IR (which lives in the v1
// tool at ir/). It lives at psgen's canonical package
// path with no version suffix -- the version is the tool, not a package path.
// The v1 IR and all of its consumers stay untouched during the migration
// window; this package takes over the canonical schema-ir path in the flip PR
// that deletes the GraphQL toolchain end-to-end.
//
// Compared with the v1 IR, this package:
//
//   - Drops GraphQL-legacy concepts: RootType, the Queries/Mutations split
//     (replaced by named [OperationSet] entries), builtin scalars, GraphQL-safe
//     scalar names, the Object/Input TypeKind split (replaced by [Role]),
//     custom JSON tags, the [T!] element-nullability distinction, and
//     wildcard imports.
//   - Adds the TypeScript-shaped concepts the schema language needs:
//     [TypeDef.Role], pre-flattened inheritance ([TypeDef.Extends] with
//     [FieldDef.InheritedFrom]), traits ([TypeDef.Implements],
//     [TypeDef.IsTrait], [TypeDef.TraitConfig]), raw heritage clauses
//     ([RawHeritage]), verified cross-layer projections ([SourceRef],
//     [FieldDef.Virtual]), node-attached comments (Comment fields are IR
//     metadata and round-trip through every format), language primitives
//     ([LanguagePrimitive]), and extension-owned data in the open
//     Extensions slots ([Schema.Extensions], [TypeDef.Extensions],
//     [FieldDef.Extensions], [OperationSet.Extensions]).
//
// The TypeScript walker, the JSON reader, and the YAML reader all produce this
// one shape. The on-disk JSON and YAML representations use the struct tags in
// this package verbatim.
package ir
