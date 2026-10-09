module example.com/schemas/sdk/go/fixture-api

go 1.26.4

require example.com/schemas/types/go/fixture-api v0.0.0
require github.com/parable-work/superschematic/runtime/http/go v1.2.3
replace example.com/schemas/types/go/fixture-api => ../../../types/go/fixture-api

// The release of superschematic that generated this module pins the
// runtime modules it reaches, which the module proxy serves, whatever
// version the modules it requires ask for.
replace (
	github.com/parable-work/superscalar/go => github.com/parable-work/superscalar/go v0.0.0-20260928143325-10cf493f485e
	github.com/parable-work/superschematic/ir => github.com/parable-work/superschematic/ir v1.2.3
	github.com/parable-work/superschematic/runtime/http/go => github.com/parable-work/superschematic/runtime/http/go v1.2.3
	github.com/parable-work/superschematic/runtime/schema/go => github.com/parable-work/superschematic/runtime/schema/go v1.2.3
)
