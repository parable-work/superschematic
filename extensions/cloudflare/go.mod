module github.com/parable-work/superschematic/extensions/cloudflare

go 1.26.4

require (
	github.com/parable-work/superschematic v0.0.0
	github.com/parable-work/superschematic/ir v0.0.0
)

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/parable-work/superscalar/go v0.0.0-20261006180731-8bb3cbb31da5 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	golang.org/x/text v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// The extension builds against the checkout it lives in, as the other
// modules do (D1).
replace github.com/parable-work/superschematic => ../..

replace github.com/parable-work/superschematic/ir => ../../ir

// replace directives do not propagate across modules, so the core's pins
// are repeated here. Keep them equal to the root go.mod.
replace github.com/microsoft/typescript-go => github.com/parable-work/typescript-go v0.0.0-20260701192534-c5de5679073f
