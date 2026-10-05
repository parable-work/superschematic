package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestServiceCallersRefusedInIR: a schema authored as IR is held to the
// rules the TypeScript reader applies at the decorator. Auth stands for an
// Authenticated set, which the IR has already folded into its operations;
// an @publicRoute operation takes no clause from its set.
func TestServiceCallersRefusedInIR(t *testing.T) {
	require := &ir.ServiceCallers{Mode: ir.ServiceCallersRequire, From: []string{"orders-api"}}
	allow := &ir.ServiceCallers{Mode: ir.ServiceCallersAllow}
	schema := ir.NewSchema("stock", ir.SchemaKindAPI)
	schema.OperationSets = []*ir.OperationSet{
		{
			Name: "StockOperations",
			Operations: []*ir.FieldDef{
				{Name: "reserve", ServiceCallers: require, Permissions: []string{"stock.reserve"}},
				{Name: "reindex", ServiceCallers: require},
				{Name: "release", ServiceCallers: allow, RequireOwnership: true},
				{Name: "open", ServiceCallers: allow},
				{Name: "public", ServiceCallers: require, Public: true},
				{Name: "hook", ServiceCallers: allow, Auth: true, Webhook: true},
				{Name: "signed", ServiceCallers: require, HMACVerifiedProvider: "stripe"},
				{Name: "odd", ServiceCallers: &ir.ServiceCallers{Mode: "sometimes"}},
			},
		},
		{
			Name:           "LedgerQueries",
			ServiceCallers: allow,
			Operations: []*ir.FieldDef{
				{Name: "list", Auth: true},
				{Name: "anyone"},
				{Name: "health", Public: true},
				{Name: "own", ServiceCallers: require},
			},
		},
		{
			Name:           "HookOperations",
			ServiceCallers: require,
			Operations:     []*ir.FieldDef{{Name: "receive", Webhook: true}},
		},
		{
			Name:           "OddOperations",
			ServiceCallers: &ir.ServiceCallers{Mode: "never"},
			Operations:     []*ir.FieldDef{{Name: "x"}},
		},
	}
	r := &Result{}
	checkServiceCallers(schema, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	want := []string{
		"StockOperations.open: @allowService needs a user clause: @auth, an Authenticated set, @requirePermission or @requireOwnership; an operation only services call is @requireService",
		"StockOperations.public: @requireService contradicts @publicRoute: a public route needs no caller",
		"StockOperations.hook: @allowService contradicts @webhook: a third party calls it, and holds no service credential",
		"StockOperations.signed: @requireService contradicts @hmacVerified: a third party calls it, and holds no service credential",
		`StockOperations.odd: service clause mode "sometimes" is not require or allow`,
		"LedgerQueries.anyone: the set's @allowService needs a user clause: @auth, an Authenticated set, @requirePermission or @requireOwnership; an operation only services call is @requireService",
		"HookOperations.receive: the set's @requireService contradicts @webhook: a third party calls it, and holds no service credential",
		`OddOperations: service clause mode "never" is not require or allow`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors =\n%q\nwant\n%q", got, want)
	}
}

// TestServiceCallersConflictMarksTheSetsClause: the walker points a
// conflict at the method's own decorator, or at the method when the clause
// is its set's, and reads which from FromSet.
func TestServiceCallersConflictMarksTheSetsClause(t *testing.T) {
	set := &ir.OperationSet{Name: "S", ServiceCallers: &ir.ServiceCallers{Mode: ir.ServiceCallersAllow}}
	inherited := &ir.FieldDef{Name: "a"}
	conflict, ok := ServiceCallersConflict(set, inherited)
	if !ok || !conflict.FromSet || conflict.Rule != "the set's @allowService" {
		t.Errorf("inherited conflict = %+v, %v", conflict, ok)
	}
	own := &ir.FieldDef{Name: "b", ServiceCallers: &ir.ServiceCallers{Mode: ir.ServiceCallersAllow}}
	conflict, ok = ServiceCallersConflict(set, own)
	if !ok || conflict.FromSet || conflict.Rule != "@allowService" {
		t.Errorf("own conflict = %+v, %v", conflict, ok)
	}
	if _, ok := ServiceCallersConflict(set, &ir.FieldDef{Name: "c", Public: true}); ok {
		t.Error("an @publicRoute operation takes no clause from its set, so nothing conflicts")
	}
	if _, ok := ServiceCallersConflict(nil, &ir.FieldDef{Name: "d"}); ok {
		t.Error("an operation without a clause has no conflict")
	}
}
