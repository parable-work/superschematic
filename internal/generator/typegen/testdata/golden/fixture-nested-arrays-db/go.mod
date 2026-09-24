module example.com/schemas/types/go/fixture-nested-arrays-db

go 1.26.4

require (
	github.com/google/uuid v1.6.0
	github.com/parable-work/superscalar/go v1.0.0
	github.com/parable-work/superschematic/ir v0.0.0-00010101000000-000000000000
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/parable-work/superscalar/go => ../scalars/go

replace github.com/parable-work/superschematic/ir => ../ir
