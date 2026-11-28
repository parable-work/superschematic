module github.com/parable-work/superschematic/runtime/schema/go

go 1.27.3

require (
	github.com/parable-work/superscalar/go v0.0.0-20261119204639-51d50302d5dd
	github.com/parable-work/superschematic/ir v0.0.0
	github.com/stretchr/testify v1.12.1
	go.yaml.in/yaml/v3 v3.0.5
)

require github.com/google/uuid v1.6.0 // indirect

replace github.com/parable-work/superschematic/ir => ../../../ir
