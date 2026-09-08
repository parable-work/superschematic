package sdkgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/toolsutil"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
)

type ToolPathParam = toolsutil.ToolPathParam
type ToolScalarArg = toolsutil.ToolScalarArg
type JSONSchemaObject = toolsutil.JSONSchemaObject
type JSONSchemaProperty = toolsutil.JSONSchemaProperty
type JSONSchemaReturn = toolsutil.JSONSchemaReturn

// ToolDefinition represents a single tool for LLM function cOsalling
type ToolDefinition struct {
	Name         string           // Namespaced name e.g., "auth.sendMagicLink"
	APIID        string           // API identifier e.g., "web-api"
	MethodName   string           // SDK method name e.g., "sendMagicLink"
	HTTPMethod   string           // HTTP method e.g., "GET", "POST"
	HTTPPath     string           // HTTP path template e.g., "/api/auth/send-magic-link"
	Description  string           // From GraphQL @description, includes auth note if required
	RequiresAuth bool             // Whether authentication is required
	Namespace    string           // Namespace for grouping e.g., "auth"
	IsTenantNS   bool             // Whether this is a tenant-scoped endpoint
	Parameters   JSONSchemaObject // JSON Schema for parameters
	Returns      JSONSchemaReturn // JSON Schema for return type
	PathParams   []ToolPathParam  // Path parameters for invocation
	HasInput     bool             // Whether endpoint has input type
	InputType    string           // Input type name if HasInput
	ScalarArgs   []ToolScalarArg  // Scalar arguments (not path params)
	QueryArgs    []ToolScalarArg  // Query arguments
	Encrypted    bool             // Whether endpoint payload must be encrypted
	MCPBinding   MCPToolBinding   // Explicit MCP invocation binding metadata
}

// MCPToolBinding describes deterministic runtime invocation bindings.
type MCPToolBinding struct {
	ToolName    string                     `json:"toolName"`
	APIID       string                     `json:"apiId"`
	Namespace   string                     `json:"namespace"`
	MethodName  string                     `json:"methodName"`
	IsTenantNS  bool                       `json:"isTenantScoped"`
	TenantParam string                     `json:"tenantParam,omitempty"`
	Arguments   []MCPToolArgumentBinding   `json:"arguments"`
	MethodArgs  []MCPMethodArgumentBinding `json:"methodArgs"`
}

// MCPToolArgumentBinding maps one tool parameter to an invocation target.
type MCPToolArgumentBinding struct {
	ToolParameter string `json:"toolParameter"`
	Required      bool   `json:"required"`
	Kind          string `json:"kind"`
	Target        string `json:"target"`
}

// MCPMethodArgumentBinding defines SDK method argument order and sources.
type MCPMethodArgumentBinding struct {
	Position int      `json:"position"`
	Kind     string   `json:"kind"`
	Target   string   `json:"target"`
	Sources  []string `json:"sources,omitempty"`
}

// ToolsOutput contains all generated tool definitions
type ToolsOutput struct {
	SchemaName        string           // Schema name e.g., "web-api"
	APIID             string           // API identifier for MCP binding artifacts
	SDKClassName      string           // SDK class name e.g., "WebApiSDK"
	TypesPackage      string           // Types package name
	Tools             []ToolDefinition // All tool definitions
	Namespaces        []ToolsNamespace // Tools grouped by namespace
	Timestamp         string           // Generation timestamp
	MCPBindingVersion string           // MCP binding manifest schema version
}

// ToolsNamespace groups tools by namespace for TypeScript generation
type ToolsNamespace struct {
	Name       string           // Namespace name e.g., "auth"
	ClassName  string           // Namespace class name e.g., "AuthNamespace"
	Tools      []ToolDefinition // Tools in this namespace
	IsTenantNS bool             // Whether this is tenant namespace
}

// GenerateTools generates tool calling bindings from SDK and API output
func GenerateTools(sdkOutput *SDKOutput, apiOutput *apigen.APIOutput, clock codegen.Clock) (*ToolsOutput, error) {
	if sdkOutput == nil || len(sdkOutput.Namespaces) == 0 {
		return nil, nil
	}

	output := &ToolsOutput{
		SchemaName:        sdkOutput.SchemaName,
		APIID:             sdkOutput.SchemaName,
		SDKClassName:      sdkOutput.SDKClassName,
		TypesPackage:      sdkOutput.TypesPackage,
		Tools:             []ToolDefinition{},
		Namespaces:        []ToolsNamespace{},
		Timestamp:         clock.RFC3339(),
		MCPBindingVersion: "1.0",
	}

	// Build a map of input types for field expansion
	inputTypeFields := toolsutil.BuildInputTypeFieldsMap(apiOutput)

	// Get scalars from API output (dynamically extracted from schema)
	scalars := apiOutput.Scalars

	// Process each namespace
	for _, ns := range sdkOutput.Namespaces {
		toolsNS := ToolsNamespace{
			Name:       ns.Name,
			ClassName:  ns.ClassName,
			Tools:      []ToolDefinition{},
			IsTenantNS: ns.IsTenantNS,
		}

		for _, endpoint := range ns.Endpoints {
			tool := endpointToTool(output.APIID, endpoint, ns, inputTypeFields, scalars)
			toolsNS.Tools = append(toolsNS.Tools, tool)
			output.Tools = append(output.Tools, tool)
		}

		output.Namespaces = append(output.Namespaces, toolsNS)
	}

	return output, nil
}

