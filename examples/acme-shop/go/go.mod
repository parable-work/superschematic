module example.com/acme/shop

go 1.26.4

require (
	example.com/acme/api/shop-api v0.0.0-00010101000000-000000000000
	example.com/acme/api/shop-orders v0.0.0-00010101000000-000000000000
	example.com/acme/orm/shop-db v0.0.0-00010101000000-000000000000
	example.com/acme/sdk/go/shop-api v0.0.0-00010101000000-000000000000
	example.com/acme/sdk/go/shop-orders v0.0.0-00010101000000-000000000000
	example.com/acme/types/go/shop-api v0.0.0
	example.com/acme/types/go/shop-common v0.0.0-00010101000000-000000000000
	example.com/acme/types/go/shop-db v0.0.0
	example.com/acme/types/go/shop-orders v0.0.0
	github.com/go-chi/chi/v5 v5.3.2
	github.com/jackc/pgx/v5 v5.11.0
	github.com/parable-work/superscalar/go v1.0.0
	github.com/parable-work/superschematic/runtime/http/go v0.0.0-00010101000000-000000000000
	go.uber.org/zap v1.28.0
	modernc.org/sqlite v1.60.1
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-chi/httprate v0.16.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/joho/godotenv v1.5.1 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/parable-work/superschematic/ir v0.0.0 // indirect
	github.com/parable-work/superschematic/runtime/schema/go v0.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/zeebo/xxh3 v1.0.2 // indirect
	go.opentelemetry.io/otel v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

// The generated modules under ../schemas/dist, which scripts/check.sh
// builds, and the runtimes they import from this checkout. A project that
// consumes published modules keeps only the replace lines for its own
// generated modules, or none once those are published too.
replace (
	example.com/acme/api/shop-api => ../schemas/dist/api/shop-api
	example.com/acme/api/shop-orders => ../schemas/dist/api/shop-orders
	example.com/acme/orm/shop-db => ../schemas/dist/orm/shop-db
	example.com/acme/sdk/go/shop-api => ../schemas/dist/sdk/go/shop-api
	example.com/acme/sdk/go/shop-orders => ../schemas/dist/sdk/go/shop-orders
	example.com/acme/types/go/shop-api => ../schemas/dist/types/go/shop-api
	example.com/acme/types/go/shop-common => ../schemas/dist/types/go/shop-common
	example.com/acme/types/go/shop-db => ../schemas/dist/types/go/shop-db
	example.com/acme/types/go/shop-orders => ../schemas/dist/types/go/shop-orders
	github.com/parable-work/superscalar/go => ../../../third_party/superscalar/go
	github.com/parable-work/superschematic/ir => ../../../ir
	github.com/parable-work/superschematic/runtime/http/go => ../../../runtime/http/go
	github.com/parable-work/superschematic/runtime/schema/go => ../../../runtime/schema/go
)
