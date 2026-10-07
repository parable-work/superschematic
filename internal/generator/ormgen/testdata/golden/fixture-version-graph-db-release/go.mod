module example.com/schemas/orm/fixture-version-graph-db

go 1.26.4

require (
	example.com/schemas/types/go/fixture-version-graph-db v0.0.0-00010101000000-000000000000
	github.com/jackc/pgx/v5 v5.11.0
	github.com/parable-work/superscalar/go v0.0.0-20260928143325-10cf493f485e
	github.com/parable-work/superschematic/ir v1.2.3
	github.com/parable-work/superschematic/runtime/versiongraph/go v1.2.3
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

replace example.com/schemas/types/go/fixture-version-graph-db => ../../types/go/fixture-version-graph-db

// The release of superschematic that generated this module pins the
// runtime modules it reaches, which the module proxy serves, whatever
// version the modules it requires ask for.
replace (
	github.com/parable-work/superscalar/go => github.com/parable-work/superscalar/go v0.0.0-20260928143325-10cf493f485e
	github.com/parable-work/superschematic/ir => github.com/parable-work/superschematic/ir v1.2.3
	github.com/parable-work/superschematic/runtime/versiongraph/go => github.com/parable-work/superschematic/runtime/versiongraph/go v1.2.3
)
