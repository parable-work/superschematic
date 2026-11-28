module example.com/schemas/types/go/fixture-variant-field-api

go 1.27.3

require (
	github.com/google/uuid v1.6.0
	github.com/parable-work/superscalar/go v1.0.0
	github.com/parable-work/superschematic/ir v0.0.0-00010101000000-000000000000
	gopkg.in/yaml.v3 v3.0.1
	example.com/schemas/types/go/fixture-variant-field-db v0.0.0-00010101000000-000000000000
)

replace github.com/parable-work/superscalar/go => ../scalars/go

replace github.com/parable-work/superschematic/ir => ../ir

replace example.com/schemas/types/go/fixture-variant-field-db => ../fixture-variant-field-db
