package ir

import "testing"

// TestEffectiveServiceCallers: an operation's own clause replaces its
// set's, an @publicRoute operation takes none from its set, and an
// operation without either has none.
func TestEffectiveServiceCallers(t *testing.T) {
	own := &ServiceCallers{Mode: ServiceCallersAllow}
	inherited := &ServiceCallers{Mode: ServiceCallersRequire, From: []string{"orders-api"}}
	set := &OperationSet{Name: "Stock", ServiceCallers: inherited}
	for _, tc := range []struct {
		name string
		set  *OperationSet
		op   *FieldDef
		want *ServiceCallers
	}{
		{"own replaces the set's", set, &FieldDef{ServiceCallers: own, Auth: true}, own},
		{"the set's", set, &FieldDef{}, inherited},
		{"public takes none from its set", set, &FieldDef{Public: true}, nil},
		{"public keeps its own", set, &FieldDef{Public: true, ServiceCallers: own}, own},
		{"no set", nil, &FieldDef{}, nil},
		{"no operation", set, nil, nil},
	} {
		if got := EffectiveServiceCallers(tc.set, tc.op); got != tc.want {
			t.Errorf("%s: EffectiveServiceCallers = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// TestHasUserClause: @auth, @requirePermission and @requireOwnership each
// make a user clause; @publicRoute and a service clause do not.
func TestHasUserClause(t *testing.T) {
	for _, tc := range []struct {
		op   FieldDef
		want bool
	}{
		{FieldDef{Auth: true}, true},
		{FieldDef{Permissions: []string{"stock.write"}}, true},
		{FieldDef{RequireOwnership: true}, true},
		{FieldDef{Public: true}, false},
		{FieldDef{ServiceCallers: &ServiceCallers{Mode: ServiceCallersRequire}}, false},
	} {
		if got := tc.op.HasUserClause(); got != tc.want {
			t.Errorf("HasUserClause(%+v) = %v, want %v", tc.op, got, tc.want)
		}
	}
}
