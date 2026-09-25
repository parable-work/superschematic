package ir

import (
	"strings"
	"testing"
)

func newTestSchema() *Schema {
	s := NewSchema("web-api", SchemaKindAPI)
	s.Scalars["Identity.UUID"] = &ScalarDef{Name: "Identity.UUID", LanguagePrimitive: LanguageString}
	s.Types["User"] = &TypeDef{
		Name: "User",
		Role: RoleAPIView,
		Fields: []*FieldDef{
			{Name: "id", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true},
		},
	}
	s.Enums["Status"] = &EnumDef{
		Name:   "Status",
		Values: []EnumValueDef{{Name: "Active", SerializedAs: "active"}},
	}
	return s
}

func TestValidate_CleanSchema(t *testing.T) {
	s := newTestSchema()
	if errs := s.Validate(); len(errs) != 0 {
		t.Errorf("Validate() = %v, want no errors", errs)
	}
}

func TestValidateHydrated_UploadMaxBytesRequiresPositiveFileUploadScalar(t *testing.T) {
	limit := int64(64 * 1024 * 1024)
	s := newTestSchema()
	s.Scalars["Media.File"] = &ScalarDef{
		Name:              "Media.File",
		LanguagePrimitive: LanguageObject,
		FileUpload:        &FileUploadConfig{MaxSize: 1024},
	}
	s.Types["User"].Fields = append(s.Types["User"].Fields, &FieldDef{
		Name:                   "archive",
		TypeRef:                TypeRef{Name: "Media.File"},
		Required:               true,
		ValidateUploadMaxBytes: &limit,
	})
	if errs := s.ValidateHydrated(); len(errs) != 0 {
		t.Fatalf("valid upload bound: %v", errs)
	}

	zero := int64(0)
	s.Types["User"].Fields[1].ValidateUploadMaxBytes = &zero
	if errs := s.ValidateHydrated(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "must be positive") {
		t.Fatalf("zero upload bound errors = %v, want one positive-bound error", errs)
	}

	s.Types["User"].Fields[1].ValidateUploadMaxBytes = &limit
	s.Types["User"].Fields[1].TypeRef = TypeRef{Name: "Media.File", IsArray: true}
	if errs := s.ValidateHydrated(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "requires a file-upload scalar") {
		t.Fatalf("list upload bound errors = %v, want one file-upload scalar error", errs)
	}

	s.Types["User"].Fields[1].TypeRef = TypeRef{Name: "Identity.UUID"}
	if errs := s.ValidateHydrated(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "User.archive uploadMaxBytes requires a file-upload scalar") {
		t.Fatalf("non-upload bound errors = %v, want one file-upload scalar error", errs)
	}
}

// TestValidate_LeavesUploadMaxBytesToValidateHydrated: a frontend validates
// before the loader hydrates, when a scalar reference carries no FileUpload
// metadata yet, so Validate does not judge the bound; ValidateHydrated does.
func TestValidate_LeavesUploadMaxBytesToValidateHydrated(t *testing.T) {
	limit := int64(1024)
	s := newTestSchema()
	s.Scalars["Media.File"] = &ScalarDef{Name: "Media.File", LanguagePrimitive: LanguageString}
	s.Types["User"].Fields = append(s.Types["User"].Fields, &FieldDef{
		Name: "archive", TypeRef: TypeRef{Name: "Media.File"}, ValidateUploadMaxBytes: &limit,
	})
	if errs := s.Validate(); len(errs) != 0 {
		t.Fatalf("Validate() = %v, want the bound left to ValidateHydrated", errs)
	}

	s.Scalars["Media.File"].FileUpload = &FileUploadConfig{MaxSize: 4096}
	if errs := s.ValidateHydrated(); len(errs) != 0 {
		t.Fatalf("ValidateHydrated() on the hydrated upload scalar = %v, want none", errs)
	}
}

func TestValidateHydrated_UploadMaxBytesChecksOperations(t *testing.T) {
	limit := int64(1024)
	s := newTestSchema()
	s.OperationSets = []*OperationSet{{
		Name: "UserMutations",
		Operations: []*FieldDef{{
			Name: "upload", TypeRef: TypeRef{Name: "Identity.UUID"}, ValidateUploadMaxBytes: &limit,
		}},
	}}
	errs := s.ValidateHydrated()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "UserMutations.upload uploadMaxBytes requires a file-upload scalar") {
		t.Fatalf("validation errors = %v, want the operation's file-upload scalar error", errs)
	}
}

