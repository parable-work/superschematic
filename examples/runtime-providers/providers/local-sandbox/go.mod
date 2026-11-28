module example.com/runtime-providers/local-sandbox

go 1.27.3

require (
	github.com/parable-work/superschematic/runtime/http/go v0.0.0
	go.uber.org/zap v1.28.0
)

require (
	github.com/go-chi/chi/v5 v5.4.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/parable-work/superscalar/go v0.0.0-20261127170532-5fe84bd97aa3 // indirect
	github.com/parable-work/superschematic/ir v0.0.0 // indirect
	github.com/parable-work/superschematic/runtime/schema/go v0.0.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
)

// The runtime modules of this checkout. A server that links the provider
// replaces them itself, since Go takes no replace from a module it
// requires.
replace (
	github.com/parable-work/superscalar/go => ../../../../third_party/superscalar/go
	github.com/parable-work/superschematic/ir => ../../../../ir
	github.com/parable-work/superschematic/runtime/http/go => ../../../../runtime/http/go
	github.com/parable-work/superschematic/runtime/schema/go => ../../../../runtime/schema/go
)
