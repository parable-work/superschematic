package apigen

import (
	"embed"
	"encoding/json"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
	validator "github.com/santhosh-tekuri/jsonschema/v6"
)

// stubProvider is the smallest AuthProvider the in-package tests can use;
// the real providers (sessionauth, the extensions') import this package.
type stubProvider struct{}

func (stubProvider) Name() string { return "stub" }
func (stubProvider) Analyze(_, upstream *ir.Schema) (AuthModel, error) {
	return AnalyzeSessionStores(upstream), nil
}
func (stubProvider) Endpoint(*ir.FieldDef, *ir.OperationSet, *EndpointInfo) error { return nil }
func (stubProvider) Templates() embed.FS                                          { return embed.FS{} }
func (stubProvider) Funcs() template.FuncMap                                      { return nil }
func (stubProvider) Files(*APIOutput) []codegen.ConditionalFile                   { return nil }
func (stubProvider) OpenAPIParameters(*APIOutput) []map[string]any                { return nil }

func TestOpenAPIIncludesImportedDiscriminatedUnionMembers(t *testing.T) {
	apiSchema := ir.NewSchema("fixture-api", ir.SchemaKindAPI)
	apiSchema.Types["AcmePrimaryView"] = &ir.TypeDef{
		Name: "AcmePrimaryView",
		Role: ir.RoleAPIView,
		Fields: []*ir.FieldDef{{
			Name:     "configuration",
			TypeRef:  ir.TypeRef{Name: "AcmeTypeConfigurationView"},
			Required: true,
		}},
	}

	shared := ir.NewSchema("fixture-shared", ir.SchemaKindAPI)
	members := []struct {
		name  string
		value string
	}{
		{name: "ProblemAcmeConfigurationView", value: "PROBLEM"},
		{name: "PlaybookAcmeConfigurationView", value: "PLAYBOOK"},
		{name: "PlatformAcmeConfigurationView", value: "PLATFORM"},
		{name: "PrimitiveAcmeConfigurationView", value: "PRIMITIVE"},
	}
	memberNames := make([]string, 0, len(members))
	for _, member := range members {
		value := member.value
		memberNames = append(memberNames, member.name)
		shared.Types[member.name] = &ir.TypeDef{
			Name: member.name,
			Role: ir.RoleAPIView,
			Fields: []*ir.FieldDef{{
				Name:             "type",
				TypeRef:          ir.TypeRef{Name: "string"},
				Required:         true,
				InternalMetadata: true,
				Default:          &value,
			}},
		}
	}
	shared.Unions["AcmeTypeConfigurationView"] = &ir.UnionDef{
		Name:  "AcmeTypeConfigurationView",
		Types: memberNames,
	}

	schemas := buildOpenAPISchemas(
		[]EndpointInfo{{OutputType: "AcmePrimaryView"}},
		map[string]string{},
		map[string]string{},
		map[string]string{"string": "string"},
		apiSchema,
		map[string]*ir.Schema{"fixture-shared": shared},
	)
	union, ok := schemas["AcmeTypeConfigurationView"].(map[string]interface{})
	if !ok {
		t.Fatal("imported union component was not materialized")
	}
	oneOf, ok := union["oneOf"].([]map[string]interface{})
	if !ok || len(oneOf) != len(members) {
		t.Fatalf("union oneOf = %#v, want %d members", union["oneOf"], len(members))
	}
	discriminator, ok := union["discriminator"].(map[string]interface{})
	if !ok || discriminator["propertyName"] != "type" {
		t.Fatalf("union discriminator = %#v, want propertyName type", union["discriminator"])
	}
	mapping, ok := discriminator["mapping"].(map[string]string)
	if !ok || len(mapping) != len(members) {
		t.Fatalf("union discriminator mapping = %#v, want %d entries", discriminator["mapping"], len(members))
	}
	for _, member := range members {
		if _, ok := schemas[member.name]; !ok {
			t.Errorf("union member component %s was not materialized", member.name)
		}
	}
}

