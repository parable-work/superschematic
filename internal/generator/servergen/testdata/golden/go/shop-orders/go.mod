// The module of the implementation of shop-orders. superschematic wrote it
// once, with the scaffold beside it, because no go.mod held the package
// (docs/stack-model.md, section 8.5), and never writes it again: it is
// yours. Each build of a stack reads the module's path from here, so you may
// rename it, or move the package into a module of your own. The replace
// lines point at the modules the package builds from, so it builds and
// tests on its own after `go mod tidy`.

module example.com/schemas/implementation/shop-orders

go 1.26.4

require (
	example.com/schemas/api/shop-orders v0.0.0-00010101000000-000000000000
)

require (
	example.com/schemas/orm/shop-db v0.0.0-00010101000000-000000000000 // indirect
	example.com/schemas/sdk/go/shop-api v0.0.0-00010101000000-000000000000 // indirect
	example.com/schemas/types/go/shop-api v0.0.0-00010101000000-000000000000 // indirect
	example.com/schemas/types/go/shop-db v0.0.0-00010101000000-000000000000 // indirect
	example.com/schemas/types/go/shop-orders v0.0.0-00010101000000-000000000000 // indirect
	github.com/parable-work/superscalar/go v1.0.0 // indirect
	github.com/parable-work/superschematic/ir v0.0.0-00010101000000-000000000000 // indirect
	github.com/parable-work/superschematic/runtime/http/go v0.0.0-00010101000000-000000000000 // indirect
	github.com/parable-work/superschematic/runtime/schema/go v0.0.0-00010101000000-000000000000 // indirect
)

replace (
	example.com/schemas/api/shop-orders => ../../schemas/dist/api/shop-orders
	example.com/schemas/orm/shop-db => ../../schemas/dist/orm/shop-db
	example.com/schemas/sdk/go/shop-api => ../../schemas/dist/sdk/go/shop-api
	example.com/schemas/types/go/shop-api => ../../schemas/dist/types/go/shop-api
	example.com/schemas/types/go/shop-db => ../../schemas/dist/types/go/shop-db
	example.com/schemas/types/go/shop-orders => ../../schemas/dist/types/go/shop-orders
	github.com/parable-work/superscalar/go => ../../third_party/superscalar/go
	github.com/parable-work/superschematic/ir => ../../ir
	github.com/parable-work/superschematic/runtime/http/go => ../../runtime/http/go
	github.com/parable-work/superschematic/runtime/schema/go => ../../runtime/schema/go
)
