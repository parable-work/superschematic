package codegen

import (
	"reflect"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

func TestTypeDependencies(t *testing.T) {
	db := &ir.Schema{
		Name:    "shop-db",
		Kind:    ir.SchemaKindDB,
		Scalars: map[string]*ir.ScalarDef{"Identity.UUID": {Name: "Identity.UUID"}},
		Enums:   map[string]*ir.EnumDef{"OrderStatus": {Name: "OrderStatus"}},
		Types: map[string]*ir.TypeDef{
			"Order":     {Name: "Order", Role: ir.RoleDBTable},
			"Auditable": {Name: "Auditable", Role: ir.RoleTrait, IsTrait: true},
		},
	}
	common := &ir.Schema{
		Name:    "shop-common",
		Kind:    ir.SchemaKindGeneral,
		Scalars: map[string]*ir.ScalarDef{"Money.Cents": {Name: "Money.Cents"}},
		Enums:   map[string]*ir.EnumDef{"Currency": {Name: "Currency"}},
	}
	scalarsOnly := &ir.Schema{
		Name:    "shop-scalars",
		Kind:    ir.SchemaKindGeneral,
		Scalars: map[string]*ir.ScalarDef{"Geo.Point": {Name: "Geo.Point"}},
	}
	deps := map[string]*ir.Schema{"shop-db": db, "shop-common": common, "shop-scalars": scalarsOnly}

	cases := []struct {
		name    string
		schema  *ir.Schema
		want    []string
		comment string
	}{
		{
			name: "a DB enum and a General type",
			schema: &ir.Schema{Imports: []ir.Import{
				{Package: "@schemas/shop-db", Types: []string{"OrderStatus"}},
				{Package: "@schemas/shop-common", Types: []string{"Currency"}},
			}},
			want: []string{"shop-common", "shop-db"},
		},
		{
			name:   "a DB table",
			schema: &ir.Schema{Imports: []ir.Import{{Package: "@schemas/shop-db", Types: []string{"Order"}}}},
			want:   []string{"shop-db"},
		},
		{
			name:    "a DB scalar and a DB trait import nothing",
			schema:  &ir.Schema{Imports: []ir.Import{{Package: "@schemas/shop-db", Types: []string{"Identity.UUID", "Auditable"}}}},
			comment: "a DB dependency contributes only the named symbols",
		},
		{
			name:    "a General scalar brings the dependency's catalog",
			schema:  &ir.Schema{Imports: []ir.Import{{Package: "@schemas/shop-common", Types: []string{"Money.Cents"}}}},
			want:    []string{"shop-common"},
			comment: "the Currency enum is re-exported",
		},
		{
			name:   "a dependency with only scalars",
			schema: &ir.Schema{Imports: []ir.Import{{Package: "@schemas/shop-scalars", Types: []string{"Geo.Point"}}}},
		},
		{
			name: "a local definition shadows the imported one",
			schema: &ir.Schema{
				Enums:   map[string]*ir.EnumDef{"OrderStatus": {Name: "OrderStatus"}},
				Imports: []ir.Import{{Package: "@schemas/shop-db", Types: []string{"OrderStatus"}}},
			},
		},
		{
			name:    "an import of a service that is not loaded",
			schema:  &ir.Schema{Imports: []ir.Import{{Package: "@schemas/shop-missing", Types: []string{"Thing"}}}},
			comment: "left to the generators, which fail on it",
		},
		{
			name:   "no imports",
			schema: &ir.Schema{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TypeDependencies(tc.schema, deps)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("TypeDependencies = %v, want %v (%s)", got, tc.want, tc.comment)
			}
		})
	}
}
