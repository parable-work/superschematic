module github.com/parable-work/superschematic/runtime/engine/testdata/linkcheck

go 1.26.4

require (
	github.com/parable-work/superscalar/go v0.0.0-20261021190928-62b300e78ba9
	github.com/parable-work/superschematic/runtime/engine/go v0.0.0
	github.com/parable-work/superschematic/runtime/versiongraph/go v0.0.0
)

require github.com/google/uuid v1.6.0 // indirect

replace github.com/parable-work/superschematic/runtime/engine/go => ../../go

replace github.com/parable-work/superschematic/runtime/versiongraph/go => ../../../versiongraph/go
