module example.com/acme/schematic

go 1.26.4

require (
	github.com/microsoft/typescript-go v0.0.0
	github.com/parable-work/superschematic v0.0.0
	github.com/parable-work/superschematic/ir v0.0.0
	github.com/spf13/cobra v1.10.2
)

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/go-json-experiment/json v0.0.0-20260623181947-01eb4420fa68 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/parable-work/superscalar/go v0.0.0-20260924135511-79a8e6a73504 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.2 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// The example builds against the checkout it lives in. A real extension
// requires github.com/parable-work/superschematic at a tag and drops these.
replace github.com/parable-work/superschematic => ../..

replace github.com/parable-work/superschematic/ir => ../../ir

// replace directives do not propagate across modules, so the core's pins
// are repeated here. Keep them equal to the root go.mod.
replace github.com/microsoft/typescript-go => github.com/parable-work/typescript-go v0.0.0-20260701192534-c5de5679073f
