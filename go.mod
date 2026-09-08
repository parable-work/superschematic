module github.com/parable-work/superschematic

go 1.26.4

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/invopop/jsonschema v0.14.0
	github.com/microsoft/typescript-go v0.0.0
	github.com/parable-work/superscalar/go v0.0.0-20260908181317-c9da122f99b8
	github.com/parable-work/superschematic/ir v0.0.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.12.1
	golang.org/x/text v0.41.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/go-json-experiment/json v0.0.0-20260623181947-01eb4420fa68 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.2 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/parable-work/superschematic/ir => ./ir

// Project Corsa (TypeScript 7.0 Go compiler), pinned fork exposing the shim
// packages the tsreader frontend programs against. Upgraded in dedicated PRs.
replace github.com/microsoft/typescript-go => github.com/parable-work/typescript-go v0.0.0-20260701192534-c5de5679073f
