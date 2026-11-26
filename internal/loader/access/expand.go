// Package access expands a DB schema's per-record access declarations
// (D156) into ordinary fields, as the identity expansion does for the user
// model: an @audience table's audience columns, and the fields of the
// classes marked @accessGrants and @accessLog. The sql, orm and types
// generators emit the result as any table's, and sqlgen adds the trigger
// that keeps an @audience table's effective audience.
//
// It runs before the verification pass, so the pass reads the added fields
// as the table's own: an index, a view or a row rule may name them. Every
// field it adds carries ir.OriginAccess, so the schema writer can skip it
// and write the declaration instead.
package access

import (
	ir "github.com/parable-work/superschematic/ir"
)

// Scalars the added fields use.
const (
	uuidScalar     = "Identity.UUID"
	dateTimeScalar = "Temporal.DateTime"
	sha256Scalar   = "Crypto.SHA256"
)

// Expand adds the fields of schema's access declarations: the audience
// columns of each @audience table that does not hold them yet, and the
// fields of an @accessGrants or @accessLog class that declares none. A
// field the author declared under a name the expansion would add is left
// for the verification pass to refuse. It returns the scalar names the
// added fields use, so the caller can hydrate any the schema did not
// already reference.
func Expand(schema *ir.Schema) []string {
	var used bool
	for _, td := range schema.AudienceTables() {
		if addAudience(td) {
			used = true
		}
	}
	if td := schema.AccessGrantsTable(); td != nil && len(td.Fields) == 0 {
		fillGrants(td)
		used = true
	}
	if td := schema.AccessLogTable(); td != nil && len(td.Fields) == 0 {
		fillLog(td)
		used = true
	}
	if !used {
		return nil
	}
	scalars := []string{uuidScalar, dateTimeScalar, sha256Scalar}
	for _, name := range scalars {
		if schema.Scalars[name] == nil {
			schema.Scalars[name] = &ir.ScalarDef{Name: name}
		}
	}
	return scalars
}

// addAudience adds an @audience table's columns, each one it lacks, and
// reports whether it added one. A table that names one of them already
// keeps its own, which the verification pass refuses.
func addAudience(td *ir.TypeDef) bool {
	a := td.Audience
	defaultAudience := a.DefaultAudience()
	defaultAgents := a.DefaultAgents()
	followRole := ir.AgentsFollowRole
	fields := []*ir.FieldDef{
		{
			Name:     ir.AudienceField,
			Comment:  "Who may see the row: everyone, private (its owner alone), named (its owner and audienceNamed) or inherit (what its parent sees) (D156).",
			TypeRef:  ir.TypeRef{Name: "string"},
			Required: true,
			Default:  &defaultAudience,
		},
		{
			Name:    ir.AudienceNamedField,
			Comment: "The principals a named row is visible to beside its owner, by their ids.",
			TypeRef: ir.TypeRef{Name: uuidScalar, IsArray: true},
		},
		{
			Name:     ir.AgentAccessField,
			Comment:  "What an agent run may do with the row: followRole, readOnly or none.",
			TypeRef:  ir.TypeRef{Name: "string"},
			Required: true,
			Default:  &defaultAgents,
		},
		{
			Name:    ir.EffectiveAudienceField,
			Comment: "The principals the row is visible to, by their ids, or null for everyone: the audience resolved, which a trigger keeps.",
			TypeRef: ir.TypeRef{Name: uuidScalar, IsArray: true},
		},
		{
			Name:     ir.EffectiveAgentsField,
			Comment:  "What an agent run may do with the row, resolved through its parent, which a trigger keeps.",
			TypeRef:  ir.TypeRef{Name: "string"},
			Required: true,
			Default:  &followRole,
		},
	}
	added := false
	for _, fd := range fields {
		if hasField(td, fd.Name) {
			continue
		}
		fd.Origin = ir.OriginAccess
		td.Fields = append(td.Fields, fd)
		added = true
	}
	return added
}

