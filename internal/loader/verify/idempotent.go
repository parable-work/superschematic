package verify

import (
	"fmt"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// checkIdempotency holds @idempotent and the reserved table of its keys to
// where each means something (D140).
//
// A DB schema declares the table with one class that implements
// IdempotencyKeys: a DB table, at most one a schema.
//
// An operation is @idempotent only when a repeat can be answered what the
// first call was: a POST, PUT, PATCH or DELETE that answers the JSON
// envelope, so not a file, a stream, a manually registered route, whose
// handler answers as it likes, or an encrypted operation, whose body is
// sealed anew by each call and so never hashes the same. And the API's
// database must declare the table, with the line to add when it does not.
func checkIdempotency(schema *ir.Schema, in Input, r *Result) {
	var tables []string
	for name, td := range schema.Types {
		if td == nil || td.IdempotencyKeys == nil {
			continue
		}
		tables = append(tables, name)
		switch {
		case schema.Kind != ir.SchemaKindDB:
			r.errorf("", "type %s: implements %s, which marks a DB table of a DB schema (this service is kind %s)", name, ir.IdempotencyKeysTraitName, schema.Kind)
		case td.Role != ir.RoleDBTable:
			r.errorf("", "type %s: implements %s, which marks a DB table (this type has role %s)", name, ir.IdempotencyKeysTraitName, td.Role)
		case td.User != nil || td.UserRole != nil:
			r.errorf("", "type %s: a table is the user model's or the idempotency keys', not both", name)
		}
	}
	if len(tables) > 1 {
		sort.Strings(tables)
		r.errorf("", "%s all implement %s; a schema keeps its idempotency keys in one table", strings.Join(tables, ", "), ir.IdempotencyKeysTraitName)
	}

	idempotent := false
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op == nil || op.Idempotent == nil {
				continue
			}
			idempotent = true
			owner := fmt.Sprintf("%s.%s", set.Name, op.Name)
			switch strings.ToUpper(op.HTTPMethod) {
			case "POST", "PUT", "PATCH", "DELETE":
			default:
				r.errorf("", "%s: @idempotent on a %s, which a caller repeats freely; it marks a POST, PUT, PATCH or DELETE", owner, methodName(op.HTTPMethod))
			}
			switch {
			case op.FileDownload != nil:
				r.errorf("", "%s: @idempotent on a FileDownload result, whose answer is a file, not the JSON envelope a repeat is answered", owner)
			case op.HTTPBinaryStream != nil:
				r.errorf("", "%s: @idempotent on an httpBinaryStream result, whose answer is a stream, not the JSON envelope a repeat is answered", owner)
			case op.ManualRouteRegistration:
				r.errorf("", "%s: @idempotent on a @manualRouteRegistration operation, whose handler answers as it likes; keep its keys in the handler", owner)
			case set.Encrypted || op.Encrypted:
				r.errorf("", "%s: @idempotent on an encrypted operation, whose body each call seals anew, so a repeat never sends the same request", owner)
			}
			if op.Idempotent.TTLSeconds < 0 || op.Idempotent.TTLSeconds > ir.MaxIdempotencyTTLSeconds {
				r.errorf("", "%s: @idempotent ttl is %d seconds; it is above 0 and at most %d", owner, op.Idempotent.TTLSeconds, ir.MaxIdempotencyTTLSeconds)
			}
		}
	}
	if !idempotent || schema.Kind != ir.SchemaKindAPI {
		return
	}
	database, _, ok := schema.Database()
	line := "export abstract class IdempotencyKey implements IdempotencyKeys {}"
	switch {
	case !ok:
		r.errorf("", "%s: its @idempotent operations (%s) keep their keys in the API's database, and it connects to none: set its authDb, or give it one DB dependency, and add to that schema: %s", schema.Name, strings.Join(schema.IdempotentOperations(), ", "), line)
	case in.Database == nil:
		// The loader could not read the database, and said why.
	case in.Database.IdempotencyKeysTable() == nil:
		r.errorf("", "%s: its @idempotent operations (%s) keep their keys in %s, which declares no table for them; add to a schema file of %s, with IdempotencyKeys imported from @superschematic/db: %s", schema.Name, strings.Join(schema.IdempotentOperations(), ", "), database, database, line)
	}
}

// methodName is an operation's HTTP method for a message, GET when it
// declares none.
func methodName(method string) string {
	if method == "" {
		return "GET"
	}
	return strings.ToUpper(method)
}