// endpointToTool converts an SDK endpoint to a tool definition
func endpointToTool(apiID string, endpoint EndpointInfo, ns NamespaceInfo, inputTypeFields map[string][]apigen.Param, scalars map[string]apigen.ScalarJSONSchemaInfo) ToolDefinition {
	// Build namespaced tool name
	toolName := fmt.Sprintf("%s.%s", ns.Name, endpoint.Name)

	// Build description with auth note if required
	description := endpoint.Description
	if description == "" {
		description = fmt.Sprintf("%s endpoint", endpoint.Name)
	}
	if endpoint.RequiresAuth {
		description = fmt.Sprintf("%s (Requires authentication)", description)
	}

	// Build path params for invocation
	pathParams := make([]ToolPathParam, len(endpoint.PathParams))
	for i, p := range endpoint.PathParams {
		pathParams[i] = ToolPathParam{
			Name:   p.Name,
			TSName: p.TSName,
			Type:   p.IRType,
		}
	}

	// Build scalar args for invocation
	scalarArgs := make([]ToolScalarArg, len(endpoint.ScalarArgs))
	for i, arg := range endpoint.ScalarArgs {
		scalarArgs[i] = ToolScalarArg{
			Name:     arg.Name,
			TSName:   tsutil.ToCamelCase(arg.Name),
			Type:     arg.Type,
			Required: arg.Required,
		}
	}

	// Build query args for invocation
	queryArgs := make([]ToolScalarArg, len(endpoint.QueryParams))
	for i, arg := range endpoint.QueryParams {
		queryArgs[i] = ToolScalarArg{
			Name:     arg.Name,
			TSName:   tsutil.ToCamelCase(arg.Name),
			Required: arg.Required,
		}
	}

	// Build parameters JSON Schema
	parameters := buildParametersSchema(endpoint, inputTypeFields, scalars, pathParams, scalarArgs)

	// Build return type schema
	returns := buildReturnSchema(endpoint, scalars)

	mcpBinding := buildMCPToolBinding(apiID, toolName, endpoint, ns, inputTypeFields, parameters)

	return ToolDefinition{
		Name:         toolName,
		APIID:        apiID,
		MethodName:   endpoint.Name,
		HTTPMethod:   endpoint.Method,
		HTTPPath:     endpoint.Path,
		Description:  description,
		RequiresAuth: endpoint.RequiresAuth,
		Namespace:    ns.Name,
		IsTenantNS:   ns.IsTenantNS,
		Parameters:   parameters,
		Returns:      returns,
		PathParams:   pathParams,
		HasInput:     endpoint.HasInput,
		InputType:    endpoint.InputType,
		ScalarArgs:   scalarArgs,
		QueryArgs:    queryArgs,
		Encrypted:    endpoint.Encrypted,
		MCPBinding:   mcpBinding,
	}
}

