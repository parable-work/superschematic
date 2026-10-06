module example.com/fakeserver

go 1.26.4

require github.com/parable-work/superschematic/runtime/http/go v0.0.0

// The integration test rewrites these to the checkout's absolute paths when
// it copies the module under its output root.
replace github.com/parable-work/superschematic/runtime/http/go => ../../../../../runtime/http/go

replace github.com/parable-work/superschematic/runtime/schema/go => ../../../../../runtime/schema/go

replace github.com/parable-work/superschematic/ir => ../../../../../ir
