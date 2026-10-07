module example.com/schemas/types/go/fixture-api

go 1.26.4

require (
	github.com/google/uuid v1.6.0
	github.com/parable-work/superscalar/go v0.0.0-20260928143325-10cf493f485e
	github.com/parable-work/superschematic/ir v1.2.3
	gopkg.in/yaml.v3 v3.0.1
)

// The release of superschematic that generated this module pins the
// runtime modules it reaches, which the module proxy serves, whatever
// version the modules it requires ask for.
replace (
	github.com/parable-work/superscalar/go => github.com/parable-work/superscalar/go v0.0.0-20260928143325-10cf493f485e
	github.com/parable-work/superschematic/ir => github.com/parable-work/superschematic/ir v1.2.3
)

replace example.com/schemas/types/go/fixture-db => ../fixture-db
