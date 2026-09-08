package merge

import (
	"testing"

	"github.com/parable-work/superschematic/ir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testMergeSchema() *ir.Schema {
	s := ir.NewSchema("test", ir.SchemaKindGeneral)

	s.Types["Credentials"] = &ir.TypeDef{
		Name: "Credentials",
		Kind: ir.TypeKindObject,
		Fields: []*ir.FieldDef{
			{Name: "username", TypeRef: ir.TypeRef{Name: "String"}, Required: true},
			{Name: "apiKey", TypeRef: ir.TypeRef{Name: "String"}, Required: true, Secret: true},
		},
	}

	s.Types["Endpoint"] = &ir.TypeDef{
		Name: "Endpoint",
		Kind: ir.TypeKindObject,
		Fields: []*ir.FieldDef{
			{Name: "url", TypeRef: ir.TypeRef{Name: "String"}, Required: true},
			{Name: "token", TypeRef: ir.TypeRef{Name: "String"}, Required: true, Secret: true},
		},
	}

	s.Types["ServiceConfig"] = &ir.TypeDef{
		Name: "ServiceConfig",
		Kind: ir.TypeKindObject,
		Fields: []*ir.FieldDef{
			{Name: "name", TypeRef: ir.TypeRef{Name: "String"}, Required: true},
			{Name: "retries", TypeRef: ir.TypeRef{Name: "Int"}, Required: true},
			{Name: "password", TypeRef: ir.TypeRef{Name: "String"}, Required: true, Secret: true},
			{Name: "credentials", TypeRef: ir.TypeRef{Name: "Credentials"}, Required: false},
			{Name: "endpoints", TypeRef: ir.TypeRef{Name: "Endpoint", IsArray: true, ElemNonNull: true}, Required: false},
		},
	}

	s.Inputs["CredentialsInput"] = &ir.TypeDef{
		Name: "CredentialsInput",
		Kind: ir.TypeKindInput,
		Fields: []*ir.FieldDef{
			{Name: "clientId", TypeRef: ir.TypeRef{Name: "String"}, Required: true},
			{Name: "clientSecret", TypeRef: ir.TypeRef{Name: "String"}, Required: true, Secret: true},
		},
	}

	s.Inputs["ConfigInput"] = &ir.TypeDef{
		Name: "ConfigInput",
		Kind: ir.TypeKindInput,
		Fields: []*ir.FieldDef{
			{Name: "displayName", TypeRef: ir.TypeRef{Name: "String"}, Required: true},
			{Name: "auth", TypeRef: ir.TypeRef{Name: "CredentialsInput"}, Required: false},
		},
	}

	return s
}