func TestValidate_DanglingFieldRef(t *testing.T) {
	s := newTestSchema()
	s.Types["User"].Fields = append(s.Types["User"].Fields, &FieldDef{
		Name:    "tenant",
		TypeRef: TypeRef{Name: "Tenant"},
	})

	errs := s.Validate()
	if len(errs) != 1 {
		t.Fatalf("Validate() returned %d errors, want 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), `User.tenant references unknown type "Tenant"`) {
		t.Errorf("unexpected error: %v", errs[0])
	}
}

// TestValidate_RelationOnDeleteInvalid: an out-of-range onDelete on a relation
// is rejected. This is the value-gate for every load format (TS/JSON/YAML),
// since Validate runs on the loaded IR regardless of source.
func TestValidate_RelationOnDeleteInvalid(t *testing.T) {
	s := newTestSchema()
	s.Types["User"].Fields = append(s.Types["User"].Fields, &FieldDef{
		Name:     "owner",
		TypeRef:  TypeRef{Name: "User"},
		Relation: &RelationDef{Type: "User", OnDelete: "BOGUS"},
	})

	errs := s.Validate()
	if len(errs) != 1 {
		t.Fatalf("Validate() returned %d errors, want 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), `User.owner relation onDelete "BOGUS" is invalid`) {
		t.Errorf("unexpected error: %v", errs[0])
	}
}

// TestValidate_RelationOnDeleteValid: every supported action and the empty
// (default CASCADE) case validate cleanly.
func TestValidate_RelationOnDeleteValid(t *testing.T) {
	for _, action := range []string{"CASCADE", "RESTRICT", "NO ACTION", ""} {
		s := newTestSchema()
		s.Types["User"].Fields = append(s.Types["User"].Fields, &FieldDef{
			Name:     "owner",
			TypeRef:  TypeRef{Name: "User"},
			Relation: &RelationDef{Type: "User", OnDelete: action},
		})
		if errs := s.Validate(); len(errs) != 0 {
			t.Errorf("onDelete %q: Validate() = %v, want no errors", action, errs)
		}
	}
}

func TestValidate_DanglingArgumentRef(t *testing.T) {
	s := newTestSchema()
	s.Types["User"].Fields[0].Arguments = []*ArgumentDef{
		{Name: "filter", TypeRef: TypeRef{Name: "UserFilter"}},
	}

	errs := s.Validate()
	if len(errs) != 1 {
		t.Fatalf("Validate() returned %d errors, want 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), `argument "filter" references unknown type "UserFilter"`) {
		t.Errorf("unexpected error: %v", errs[0])
	}
}

func TestValidate_OperationSets(t *testing.T) {
	s := newTestSchema()
	s.OperationSets = []*OperationSet{
		{
			Name: "UserOperations",
			Operations: []*FieldDef{
				{
					Name:       "getUser",
					HTTPMethod: "GET",
					TypeRef:    TypeRef{Name: "User"},
					Arguments: []*ArgumentDef{
						{Name: "userId", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true},
					},
				},
			},
		},
		nil, // nil sets are skipped
	}

	if errs := s.Validate(); len(errs) != 0 {
		t.Errorf("Validate() = %v, want no errors", errs)
	}

	s.OperationSets[0].Operations = append(s.OperationSets[0].Operations, &FieldDef{
		Name:       "getTenant",
		HTTPMethod: "GET",
		TypeRef:    TypeRef{Name: "Tenant"},
	})

	errs := s.Validate()
	if len(errs) != 1 {
		t.Fatalf("Validate() returned %d errors, want 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), `UserOperations.getTenant references unknown type "Tenant"`) {
		t.Errorf("unexpected error: %v", errs[0])
	}
}

func TestValidate_UnionMembers(t *testing.T) {
	s := newTestSchema()
	s.Unions["Entity"] = &UnionDef{Name: "Entity", Types: []string{"User", "Ghost"}}

	errs := s.Validate()
	if len(errs) != 1 {
		t.Fatalf("Validate() returned %d errors, want 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), `union Entity references unknown member type "Ghost"`) {
		t.Errorf("unexpected error: %v", errs[0])
	}
}

func TestValidate_KnownExternals(t *testing.T) {
	s := newTestSchema()
	s.Types["User"].Fields = append(s.Types["User"].Fields, &FieldDef{
		Name:    "tenant",
		TypeRef: TypeRef{Name: "Tenant"},
	})

	errs := s.Validate(WithKnownExternals(map[string]bool{"Tenant": true}))
	if len(errs) != 0 {
		t.Errorf("Validate() with externals = %v, want no errors", errs)
	}
}

func TestValidate_ScalarAliasIndex(t *testing.T) {
	s := newTestSchema()
	s.Types["User"].Fields = append(s.Types["User"].Fields, &FieldDef{
		Name:    "slug",
		TypeRef: TypeRef{Name: "Slug"},
	})

	index := BuildScalarAliasIndex(map[string]*ScalarDef{
		"Identity.Slug": {Name: "Identity.Slug"},
		"Content.Slug":  {Name: "Content.Slug"},
	})

	// Ambiguous aliases pass by default.
	if errs := s.Validate(WithScalarAliasIndex(index)); len(errs) != 0 {
		t.Errorf("Validate() with alias index = %v, want no errors", errs)
	}

	// Strict resolution rejects ambiguous aliases.
	errs := s.Validate(WithScalarAliasIndex(index), WithStrictScalarResolution())
	if len(errs) != 1 {
		t.Fatalf("Validate() strict returned %d errors, want 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), `User.slug references unknown type "Slug"`) {
		t.Errorf("unexpected error: %v", errs[0])
	}
}

func TestValidate_NoBuiltinScalars(t *testing.T) {
	s := newTestSchema()
	s.Types["User"].Fields = append(s.Types["User"].Fields, &FieldDef{
		Name:    "displayName",
		TypeRef: TypeRef{Name: "String"},
	})

	// GraphQL builtins do not exist in v2; "String" must not resolve implicitly.
	errs := s.Validate()
	if len(errs) != 1 {
		t.Fatalf("Validate() returned %d errors, want 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), `User.displayName references unknown type "String"`) {
		t.Errorf("unexpected error: %v", errs[0])
	}
}

func TestValidate_Imports(t *testing.T) {
	cases := []struct {
		name    string
		imports []Import
		want    []string
	}{
		{
			name:    "named import is valid",
			imports: []Import{{Package: "@schemas/web-db", Types: []string{"User"}}},
			want:    nil,
		},
		{
			name:    "empty package",
			imports: []Import{{Package: "", Types: []string{"User"}}},
			want:    []string{"import[0] has an empty package"},
		},
		{
			name:    "no symbols",
			imports: []Import{{Package: "@schemas/web-db", Types: nil}},
			want:    []string{"lists no symbols"},
		},
		{
			name:    "wildcard symbol",
			imports: []Import{{Package: "@schemas/web-db", Types: []string{"*"}}},
			want:    []string{`uses the "*" wildcard`},
		},
		{
			name:    "empty symbol",
			imports: []Import{{Package: "@schemas/web-db", Types: []string{""}}},
			want:    []string{"lists an empty symbol name"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSchema()
			s.Imports = tc.imports
			errs := s.Validate()
			if len(errs) != len(tc.want) {
				t.Fatalf("Validate() returned %d errors, want %d: %v", len(errs), len(tc.want), errs)
			}
			for i, fragment := range tc.want {
				if !strings.Contains(errs[i].Error(), fragment) {
					t.Errorf("errs[%d] = %v, want it to contain %q", i, errs[i], fragment)
				}
			}
		})
	}
}

func TestValidateOperationTransportMetadata(t *testing.T) {
	if err := ValidateOperationTransportMetadata(nil); err != nil {
		t.Errorf("nil schema: %v, want nil", err)
	}

	s := newTestSchema()
	s.OperationSets = []*OperationSet{
		{
			Name: "QueryOperations",
			Operations: []*FieldDef{
				{
					Name:       "streamResults",
					HTTPMethod: "POST",
					TypeRef:    TypeRef{Name: "User"},
					HTTPBinaryStream: &HTTPBinaryStreamDef{
						ContentType:     "application/vnd.apache.arrow.stream",
						ResponseHeaders: []string{"X-Query-Id"},
					},
				},
			},
		},
	}
	if err := ValidateOperationTransportMetadata(s); err != nil {
		t.Errorf("valid stream metadata: %v, want nil", err)
	}

	s.OperationSets[0].Operations[0].HTTPBinaryStream.ContentType = "  "
	err := ValidateOperationTransportMetadata(s)
	if err == nil || !strings.Contains(err.Error(), "httpBinaryStream without contentType") {
		t.Errorf("missing contentType: %v, want contentType error", err)
	}

	s.OperationSets[0].Operations[0].HTTPBinaryStream.ContentType = "application/json"
	s.OperationSets[0].Operations[0].HTTPBinaryStream.ResponseHeaders = []string{""}
	err = ValidateOperationTransportMetadata(s)
	if err == nil || !strings.Contains(err.Error(), "empty responseHeaders[0]") {
		t.Errorf("empty response header: %v, want responseHeaders error", err)
	}

	// Type fields are also checked.
	s.OperationSets = nil
	s.Types["User"].Fields[0].HTTPBinaryStream = &HTTPBinaryStreamDef{}
	err = ValidateOperationTransportMetadata(s)
	if err == nil || !strings.Contains(err.Error(), `type "User"`) {
		t.Errorf("type field stream: %v, want type-scoped error", err)
	}
}
