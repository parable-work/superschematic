package mask

import (
	"testing"

	"github.com/parable-work/superschematic/ir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testMaskSchema() *ir.Schema {
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
			{Name: "enabled", TypeRef: ir.TypeRef{Name: "Boolean"}, Required: true},
			{Name: "password", TypeRef: ir.TypeRef{Name: "String"}, Required: true, Secret: true},
			{Name: "pin", TypeRef: ir.TypeRef{Name: "Int"}, Required: true, Secret: true},
			{Name: "activeSecret", TypeRef: ir.TypeRef{Name: "Boolean"}, Required: true, Secret: true},
			{Name: "optionalSecret", TypeRef: ir.TypeRef{Name: "String"}, Required: false, Secret: true},
			{Name: "credentials", TypeRef: ir.TypeRef{Name: "Credentials"}, Required: false},
			{Name: "endpoints", TypeRef: ir.TypeRef{Name: "Endpoint", IsArray: true, ElemNonNull: true}, Required: false},
			{Name: "secretTags", TypeRef: ir.TypeRef{Name: "String", IsArray: true, ElemNonNull: true}, Required: true, Secret: true},
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

func TestMasker_TableDrivenScenarios(t *testing.T) {
	m := New(testMaskSchema())

	type testCase struct {
		name     string
		maskKind string
		typeName string
		data     map[string]any
		expected map[string]any
		wantNil  bool
	}

	tests := []testCase{
		{
			name:     "nil data returns nil",
			maskKind: "type",
			typeName: "ServiceConfig",
			data:     nil,
			wantNil:  true,
		},
		{
			name:     "empty map returns empty map",
			maskKind: "type",
			typeName: "ServiceConfig",
			data:     map[string]any{},
			expected: map[string]any{},
		},
		{
			name:     "secret scalar fields are zeroed while non-secret fields pass through",
			maskKind: "type",
			typeName: "ServiceConfig",
			data: map[string]any{
				"name":         "connector-a",
				"retries":      5,
				"enabled":      true,
				"password":     "top-secret",
				"pin":          1234,
				"activeSecret": true,
			},
			expected: map[string]any{
				"name":         "connector-a",
				"retries":      5,
				"enabled":      true,
				"password":     "",
				"pin":          0,
				"activeSecret": false,
			},
		},
		{
			name:     "nested type fields are recursively masked and unknown fields pass through",
			maskKind: "type",
			typeName: "ServiceConfig",
			data: map[string]any{
				"name":    "connector-b",
				"retries": 2,
				"enabled": true,
				"credentials": map[string]any{
					"username": "alice",
					"apiKey":   "abc-123",
					"unknown":  "preserve-me",
				},
				"unknownTop": "keep-top-level",
			},
			expected: map[string]any{
				"name":    "connector-b",
				"retries": 2,
				"enabled": true,
				"credentials": map[string]any{
					"username": "alice",
					"apiKey":   "",
					"unknown":  "preserve-me",
				},
				"unknownTop": "keep-top-level",
			},
		},
		{
			name:     "array of nested types is recursively masked",
			maskKind: "type",
			typeName: "ServiceConfig",
			data: map[string]any{
				"name":    "connector-c",
				"retries": 3,
				"enabled": true,
				"endpoints": []any{
					map[string]any{"url": "https://a.example.com", "token": "t1"},
					map[string]any{"url": "https://b.example.com", "token": "t2"},
				},
			},
			expected: map[string]any{
				"name":    "connector-c",
				"retries": 3,
				"enabled": true,
				"endpoints": []any{
					map[string]any{"url": "https://a.example.com", "token": ""},
					map[string]any{"url": "https://b.example.com", "token": ""},
				},
			},
		},
		{
			name:     "required array of secret scalars is zeroed",
			maskKind: "type",
			typeName: "ServiceConfig",
			data: map[string]any{
				"name":       "connector-d",
				"retries":    1,
				"enabled":    true,
				"secretTags": []any{"a", "b", "c"},
			},
			expected: map[string]any{
				"name":       "connector-d",
				"retries":    1,
				"enabled":    true,
				"secretTags": []any{},
			},
		},
		{
			name:     "optional nil fields are preserved",
			maskKind: "type",
			typeName: "ServiceConfig",
			data: map[string]any{
				"name":           "connector-e",
				"retries":        4,
				"enabled":        false,
				"optionalSecret": nil,
				"credentials":    nil,
			},
			expected: map[string]any{
				"name":           "connector-e",
				"retries":        4,
				"enabled":        false,
				"optionalSecret": nil,
				"credentials":    nil,
			},
		},
		{
			name:     "__typename is preserved",
			maskKind: "type",
			typeName: "ServiceConfig",
			data: map[string]any{
				"__typename": "ServiceConfig",
				"name":       "connector-f",
				"retries":    7,
				"enabled":    true,
				"password":   "secret",
			},
			expected: map[string]any{
				"__typename": "ServiceConfig",
				"name":       "connector-f",
				"retries":    7,
				"enabled":    true,
				"password":   "",
			},
		},
		{
			name:     "input masking works recursively",
			maskKind: "input",
			typeName: "ConfigInput",
			data: map[string]any{
				"displayName": "My Connector",
				"auth": map[string]any{
					"clientId":     "abc",
					"clientSecret": "very-secret",
				},
			},
			expected: map[string]any{
				"displayName": "My Connector",
				"auth": map[string]any{
					"clientId":     "abc",
					"clientSecret": "",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var masked map[string]any
			switch tc.maskKind {
			case "input":
				masked = m.MaskInput(tc.typeName, tc.data)
			default:
				masked = m.MaskType(tc.typeName, tc.data)
			}

			if tc.wantNil {
				assert.Nil(t, masked)
				return
			}

			assert.Equal(t, tc.expected, masked)
		})
	}
}

func TestMasker_DoesNotMutateInput(t *testing.T) {
	m := New(testMaskSchema())

	input := map[string]any{
		"name":    "immutable",
		"retries": 1,
		"enabled": true,
		"credentials": map[string]any{
			"username": "alice",
			"apiKey":   "should-stay-in-input",
		},
		"endpoints": []any{
			map[string]any{"url": "https://api.example.com", "token": "nested-secret"},
		},
	}

	originalNested := input["credentials"].(map[string]any)
	originalArray := input["endpoints"].([]any)

	masked := m.MaskType("ServiceConfig", input)

	require.NotNil(t, masked)
	assert.Equal(t, "should-stay-in-input", input["credentials"].(map[string]any)["apiKey"])
	assert.Equal(t, "nested-secret", input["endpoints"].([]any)[0].(map[string]any)["token"])

	maskedArray, ok := masked["endpoints"].([]any)
	require.True(t, ok)

	maskedCredentials := masked["credentials"].(map[string]any)
	maskedCredentials["apiKey"] = "changed-in-masked"
	maskedArray[0].(map[string]any)["token"] = "changed-in-masked"

	assert.Equal(t, "should-stay-in-input", originalNested["apiKey"])
	assert.Equal(t, "nested-secret", originalArray[0].(map[string]any)["token"])
}
