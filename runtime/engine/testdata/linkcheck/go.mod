module github.com/parable-work/superschematic/runtime/engine/testdata/linkcheck

go 1.27.3

require (
	github.com/parable-work/superscalar/go v0.0.0-20261127170532-5fe84bd97aa3
	github.com/parable-work/superschematic/runtime/engine/go v0.0.0
	github.com/parable-work/superschematic/runtime/rules/go v0.0.0
	github.com/parable-work/superschematic/runtime/versiongraph/go v0.0.0
)

require github.com/google/uuid v1.6.0 // indirect

replace github.com/parable-work/superschematic/runtime/engine/go => ../../go

replace github.com/parable-work/superschematic/runtime/rules/go => ../../../rules/go

replace github.com/parable-work/superschematic/runtime/versiongraph/go => ../../../versiongraph/go
