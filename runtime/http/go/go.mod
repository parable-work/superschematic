module github.com/parable-work/superschematic/runtime/http/go

go 1.26.4

require (
	github.com/go-chi/chi/v5 v5.3.2
	github.com/go-chi/httprate v0.16.0
	github.com/parable-work/superschematic/runtime/schema/go v0.0.0
	github.com/stretchr/testify v1.12.1
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/otel/sdk v1.46.0
	go.opentelemetry.io/otel/trace v1.46.0
	go.uber.org/zap v1.28.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/parable-work/superscalar/go v0.0.0-20260924221754-1be340a36ae1 // indirect
	github.com/parable-work/superschematic/ir v0.0.0 // indirect
	github.com/zeebo/xxh3 v1.0.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/parable-work/superschematic/runtime/schema/go => ../../schema/go

replace github.com/parable-work/superschematic/ir => ../../../ir