// TestGenerateFixtureAPIShape verifies endpoint extraction from fixture-api.
// nullableFixture is an API schema whose TapInfo type carries every nullable
// shape the OpenAPI writer encodes (scalar, local type ref, local and
// dependency enum refs, array, map) next to required counterparts.
func nullableFixture() (*ir.Schema, map[string]*ir.Schema) {
	schema := ir.NewSchema("fixture-api", ir.SchemaKindAPI)
	schema.Scalars["Identity.UUID"] = &ir.ScalarDef{
		Name:              "Identity.UUID",
		LanguagePrimitive: ir.LanguageString,
	}
	schema.Enums["TapKindEnum"] = &ir.EnumDef{
		Name:   "TapKindEnum",
		Values: []ir.EnumValueDef{{Name: "REST", SerializedAs: "rest"}},
	}
	schema.Types["TapConfig"] = &ir.TypeDef{
		Name: "TapConfig",
		Fields: []*ir.FieldDef{
			{Name: "key", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	schema.Types["TapInfo"] = &ir.TypeDef{
		Name: "TapInfo",
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
			{Name: "owner", TypeRef: ir.TypeRef{Name: "TapConfig"}, Required: true},
			{Name: "note", TypeRef: ir.TypeRef{Name: "string"}},
			{Name: "config", TypeRef: ir.TypeRef{Name: "TapConfig"}, Description: "Optional tap config."},
			{Name: "kind", TypeRef: ir.TypeRef{Name: "TapKindEnum"}},
			{Name: "region", TypeRef: ir.TypeRef{Name: "RegionEnum"}},
			{Name: "tags", TypeRef: ir.TypeRef{Name: "string", IsArray: true}},
			{Name: "labels", TypeRef: ir.TypeRef{Name: "string", IsMap: true}},
		},
	}
	dependencies := map[string]*ir.Schema{
		"enums": {
			Name: "enums",
			Enums: map[string]*ir.EnumDef{
				"RegionEnum": {
					Name:   "RegionEnum",
					Values: []ir.EnumValueDef{{Name: "US", SerializedAs: "us"}},
				},
			},
		},
	}
	return schema, dependencies
}

// assertNullableRef checks the OpenAPI 3.0 encoding of a nullable reference:
// a oneOf of the bare $ref and a branch that admits nothing but null, with
// no nullable, allOf, or type on the wrapper (see nullableOpenAPISchema).
func assertNullableRef(t *testing.T, field string, got map[string]interface{}, wantRef, wantNullType string) {
	t.Helper()
	for _, key := range []string{"$ref", "allOf", "nullable", "type"} {
		if _, ok := got[key]; ok {
			t.Errorf("%s: nullable ref wrapper must not carry %s: %#v", field, key, got)
		}
	}
	oneOf, ok := got["oneOf"].([]interface{})
	if !ok || len(oneOf) != 2 {
		t.Fatalf("%s: nullable ref oneOf = %#v, want [$ref, null branch]", field, got["oneOf"])
	}
	if ref := oneOf[0].(map[string]interface{})["$ref"]; ref != wantRef {
		t.Errorf("%s: nullable ref target = %#v, want %s", field, ref, wantRef)
	}
	nullBranch := oneOf[1].(map[string]interface{})
	if nullBranch["type"] != wantNullType || nullBranch["nullable"] != true {
		t.Errorf("%s: null branch = %#v, want type %s with nullable true", field, nullBranch, wantNullType)
	}
	if enum, ok := nullBranch["enum"].([]interface{}); !ok || len(enum) != 1 || enum[0] != nil {
		t.Errorf("%s: null branch enum = %#v, want [null]", field, nullBranch["enum"])
	}
}

// TestBuildOpenAPISchemasMarksNullableFields checks that non-required fields
// (Nullable<T> in the authoring form) carry the OpenAPI 3.0 nullable flag,
// and that nullable refs take the oneOf encoding since a ref cannot carry
// the flag itself.
func TestBuildOpenAPISchemasMarksNullableFields(t *testing.T) {
	schema, dependencies := nullableFixture()
	schemas := buildOpenAPISchemas(
		[]EndpointInfo{{OutputType: "TapInfo"}},
		nil,
		nil,
		buildOpenAPIScalarMap(schema, dependencies),
		schema,
		dependencies,
	)

	tapInfo, ok := schemas["TapInfo"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected TapInfo component, got %#v", schemas["TapInfo"])
	}
	properties := tapInfo["properties"].(map[string]interface{})

	id := properties["id"].(map[string]interface{})
	if _, ok := id["nullable"]; ok {
		t.Errorf("required field id must not be nullable: %#v", id)
	}

	note := properties["note"].(map[string]interface{})
	if note["nullable"] != true || note["type"] != "string" {
		t.Errorf("nullable scalar field = %#v, want type string with nullable true", note)
	}

	config := properties["config"].(map[string]interface{})
	assertNullableRef(t, "config", config, "#/components/schemas/TapConfig", "object")
	if config["description"] != "Optional tap config." {
		t.Errorf("nullable ref description = %#v, want it on the oneOf wrapper", config["description"])
	}
	assertNullableRef(t, "kind", properties["kind"].(map[string]interface{}), "#/components/schemas/TapKindEnum", "string")
	assertNullableRef(t, "region", properties["region"].(map[string]interface{}), "#/components/schemas/RegionEnum", "string")

	owner := properties["owner"].(map[string]interface{})
	if owner["$ref"] != "#/components/schemas/TapConfig" || len(owner) != 1 {
		t.Errorf("required ref field = %#v, want a bare $ref", owner)
	}

	tags := properties["tags"].(map[string]interface{})
	if tags["nullable"] != true || tags["type"] != "array" {
		t.Errorf("nullable array field = %#v, want type array with nullable true", tags)
	}
	if items := tags["items"].(map[string]interface{}); items["nullable"] != nil {
		t.Errorf("array items must not inherit field nullability: %#v", items)
	}

	labels := properties["labels"].(map[string]interface{})
	if labels["nullable"] != true || labels["type"] != "object" {
		t.Errorf("nullable map field = %#v, want type object with nullable true", labels)
	}

	required, _ := tapInfo["required"].([]string)
	if len(required) != 2 || required[0] != "id" || required[1] != "owner" {
		t.Errorf("TapInfo required = %#v, want [id owner]", tapInfo["required"])
	}
}

// openAPI30ToJSONSchema rewrites an OpenAPI 3.0 document into the JSON Schema
// a plain validator evaluates with 3.0.3 semantics: nullable adds null to the
// type declared in the same schema object and does nothing anywhere else.
func openAPI30ToJSONSchema(node interface{}) interface{} {
	switch v := node.(type) {
	case map[string]interface{}:
		if v["nullable"] == true {
			if typeName, ok := v["type"].(string); ok {
				v["type"] = []interface{}{typeName, "null"}
			}
		}
		delete(v, "nullable")
		for key, child := range v {
			v[key] = openAPI30ToJSONSchema(child)
		}
	case []interface{}:
		for i, child := range v {
			v[i] = openAPI30ToJSONSchema(child)
		}
	}
	return node
}

// TestOpenAPINullableEncodingValidatesUnder30Rules validates payloads against
// the generated components under the OpenAPI 3.0.3 reading of nullable.
// Optional fields must accept null while keeping the referenced constraints,
// and required fields must still reject null. The structural assertions
// above cannot make this check: a wrapper can look nullable and still reject
// null once the rules are applied.
func TestOpenAPINullableEncodingValidatesUnder30Rules(t *testing.T) {
	schema, dependencies := nullableFixture()
	schemas := buildOpenAPISchemas(
		[]EndpointInfo{{OutputType: "TapInfo"}},
		nil,
		nil,
		buildOpenAPIScalarMap(schema, dependencies),
		schema,
		dependencies,
	)
	raw, err := json.Marshal(map[string]interface{}{"components": map[string]interface{}{"schemas": schemas}})
	if err != nil {
		t.Fatal(err)
	}
	var doc interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	compiler := validator.NewCompiler()
	if err := compiler.AddResource("superschematic://openapi.json", openAPI30ToJSONSchema(doc)); err != nil {
		t.Fatal(err)
	}
	tapInfo, err := compiler.Compile("superschematic://openapi.json#/components/schemas/TapInfo")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		payload string
		valid   bool
	}{
		{"null optional scalar", `{"id":"a","owner":{"key":"k"},"note":null}`, true},
		{"null optional type ref", `{"id":"a","owner":{"key":"k"},"config":null}`, true},
		{"null optional enum ref", `{"id":"a","owner":{"key":"k"},"kind":null}`, true},
		{"null optional dependency enum ref", `{"id":"a","owner":{"key":"k"},"region":null}`, true},
		{"null optional array", `{"id":"a","owner":{"key":"k"},"tags":null}`, true},
		{"null optional map", `{"id":"a","owner":{"key":"k"},"labels":null}`, true},
		{"valid optional type ref", `{"id":"a","owner":{"key":"k"},"config":{"key":"k"}}`, true},
		{"valid optional enum ref", `{"id":"a","owner":{"key":"k"},"kind":"rest"}`, true},
		{"optional type ref keeps required members", `{"id":"a","owner":{"key":"k"},"config":{}}`, false},
		{"optional type ref rejects wrong type", `{"id":"a","owner":{"key":"k"},"config":"k"}`, false},
		{"optional enum ref keeps its members", `{"id":"a","owner":{"key":"k"},"kind":"soap"}`, false},
		{"null required scalar", `{"id":null,"owner":{"key":"k"}}`, false},
		{"null required type ref", `{"id":"a","owner":null}`, false},
	}
	for _, tc := range cases {
		var payload interface{}
		if err := json.Unmarshal([]byte(tc.payload), &payload); err != nil {
			t.Fatal(err)
		}
		err := tapInfo.Validate(payload)
		if (err == nil) != tc.valid {
			t.Errorf("%s: payload %s valid = %v, want %v (%v)", tc.name, tc.payload, err == nil, tc.valid, err)
		}
	}
}

// TestBuildOpenAPIPathsMarksNullableScalarBodyArgs checks the synthesized
// non-GET scalar-args request body: optional args become nullable properties,
// and an optional enum arg takes the nullable ref encoding.
func TestBuildOpenAPIPathsMarksNullableScalarBodyArgs(t *testing.T) {
	schema := ir.NewSchema("fixture-api", ir.SchemaKindAPI)
	schema.Enums["ArchiveReasonEnum"] = &ir.EnumDef{
		Name:   "ArchiveReasonEnum",
		Values: []ir.EnumValueDef{{Name: "CHURN", SerializedAs: "churn"}},
	}
	paths := buildOpenAPIPaths(
		&APIOutput{
			Provider: stubProvider{},
			Endpoints: []EndpointInfo{{
				Path:       "/tenants/archive",
				Method:     "POST",
				OutputType: "string",
				ScalarArgs: []Param{
					{Name: "reason", Type: "string", Required: true},
					{Name: "note", Type: "string"},
					{Name: "category", Type: "ArchiveReasonEnum"},
				},
			}},
		},
		nil,
		nil,
		buildOpenAPIScalarMap(schema, nil),
		schema,
		nil,
	)

	op := paths["/tenants/archive"].(map[string]interface{})["post"].(map[string]interface{})
	body := op["requestBody"].(map[string]interface{})
	bodySchema := body["content"].(map[string]interface{})["application/json"].(map[string]interface{})["schema"].(map[string]interface{})
	props := bodySchema["properties"].(map[string]interface{})

	reason := props["reason"].(map[string]interface{})
	if _, ok := reason["nullable"]; ok {
		t.Errorf("required body arg must not be nullable: %#v", reason)
	}
	note := props["note"].(map[string]interface{})
	if note["nullable"] != true {
		t.Errorf("optional body arg = %#v, want nullable true", note)
	}
	assertNullableRef(t, "category", props["category"].(map[string]interface{}), "#/components/schemas/ArchiveReasonEnum", "string")
	if required, _ := bodySchema["required"].([]string); len(required) != 1 || required[0] != "reason" {
		t.Errorf("body required = %#v, want [reason]", bodySchema["required"])
	}
}

func TestBuildOpenAPISchemasIncludesDependencyEnums(t *testing.T) {
	schema := ir.NewSchema("fixture-api", ir.SchemaKindAPI)
	schema.Types["OAuthLoginInput"] = &ir.TypeDef{
		Name: "OAuthLoginInput",
		Fields: []*ir.FieldDef{
			{
				Name:     "provider",
				TypeRef:  ir.TypeRef{Name: "OAuthProviderEnum"},
				Required: true,
			},
		},
	}
	dependencies := map[string]*ir.Schema{
		"enums": {
			Name: "enums",
			Enums: map[string]*ir.EnumDef{
				"OAuthProviderEnum": {
					Name: "OAuthProviderEnum",
					Values: []ir.EnumValueDef{
						{Name: "GOOGLE", SerializedAs: "google"},
					},
				},
			},
		},
	}

	schemas := buildOpenAPISchemas(
		[]EndpointInfo{{InputType: "OAuthLoginInput", HasInput: true}},
		nil,
		nil,
		buildOpenAPIScalarMap(schema, dependencies),
		schema,
		dependencies,
	)

	enumSchema, ok := schemas["OAuthProviderEnum"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected OAuthProviderEnum component, got %#v", schemas["OAuthProviderEnum"])
	}
	values, ok := enumSchema["enum"].([]string)
	if !ok || len(values) != 1 || values[0] != "google" {
		t.Fatalf("OAuthProviderEnum enum values = %#v, want [google]", enumSchema["enum"])
	}
}

func TestBuildOpenAPISchemasIncludesDependencyTypes(t *testing.T) {
	schema := ir.NewSchema("fixture-api", ir.SchemaKindAPI)
	dependencies := map[string]*ir.Schema{
		"connectors-web-types": {
			Name: "connectors-web-types",
			Types: map[string]*ir.TypeDef{
				"CreateConnectorInput": {
					Name: "CreateConnectorInput",
					Fields: []*ir.FieldDef{
						{
							Name:     "name",
							TypeRef:  ir.TypeRef{Name: "String"},
							Required: true,
						},
					},
				},
				"Connector": {
					Name: "Connector",
					Fields: []*ir.FieldDef{
						{
							Name:     "id",
							TypeRef:  ir.TypeRef{Name: "Identity.UUID"},
							Required: true,
						},
						{
							Name:     "name",
							TypeRef:  ir.TypeRef{Name: "String"},
							Required: true,
						},
					},
				},
			},
			Scalars: map[string]*ir.ScalarDef{
				"Identity.UUID": {
					Name:              "Identity.UUID",
					LanguagePrimitive: ir.LanguageString,
				},
			},
		},
	}

	schemas := buildOpenAPISchemas(
		[]EndpointInfo{{
			OutputType: "Connector",
			ScalarArgs: []Param{{
				Name: "input",
				Type: "CreateConnectorInput",
			}},
		}},
		nil,
		nil,
		buildOpenAPIScalarMap(schema, dependencies),
		schema,
		dependencies,
	)

	connectorSchema, ok := schemas["Connector"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected Connector component, got %#v", schemas["Connector"])
	}
	properties, ok := connectorSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("Connector properties = %#v, want object", connectorSchema["properties"])
	}
	if _, ok := properties["id"]; !ok {
		t.Fatalf("Connector properties missing id: %#v", properties)
	}
	if _, ok := properties["name"]; !ok {
		t.Fatalf("Connector properties missing name: %#v", properties)
	}
	if _, ok := schemas["CreateConnectorInput"]; !ok {
		t.Fatalf("expected CreateConnectorInput component, got keys %#v", schemas)
	}
}

