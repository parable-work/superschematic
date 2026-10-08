module github.com/parable-work/superschematic/runtime/schema/go

go 1.26.4

require (
	github.com/parable-work/superscalar/go v0.0.0-20261006180731-8bb3cbb31da5
	github.com/parable-work/superschematic/ir v0.0.0
	github.com/stretchr/testify v1.12.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/google/uuid v1.6.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)

replace github.com/parable-work/superschematic/ir => ../../../ir
