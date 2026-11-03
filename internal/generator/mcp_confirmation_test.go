package generator

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestAnAPIWhoseToolsAskHoldsItsConfirmationKey: an API whose MCP endpoint
// serves a tool that asks signs its confirmations with the secret its
// config names, outputs.api.mcpConfirmationKey, by its declaring type and
// field, so its @envVars must hold that field, a required Secret<string>;
// the build refuses it otherwise, naming the tools, where the class lives
// and the docs section, and an endpoint whose tools all run as called
// needs no key (D93).
func TestAnAPIWhoseToolsAskHoldsItsConfirmationKey(t *testing.T) {
	reg, err := assembleCore(naming.Default())
	if err != nil {
		t.Fatal(err)
	}
	asking := &tsrestgen.MCPInfo{Confirming: []string{"update_order", "cancel_order"}}
	good := ir.FieldDef{Name: "MCP_CONFIRMATION_KEY", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Secret: true, InheritedFrom: "McpKeys"}
	ordersAPI := func(fields ...ir.FieldDef) *ir.Schema {
		td := &ir.TypeDef{Name: "OrdersApiConfig", EnvVars: true}
		for i := range fields {
			td.Fields = append(td.Fields, &fields[i])
		}
		return &ir.Schema{Name: "orders-api", Kind: ir.SchemaKindAPI, Types: map[string]*ir.TypeDef{td.Name: td}}
	}
	ordersRun := func(api *ir.Schema, key string) run {
		apiConfig := &registry.APIOutputConfig{Enabled: true, Language: registry.APILanguageTypeScript, MCP: true, MCPConfirmationKey: key}
		return run{registry.GenerateContext{Schema: api, Config: &schemaconfig.SchemaConfig{Name: api.Name}, Registry: reg, Outputs: &registry.Outputs{API: apiConfig}}}
	}

	field, err := ordersRun(ordersAPI(good), "McpKeys.MCP_CONFIRMATION_KEY").mcpConfirmationKeyField(asking)
	if err != nil || field == nil || field.Name != "MCP_CONFIRMATION_KEY" {
		t.Fatalf("mcpConfirmationKeyField = %v, %v", field, err)
	}
	for _, mcp := range []*tsrestgen.MCPInfo{nil, {}} {
		if field, err := ordersRun(ordersAPI(), "").mcpConfirmationKeyField(mcp); field != nil || err != nil {
			t.Errorf("an endpoint with no tool that asks needs no key: %v, %v", field, err)
		}
	}

	declaredHere := good
	declaredHere.InheritedFrom = ""
	notSecret := good
	notSecret.Secret = false
	for _, tc := range []struct {
		name string
		api  *ir.Schema
		key  string
		want string
	}{
		{"no key", ordersAPI(good), "", `generator: orders-api serves MCP tools that run only once the person confirms the call (update_order, cancel_order), and its endpoint signs their confirmations with an @envVars Secret<string> its config names; set outputs.api.mcpConfirmationKey in orders-api's config to the field's declaring type and name, such as "McpKeys.MCP_CONFIRMATION_KEY", and declare that field, a random value of at least 32 bytes in each environment, in orders-api's @envVars class or a class it extends (see ` + MCPConfirmationDocs + `)`},
		{"no field", ordersAPI(), "McpKeys.MCP_CONFIRMATION_KEY", `generator: orders-api's MCP endpoint signs the confirmations of update_order, cancel_order with the secret McpKeys.MCP_CONFIRMATION_KEY (outputs.api.mcpConfirmationKey in orders-api's config), and its @envVars class holds no field MCP_CONFIRMATION_KEY; declare MCP_CONFIRMATION_KEY: Secret<string> in an @envVars class McpKeys, in orders-api's schema or in a General service orders-api depends on, and make orders-api's @envVars class McpKeys or extend it (see ` + MCPConfirmationDocs + `)`},
		{"another secret", ordersAPI(declaredHere), "McpKeys.MCP_CONFIRMATION_KEY", "its @envVars field MCP_CONFIRMATION_KEY is declared by OrdersApiConfig, so it is the secret OrdersApiConfig.MCP_CONFIRMATION_KEY"},
		{"no secret", ordersAPI(notSecret), "McpKeys.MCP_CONFIRMATION_KEY", "its @envVars field MCP_CONFIRMATION_KEY is no required Secret<string>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ordersRun(tc.api, tc.key).mcpConfirmationKeyField(asking)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("mcpConfirmationKeyField = %v, want it to say %q", err, tc.want)
			}
		})
	}
}
