package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// encryptedArgumentSchema is an API schema with one operation, storeCard,
// whose number argument is EncryptedField<string>; edit shapes it per case.
func encryptedArgumentSchema(edit func(op *ir.FieldDef, number *ir.ArgumentDef)) *ir.Schema {
	number := &ir.ArgumentDef{Name: "number", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Encrypted: true}
	op := &ir.FieldDef{
		Name:       "storeCard",
		TypeRef:    ir.TypeRef{Name: "boolean"},
		HTTPMethod: "POST",
		RestPath:   "customers/{customerId}/cards",
		Arguments: []*ir.ArgumentDef{
			{Name: "customerId", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			number,
		},
	}
	edit(op, number)
	schema := ir.NewSchema("cards", ir.SchemaKindAPI)
	schema.OperationSets = []*ir.OperationSet{{Name: "CardMutations", Operations: []*ir.FieldDef{op}}}
	return schema
}

func encryptedArgumentErrors(schema *ir.Schema) []string {
	r := &Result{}
	checkEncryptedArguments(schema, r)
	msgs := make([]string, 0, len(r.Errors))
	for _, d := range r.Errors {
		msgs = append(msgs, d.Msg)
	}
	return msgs
}

// TestEncryptedArgumentsInTheBodyAreAccepted: an EncryptedField<T>
// argument of a POST, PUT or PATCH request body passes.
func TestEncryptedArgumentsInTheBodyAreAccepted(t *testing.T) {
	for _, method := range []string{"POST", "PUT", "PATCH", "patch"} {
		schema := encryptedArgumentSchema(func(op *ir.FieldDef, _ *ir.ArgumentDef) { op.HTTPMethod = method })
		if msgs := encryptedArgumentErrors(schema); len(msgs) != 0 {
			t.Errorf("%s: %v", method, msgs)
		}
	}
}

// TestEncryptedArgumentsOutsideTheBodyAreRefused: a query parameter, a path
// parameter the rest path names and any argument of a GET or DELETE
// operation, or of one without a method, would travel unencrypted.
func TestEncryptedArgumentsOutsideTheBodyAreRefused(t *testing.T) {
	const notBody = `CardMutations.storeCard: argument "number" is EncryptedField<T>, which only a POST, PUT or PATCH request body carries encrypted; declare one of those methods`
	for _, tc := range []struct {
		name string
		edit func(op *ir.FieldDef, number *ir.ArgumentDef)
		want string
	}{
		{"query parameter", func(_ *ir.FieldDef, number *ir.ArgumentDef) { number.IsQuery = true },
			`CardMutations.storeCard: query parameter "number" cannot be EncryptedField<T>: the query string travels outside the encrypted request body`},
		{"query operation", func(op *ir.FieldDef, _ *ir.ArgumentDef) { op.ParamType = "query" },
			`CardMutations.storeCard: query parameter "number" cannot be EncryptedField<T>: the query string travels outside the encrypted request body`},
		{"path parameter", func(op *ir.FieldDef, _ *ir.ArgumentDef) { op.RestPath = "customers/{customerId}/cards/{number}" },
			`CardMutations.storeCard: path parameter "number" cannot be EncryptedField<T>: the path travels outside the encrypted request body`},
		{"GET", func(op *ir.FieldDef, _ *ir.ArgumentDef) { op.HTTPMethod = "GET" }, notBody},
		{"DELETE", func(op *ir.FieldDef, _ *ir.ArgumentDef) { op.HTTPMethod = "DELETE" }, notBody},
		{"no method", func(op *ir.FieldDef, _ *ir.ArgumentDef) {
			op.HTTPMethod = ""
			op.ManualRouteRegistration = true
		}, notBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgs := encryptedArgumentErrors(encryptedArgumentSchema(tc.edit))
			if !slices.Equal(msgs, []string{tc.want}) {
				t.Errorf("errors = %q, want %q", msgs, tc.want)
			}
		})
	}
}

// TestPlainArgumentsAreNotChecked: the rule reads only EncryptedField<T>
// arguments, so a plain query or path parameter passes.
func TestPlainArgumentsAreNotChecked(t *testing.T) {
	schema := encryptedArgumentSchema(func(op *ir.FieldDef, number *ir.ArgumentDef) {
		number.Encrypted = false
		number.IsQuery = true
		op.HTTPMethod = "GET"
	})
	if msgs := encryptedArgumentErrors(schema); len(msgs) != 0 {
		t.Errorf("errors = %v, want none", msgs)
	}
}