func TestMerger_TableDrivenScenarios(t *testing.T) {
	m := New(testMergeSchema())

	type testCase struct {
		name      string
		mergeKind string
		typeName  string
		newData   map[string]any
		existing  map[string]any
		expected  map[string]any
		wantNil   bool
	}

	tests := []testCase{
		{
			name:      "nil new data returns nil",
			mergeKind: "type",
			typeName:  "ServiceConfig",
			newData:   nil,
			existing:  map[string]any{"password": "secret"},
			wantNil:   true,
		},
		{
			name:      "nil existing data returns new data as-is",
			mergeKind: "type",
			typeName:  "ServiceConfig",
			newData:   map[string]any{"name": "a", "password": ""},
			existing:  nil,
			expected:  map[string]any{"name": "a", "password": ""},
		},
		{
			name:      "empty secret field preserves existing value",
			mergeKind: "type",
			typeName:  "ServiceConfig",
			newData: map[string]any{
				"name":     "connector-a",
				"retries":  5,
				"password": "",
			},
			existing: map[string]any{
				"name":     "connector-a",
				"retries":  3,
				"password": "top-secret",
			},
			expected: map[string]any{
				"name":     "connector-a",
				"retries":  5,
				"password": "top-secret",
			},
		},
		{
			name:      "non-empty secret field takes new value",
			mergeKind: "type",
			typeName:  "ServiceConfig",
			newData: map[string]any{
				"name":     "connector-a",
				"retries":  5,
				"password": "new-password",
			},
			existing: map[string]any{
				"name":     "connector-a",
				"retries":  3,
				"password": "old-password",
			},
			expected: map[string]any{
				"name":     "connector-a",
				"retries":  5,
				"password": "new-password",
			},
		},
		{
			name:      "mixed: some secrets changed, some empty",
			mergeKind: "type",
			typeName:  "ServiceConfig",
			newData: map[string]any{
				"name":     "connector-b",
				"retries":  2,
				"password": "",
				"credentials": map[string]any{
					"username": "bob",
					"apiKey":   "new-key",
				},
			},
			existing: map[string]any{
				"name":     "connector-b",
				"retries":  1,
				"password": "old-password",
				"credentials": map[string]any{
					"username": "alice",
					"apiKey":   "old-key",
				},
			},
			expected: map[string]any{
				"name":     "connector-b",
				"retries":  2,
				"password": "old-password",
				"credentials": map[string]any{
					"username": "bob",
					"apiKey":   "new-key",
				},
			},
		},
		{
			name:      "nested secret fields in sub-types are merged",
			mergeKind: "type",
			typeName:  "ServiceConfig",
			newData: map[string]any{
				"name":     "connector-c",
				"retries":  3,
				"password": "new-pass",
				"credentials": map[string]any{
					"username": "charlie",
					"apiKey":   "",
				},
			},
			existing: map[string]any{
				"name":     "connector-c",
				"retries":  2,
				"password": "old-pass",
				"credentials": map[string]any{
					"username": "old-charlie",
					"apiKey":   "existing-api-key",
				},
			},
			expected: map[string]any{
				"name":     "connector-c",
				"retries":  3,
				"password": "new-pass",
				"credentials": map[string]any{
					"username": "charlie",
					"apiKey":   "existing-api-key",
				},
			},
		},
		{
			name:      "array fields with secret sub-fields are merged by index",
			mergeKind: "type",
			typeName:  "ServiceConfig",
			newData: map[string]any{
				"name":     "connector-d",
				"retries":  1,
				"password": "",
				"endpoints": []any{
					map[string]any{"url": "https://a.example.com", "token": ""},
					map[string]any{"url": "https://b.example.com", "token": "new-token-b"},
				},
			},
			existing: map[string]any{
				"name":     "connector-d",
				"retries":  1,
				"password": "old-pass",
				"endpoints": []any{
					map[string]any{"url": "https://a.example.com", "token": "old-token-a"},
					map[string]any{"url": "https://b.example.com", "token": "old-token-b"},
				},
			},
			expected: map[string]any{
				"name":     "connector-d",
				"retries":  1,
				"password": "old-pass",
				"endpoints": []any{
					map[string]any{"url": "https://a.example.com", "token": "old-token-a"},
					map[string]any{"url": "https://b.example.com", "token": "new-token-b"},
				},
			},
		},
		{
			name:      "non-secret fields always take new value",
			mergeKind: "type",
			typeName:  "ServiceConfig",
			newData: map[string]any{
				"name":     "new-name",
				"retries":  10,
				"password": "",
			},
			existing: map[string]any{
				"name":     "old-name",
				"retries":  1,
				"password": "existing-pass",
			},
			expected: map[string]any{
				"name":     "new-name",
				"retries":  10,
				"password": "existing-pass",
			},
		},
		{
			name:      "input type merge works recursively",
			mergeKind: "input",
			typeName:  "ConfigInput",
			newData: map[string]any{
				"displayName": "Updated Name",
				"auth": map[string]any{
					"clientId":     "new-client",
					"clientSecret": "",
				},
			},
			existing: map[string]any{
				"displayName": "Old Name",
				"auth": map[string]any{
					"clientId":     "old-client",
					"clientSecret": "my-secret",
				},
			},
			expected: map[string]any{
				"displayName": "Updated Name",
				"auth": map[string]any{
					"clientId":     "new-client",
					"clientSecret": "my-secret",
				},
			},
		},
		{
			name:      "unknown type name returns deep copy of new data",
			mergeKind: "type",
			typeName:  "NonExistentType",
			newData:   map[string]any{"foo": "bar"},
			existing:  map[string]any{"foo": "old"},
			expected:  map[string]any{"foo": "bar"},
		},
		{
			name:      "new array longer than existing — extra elements kept as-is",
			mergeKind: "type",
			typeName:  "ServiceConfig",
			newData: map[string]any{
				"name":     "connector-e",
				"retries":  1,
				"password": "",
				"endpoints": []any{
					map[string]any{"url": "https://a.example.com", "token": ""},
					map[string]any{"url": "https://c.example.com", "token": ""},
				},
			},
			existing: map[string]any{
				"name":     "connector-e",
				"retries":  1,
				"password": "pass",
				"endpoints": []any{
					map[string]any{"url": "https://a.example.com", "token": "old-token-a"},
				},
			},
			expected: map[string]any{
				"name":     "connector-e",
				"retries":  1,
				"password": "pass",
				"endpoints": []any{
					map[string]any{"url": "https://a.example.com", "token": "old-token-a"},
					map[string]any{"url": "https://c.example.com", "token": ""},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var merged map[string]any
			switch tc.mergeKind {
			case "input":
				merged = m.MergeInput(tc.typeName, tc.newData, tc.existing)
			default:
				merged = m.MergeType(tc.typeName, tc.newData, tc.existing)
			}

			if tc.wantNil {
				assert.Nil(t, merged)
				return
			}

			assert.Equal(t, tc.expected, merged)
		})
	}
}

func TestMerger_DoesNotMutateInput(t *testing.T) {
	m := New(testMergeSchema())

	newData := map[string]any{
		"name":     "test",
		"retries":  1,
		"password": "",
		"credentials": map[string]any{
			"username": "new-user",
			"apiKey":   "",
		},
	}

	existing := map[string]any{
		"name":     "test",
		"retries":  1,
		"password": "real-secret",
		"credentials": map[string]any{
			"username": "old-user",
			"apiKey":   "real-api-key",
		},
	}

	merged := m.MergeType("ServiceConfig", newData, existing)

	require.NotNil(t, merged)

	// Verify original inputs were not mutated
	assert.Equal(t, "", newData["password"])
	assert.Equal(t, "", newData["credentials"].(map[string]any)["apiKey"])
	assert.Equal(t, "real-secret", existing["password"])
	assert.Equal(t, "real-api-key", existing["credentials"].(map[string]any)["apiKey"])

	// Verify merged result is independent
	merged["password"] = "changed"
	assert.Equal(t, "real-secret", existing["password"])
}

func TestIsZeroValue(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		isZero bool
	}{
		{"nil", nil, true},
		{"empty string", "", true},
		{"non-empty string", "hello", false},
		{"zero int", 0, true},
		{"non-zero int", 42, false},
		{"zero float64", float64(0), true},
		{"non-zero float64", 3.14, false},
		{"false bool", false, true},
		{"true bool", true, false},
		{"empty map", map[string]any{}, true},
		{"non-empty map", map[string]any{"a": 1}, false},
		{"empty slice", []any{}, true},
		{"non-empty slice", []any{"a"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.isZero, isZeroValue(tc.value))
		})
	}
}
