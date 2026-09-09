package ir

import "testing"

func TestNewSchema_InitializesMaps(t *testing.T) {
	s := NewSchema("web-api", SchemaKindAPI)

	if s.Name != "web-api" {
		t.Errorf("Name = %q, want %q", s.Name, "web-api")
	}
	if s.Kind != SchemaKindAPI {
		t.Errorf("Kind = %q, want %q", s.Kind, SchemaKindAPI)
	}

	// Map writes must not panic on a fresh schema.
	s.Scalars["Identity.UUID"] = &ScalarDef{Name: "Identity.UUID"}
	s.Types["User"] = &TypeDef{Name: "User", Role: RoleDBTable}
	s.Enums["Status"] = &EnumDef{Name: "Status"}
	s.Unions["Config"] = &UnionDef{Name: "Config"}
}

func TestSchemaKind_String(t *testing.T) {
	cases := []struct {
		kind SchemaKind
		want string
	}{
		{SchemaKindDB, "DB"},
		{SchemaKindAPI, "API"},
		{SchemaKindGeneral, "General"},
	}
	for _, tc := range cases {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("SchemaKind(%q).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestRole_String(t *testing.T) {
	cases := []struct {
		role Role
		want string
	}{
		{RoleDBTable, "DBTable"},
		{RoleAPIView, "APIView"},
		{RoleAPIInput, "APIInput"},
		{RoleEmbeddedStruct, "EmbeddedStruct"},
		{RoleAPIOperationSet, "APIOperationSet"},
		{RoleTrait, "Trait"},
	}
	for _, tc := range cases {
		if got := tc.role.String(); got != tc.want {
			t.Errorf("Role(%q).String() = %q, want %q", tc.role, got, tc.want)
		}
	}
}

func TestLanguagePrimitive_String(t *testing.T) {
	cases := []struct {
		prim LanguagePrimitive
		want string
	}{
		{LanguageString, "string"},
		{LanguageNumber, "number"},
		{LanguageBoolean, "boolean"},
		{LanguageObject, "object"},
	}
	for _, tc := range cases {
		if got := tc.prim.String(); got != tc.want {
			t.Errorf("LanguagePrimitive(%q).String() = %q, want %q", tc.prim, got, tc.want)
		}
	}
}