func TestGenerateClassifiesDependencyInputTypesAsRequestBody(t *testing.T) {
	schema := ir.NewSchema("fixture-api", ir.SchemaKindAPI)
	schema.OperationSets = []*ir.OperationSet{
		{
			Name: "ConnectorsMutations",
			Operations: []*ir.FieldDef{
				{
					Name:       "createConnector",
					HTTPMethod: "POST",
					RestPath:   "connectors",
					TypeRef:    ir.TypeRef{Name: "Connector"},
					Arguments: []*ir.ArgumentDef{
						{
							Name:     "input",
							TypeRef:  ir.TypeRef{Name: "CreateConnectorInput"},
							Required: true,
						},
					},
				},
			},
		},
	}
	dependencies := map[string]*ir.Schema{
		"connectors-web-types": {
			Name: "connectors-web-types",
			Types: map[string]*ir.TypeDef{
				"CreateConnectorInput": {
					Name: "CreateConnectorInput",
					Role: ir.RoleAPIInput,
					Fields: []*ir.FieldDef{
						{Name: "name", TypeRef: ir.TypeRef{Name: "String"}, Required: true},
					},
				},
				"Connector": {
					Name: "Connector",
					Role: ir.RoleAPIView,
					Fields: []*ir.FieldDef{
						{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
						{Name: "name", TypeRef: ir.TypeRef{Name: "String"}, Required: true},
					},
				},
			},
		},
	}

	output, err := Generate(schema, Options{
		Provider:     stubProvider{},
		SchemaName:   "fixture-api",
		ModulePath:   "example.com/schemas/api/fixture-api",
		TypesModule:  "example.com/schemas/types/go/fixture-api",
		Dependencies: dependencies,
		Clock:        codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output == nil || len(output.Endpoints) != 1 {
		t.Fatalf("expected one endpoint, got %#v", output)
	}
	endpoint := output.Endpoints[0]
	if !endpoint.HasInput {
		t.Fatalf("dependency CreateConnectorInput classified as scalar args: %#v", endpoint.ScalarArgs)
	}
	if endpoint.InputType != "CreateConnectorInput" {
		t.Fatalf("InputType = %q, want CreateConnectorInput", endpoint.InputType)
	}
	if len(endpoint.ScalarArgs) != 0 {
		t.Fatalf("ScalarArgs = %#v, want none", endpoint.ScalarArgs)
	}
}

// updateUserSchema builds the regression shape: PATCH users/{userId} taking a
// userId argument plus an input body. queryParam controls whether userId is
// declared @query, which is the contradiction the guard must reject.
func updateUserSchema(queryParam bool) (*ir.Schema, map[string]*ir.Schema) {
	schema := ir.NewSchema("fixture-api", ir.SchemaKindAPI)
	schema.OperationSets = []*ir.OperationSet{
		{
			Name: "UsersMutations",
			Operations: []*ir.FieldDef{
				{
					Name:       "updateUser",
					HTTPMethod: "PATCH",
					RestPath:   "users/{userId}",
					TypeRef:    ir.TypeRef{Name: "UserBase"},
					Arguments: []*ir.ArgumentDef{
						{
							Name:     "userId",
							TypeRef:  ir.TypeRef{Name: "Identity.UUID"},
							Required: true,
							IsQuery:  queryParam,
						},
						{
							Name:     "update",
							TypeRef:  ir.TypeRef{Name: "UpdateUserInput"},
							Required: true,
						},
					},
				},
			},
		},
	}
	dependencies := map[string]*ir.Schema{
		"users-web-types": {
			Name: "users-web-types",
			Types: map[string]*ir.TypeDef{
				"UpdateUserInput": {
					Name: "UpdateUserInput",
					Role: ir.RoleAPIInput,
					Fields: []*ir.FieldDef{
						{Name: "name", TypeRef: ir.TypeRef{Name: "String"}, Required: true},
					},
				},
				"UserBase": {
					Name: "UserBase",
					Role: ir.RoleAPIView,
					Fields: []*ir.FieldDef{
						{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
					},
				},
			},
		},
	}
	return schema, dependencies
}

func generateUpdateUser(t *testing.T, queryParam bool) (*APIOutput, error) {
	t.Helper()
	schema, dependencies := updateUserSchema(queryParam)
	return Generate(schema, Options{
		Provider:     stubProvider{},
		SchemaName:   "fixture-api",
		ModulePath:   "example.com/schemas/api/fixture-api",
		TypesModule:  "example.com/schemas/types/go/fixture-api",
		Dependencies: dependencies,
		Clock:        codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
}

// Regression: a @query argument whose name is embedded in the rest path is a
// contradiction. It used to be accepted silently, producing a handler that read
// the id from the query string and an SDK that never substituted {userId}.
func TestGenerateRejectsQueryParamEmbeddedInRestPath(t *testing.T) {
	_, err := generateUpdateUser(t, true)
	if err == nil {
		t.Fatal("expected an error for a @query argument embedded in the rest path, got nil")
	}
	if !strings.Contains(err.Error(), "userId") || !strings.Contains(err.Error(), "users/{userId}") {
		t.Fatalf("error should name the argument and the path, got: %v", err)
	}
}

// Regression: without the QueryParam<> wrapper, userId must be
// classified as a path parameter and must not leak into the query string.
func TestGenerateClassifiesPathEmbeddedArgAsPathParam(t *testing.T) {
	output, err := generateUpdateUser(t, false)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output == nil || len(output.Endpoints) != 1 {
		t.Fatalf("expected one endpoint, got %#v", output)
	}
	endpoint := output.Endpoints[0]
	if len(endpoint.PathParams) != 1 || endpoint.PathParams[0].Name != "userId" {
		t.Fatalf("PathParams = %#v, want exactly userId", endpoint.PathParams)
	}
	if len(endpoint.QueryParams) != 0 {
		t.Fatalf("QueryParams = %#v, want none", endpoint.QueryParams)
	}
	if !endpoint.HasInput || endpoint.InputType != "UpdateUserInput" {
		t.Fatalf("expected UpdateUserInput request body, got HasInput=%v InputType=%q", endpoint.HasInput, endpoint.InputType)
	}
}