// fillGrants fills an @accessGrants class's fields: one row per grant.
func fillGrants(td *ir.TypeDef) {
	td.Fields = []*ir.FieldDef{
		keyField(),
		required(ir.AccessGrantHolderField, uuidScalar, "The principal the grant lets reach rows: a party's or an agent run's id."),
		required(ir.AccessGrantHolderKindField, "string", "The kind of that principal: party or agent."),
		required(ir.AccessGrantRoleField, "string", "The name of the role, a row of the UserRole table, whose permissions the holder acts with and whose reaches say which rows beside the target it reaches."),
		required(ir.AccessGrantTargetTypeField, "string", "The table of the target row, by its type's name."),
		required(ir.AccessGrantTargetIDField, uuidScalar, "The target row's key."),
		required(ir.AccessGrantStartsAtField, dateTimeScalar, "When the grant starts."),
		optional(ir.AccessGrantEndsAtField, dateTimeScalar, "When the grant ends; null for no date."),
		optional(ir.AccessGrantEndsWhenFieldField, "string", "The target's field whose state ends the grant; null for no such end."),
		optional(ir.AccessGrantEndsWhenStateField, "string", "The state of that field that ends the grant."),
		optional(ir.AccessGrantRevokedAtField, dateTimeScalar, "When the grant was revoked."),
		{
			Name:    ir.AccessGrantTokenHashField,
			Comment: "The SHA-256 of the token the holder presents, which only the holder has; null for a grant resolved another way.",
			TypeRef: ir.TypeRef{Name: sha256Scalar},
			Unique:  true,
		},
		required(ir.AccessGrantCreatedAtField, dateTimeScalar, ""),
		optional(ir.AccessGrantCreatedByField, uuidScalar, "Who made the grant."),
	}
	for _, fd := range td.Fields {
		fd.Origin = ir.OriginAccess
	}
	td.Indexes = append(td.Indexes,
		ir.IndexDef{Keys: []string{ir.AccessGrantHolderField}, Name: "holder", Origin: ir.OriginAccess},
		ir.IndexDef{Keys: []string{ir.AccessGrantTargetTypeField, ir.AccessGrantTargetIDField}, Name: "target", Origin: ir.OriginAccess},
	)
}

// fillLog fills an @accessLog class's fields: one row per access it
// records.
func fillLog(td *ir.TypeDef) {
	td.Fields = []*ir.FieldDef{
		keyField(),
		required(ir.AccessLogAtField, dateTimeScalar, "When the access happened."),
		required(ir.AccessLogKindField, "string", "What happened: reveal, agentRead, keyCall or grantCall."),
		optional(ir.AccessLogPrincipalField, "string", "The subject of the principal that acted."),
		optional(ir.AccessLogPrincipalKindField, "string", "Its kind: user, party or agent."),
		optional(ir.AccessLogActingForField, "string", "The subject the principal acted for; null when it acted for itself."),
		optional(ir.AccessLogCredentialField, "string", "The API key's or the grant's id the call came with; null for a session."),
		optional(ir.AccessLogOperationField, "string", "The operation the call ran."),
		optional(ir.AccessLogTargetTypeField, "string", "The table of the row read."),
		optional(ir.AccessLogTargetIDField, "string", "The key of the row read."),
		optional(ir.AccessLogFieldField, "string", "The field revealed."),
	}
	for _, fd := range td.Fields {
		fd.Origin = ir.OriginAccess
	}
	td.Indexes = append(td.Indexes,
		ir.IndexDef{Keys: []string{ir.AccessLogAtField}, Name: "at", Origin: ir.OriginAccess},
		ir.IndexDef{Keys: []string{ir.AccessLogPrincipalField, ir.AccessLogAtField}, Name: "principal", Origin: ir.OriginAccess},
	)
}

func keyField() *ir.FieldDef {
	return &ir.FieldDef{Name: "id", TypeRef: ir.TypeRef{Name: uuidScalar}, Required: true, Key: true, AutoGenerated: true}
}

func required(name, typeName, comment string) *ir.FieldDef {
	return &ir.FieldDef{Name: name, Comment: comment, TypeRef: ir.TypeRef{Name: typeName}, Required: true}
}

func optional(name, typeName, comment string) *ir.FieldDef {
	return &ir.FieldDef{Name: name, Comment: comment, TypeRef: ir.TypeRef{Name: typeName}}
}

func hasField(td *ir.TypeDef, name string) bool {
	for _, fd := range td.Fields {
		if fd != nil && fd.Name == name {
			return true
		}
	}
	return false
}

// ExpandExternal adds the audience columns to each @audience table of
// another service the schema reads (a DB dependency's, which a @source
// view names), and the fields of its @accessGrants and @accessLog
// classes, as the dependency's own load adds them, so a view may hold
// them: an API's list of the grants or of the access log is a
// @tableOperation over such a view.
func ExpandExternal(types map[string]*ir.TypeDef) {
	for _, td := range types {
		if td == nil {
			continue
		}
		if td.Audience != nil {
			addAudience(td)
		}
		if td.AccessGrants != nil && len(td.Fields) == 0 {
			fillGrants(td)
		}
		if td.AccessLog != nil && len(td.Fields) == 0 {
			fillLog(td)
		}
	}
}