func buildMCPToolBinding(
	apiID string,
	toolName string,
	endpoint EndpointInfo,
	ns NamespaceInfo,
	inputTypeFields map[string][]apigen.Param,
	parameters JSONSchemaObject,
) MCPToolBinding {
	required := make(map[string]struct{}, len(parameters.Required))
	for _, name := range parameters.Required {
		required[name] = struct{}{}
	}

	arguments := make([]MCPToolArgumentBinding, 0)
	methodArgs := make([]MCPMethodArgumentBinding, 0)
	position := 0

	nonTenantPathSources := make([]string, 0)
	for _, pathParam := range endpoint.PathParams {
		paramName := pathParam.TSName
		_, isRequired := required[paramName]
		arguments = append(arguments, MCPToolArgumentBinding{
			ToolParameter: paramName,
			Required:      isRequired,
			Kind:          "path",
			Target:        fmt.Sprintf("path.%s", pathParam.Name),
		})
		if ns.IsTenantNS && pathParam.Name == ns.TenantParamName {
			continue
		}
		nonTenantPathSources = append(nonTenantPathSources, paramName)
	}
	if len(nonTenantPathSources) > 0 {
		methodArgs = append(methodArgs, MCPMethodArgumentBinding{
			Position: position,
			Kind:     "path",
			Target:   "path",
			Sources:  nonTenantPathSources,
		})
		position++
	}

	querySources := make([]string, 0, len(endpoint.QueryParams))
	for _, queryParam := range endpoint.QueryParams {
		paramName := tsutil.ToCamelCase(queryParam.Name)
		_, isRequired := required[paramName]
		arguments = append(arguments, MCPToolArgumentBinding{
			ToolParameter: paramName,
			Required:      isRequired,
			Kind:          "query",
			Target:        fmt.Sprintf("query.%s", queryParam.Name),
		})
		querySources = append(querySources, paramName)
	}
	if len(querySources) > 0 {
		methodArgs = append(methodArgs, MCPMethodArgumentBinding{
			Position: position,
			Kind:     "query",
			Target:   "query",
			Sources:  querySources,
		})
		position++
	}

	inputSources := make([]string, 0)
	inputKind := ""
	if endpoint.HasInput && endpoint.InputType != "" {
		inputKind = "input"
		if fields, ok := inputTypeFields[endpoint.InputType]; ok {
			for _, field := range fields {
				paramName := tsutil.ToCamelCase(field.Name)
				_, isRequired := required[paramName]
				arguments = append(arguments, MCPToolArgumentBinding{
					ToolParameter: paramName,
					Required:      isRequired,
					Kind:          "input",
					Target:        fmt.Sprintf("input.%s", field.Name),
				})
				inputSources = append(inputSources, paramName)
			}
		}
	} else if len(endpoint.ScalarArgs) > 0 {
		inputKind = "scalar"
		for _, scalarArg := range endpoint.ScalarArgs {
			paramName := tsutil.ToCamelCase(scalarArg.Name)
			_, isRequired := required[paramName]
			arguments = append(arguments, MCPToolArgumentBinding{
				ToolParameter: paramName,
				Required:      isRequired,
				Kind:          "scalar",
				Target:        fmt.Sprintf("input.%s", scalarArg.Name),
			})
			inputSources = append(inputSources, paramName)
		}
	}
	if len(inputSources) > 0 {
		methodArgs = append(methodArgs, MCPMethodArgumentBinding{
			Position: position,
			Kind:     inputKind,
			Target:   "input",
			Sources:  inputSources,
		})
		position++
	}

	if endpoint.Encrypted {
		_, isRequired := required["publicEncryptionKey"]
		arguments = append(arguments, MCPToolArgumentBinding{
			ToolParameter: "publicEncryptionKey",
			Required:      isRequired,
			Kind:          "option",
			Target:        "options.publicEncryptionKey",
		})
		methodArgs = append(methodArgs, MCPMethodArgumentBinding{
			Position: position,
			Kind:     "option",
			Target:   "options",
			Sources:  []string{"publicEncryptionKey"},
		})
	}

	tenantParam := ""
	if ns.IsTenantNS {
		tenantParam = ns.TenantParamName
	}

	return MCPToolBinding{
		ToolName:    toolName,
		APIID:       apiID,
		Namespace:   ns.Name,
		MethodName:  endpoint.Name,
		IsTenantNS:  ns.IsTenantNS,
		TenantParam: tenantParam,
		Arguments:   arguments,
		MethodArgs:  methodArgs,
	}
}

func buildParametersSchema(
	endpoint EndpointInfo,
	inputTypeFields map[string][]apigen.Param,
	scalars map[string]apigen.ScalarJSONSchemaInfo,
	pathParams []ToolPathParam,
	scalarArgs []ToolScalarArg,
) JSONSchemaObject {
	return toolsutil.BuildParametersSchema(
		pathParams,
		endpoint.HasInput,
		endpoint.InputType,
		inputTypeFields,
		scalarArgs,
		endpoint.Encrypted,
		scalars,
		func(name, tsName string) string {
			if tsName != "" {
				return tsName
			}
			return tsutil.ToCamelCase(name)
		},
	)
}

func buildReturnSchema(endpoint EndpointInfo, scalars map[string]apigen.ScalarJSONSchemaInfo) JSONSchemaReturn {
	return toolsutil.BuildReturnSchema(endpoint.OutputType, endpoint.OutputIsArray, scalars)
}

// GetToolByName returns a tool definition by its namespaced name
func (t *ToolsOutput) GetToolByName(name string) *ToolDefinition {
	for i := range t.Tools {
		if t.Tools[i].Name == name {
			return &t.Tools[i]
		}
	}
	return nil
}

// GetToolsForNamespace returns all tools for a given namespace
func (t *ToolsOutput) GetToolsForNamespace(namespace string) []ToolDefinition {
	for _, ns := range t.Namespaces {
		if ns.Name == namespace {
			return ns.Tools
		}
	}
	return nil
}

// formatToolNameForTS formats a tool name for TypeScript constant
// e.g., "auth.sendMagicLink" -> "AUTH_SEND_MAGIC_LINK"
func formatToolNameForTS(name string) string {
	// Replace dots with underscores and convert to screaming snake case
	result := strings.ReplaceAll(name, ".", "_")
	return toScreamingSnakeCase(result)
}

// toScreamingSnakeCase converts camelCase to SCREAMING_SNAKE_CASE,
// treating hyphens as word boundaries.
func toScreamingSnakeCase(s string) string {
	var result strings.Builder
	for i, r := range s {
		if r == '-' {
			result.WriteRune('_')
			continue
		}
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteRune('_')
		}
		result.WriteRune(r)
	}
	return strings.ToUpper(result.String())
}
