package sqlutil

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// IdempotencyTableOf names the table that keeps schema's idempotency keys
// and its columns, as the SQL generator writes them (D140). It is an
// error for a schema that declares no such table, or one the loader did
// not expand.
func IdempotencyTableOf(schema *ir.Schema) (*ir.IdempotencyTable, error) {
	td := schema.IdempotencyKeysTable()
	if td == nil {
		return nil, fmt.Errorf("%s declares no table that implements %s", schema.Name, ir.IdempotencyKeysTraitName)
	}
	var missing string
	column := func(name string) string {
		for _, field := range td.Fields {
			if field.Name == name && field.Origin == ir.OriginIdempotency {
				return ColumnName(field)
			}
		}
		if missing == "" {
			missing = name
		}
		return ""
	}
	names := &ir.IdempotencyTable{
		Table:       TableName(td),
		Lookup:      column(ir.IdempotencyLookupField),
		Operation:   column(ir.IdempotencyOperationField),
		Principal:   column(ir.IdempotencyPrincipalField),
		Key:         column(ir.IdempotencyKeyField),
		RequestHash: column(ir.IdempotencyRequestHashField),
		State:       column(ir.IdempotencyStateField),
		Status:      column(ir.IdempotencyStatusField),
		Body:        column(ir.IdempotencyBodyField),
		StartedAt:   column(ir.IdempotencyStartedAtField),
		ExpiresAt:   column(ir.IdempotencyExpiresAtField),
	}
	if missing != "" {
		return nil, fmt.Errorf("%s.%s has no field %s the loader adds", schema.Name, td.Name, missing)
	}
	return names, nil
}
