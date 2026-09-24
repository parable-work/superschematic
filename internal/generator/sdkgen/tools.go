package sdkgen

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/toolsutil"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	ir "github.com/parable-work/superschematic/ir"
)

type ToolPathParam = toolsutil.ToolPathParam
type ToolScalarArg = toolsutil.ToolScalarArg
type ToolQueryArg = toolsutil.ToolQueryArg
type JSONSchemaObject = toolsutil.JSONSchemaObject
type JSONSchemaProperty = toolsutil.JSONSchemaProperty
type JSONSchemaReturn = toolsutil.JSONSchemaReturn

// ToolDefinition represents a single tool for LLM function calling
type ToolDefinition struct {
	Name                     string                   // Namespaced name e.g., "auth.sendMagicLink"
	OperationID              string                   // OpenAPI operation id
	Title                    string                   // @docs title
	MCP                      *ir.OperationMCP         // resolved @mcp record, _meta with the guidance; nil without @mcp
	Capability               string                   // @docs capability
	Lifecycle                string                   // @docs lifecycle
	Visibility               string                   // @docs visibility
	Audience                 string                   // @docs audience
	Guidance                 ir.ToolOperationGuidance // @docs guidance
	ReplayMode               string                   // @docs replay mode
	IdempotencyKeyPointers   []string                 // pointers into Parameters
	ExpectedRevisionPointers []string                 // pointers into Parameters
	RequiredPerms            []string                 // permissions the route requires
	APIID                    string                   // API identifier e.g., "orders-api"
	MethodName               string                   // SDK method name e.g., "sendMagicLink"
	HTTPMethod               string                   // HTTP method e.g., "GET", "POST"
	HTTPPath                 string                   // HTTP path template e.g., "/api/auth/send-magic-link"
	Description              string                   // @docs description, else the comment with an auth note
	RequiresAuth             bool                     // Whether authentication is required
	Namespace                string                   // Namespace for grouping e.g., "auth"
	IsScopedNS               bool                     // Whether the endpoint's namespace hoists a scope parameter
	Parameters               JSONSchemaObject         // JSON Schema for parameters
	Returns                  JSONSchemaReturn         // JSON Schema for return type
	PathParams               []ToolPathParam          // Path parameters for invocation
	HasInput                 bool                     // Whether endpoint has input type
	InputType                string                   // Input type name if HasInput
	ScalarArgs               []ToolScalarArg          // Scalar arguments (not path params)
	QueryArgs                []ToolQueryArg           // Query arguments, with shape and bounds
	Encrypted                bool                     // Whether endpoint payload must be encrypted
	BindingStatus            string                   // ready, or unsupported_multipart for a file upload
	InputSchemaDigest        string                   // sha256 of the encoded Parameters
	MCPBinding               MCPToolBinding           // Explicit MCP invocation binding metadata
}

// MCPToolBinding, MCPToolArgumentBinding and MCPMethodArgumentBinding are
// the ir wire types, so tools/mcp-binding.json and its Go consumers share
// one field list.
type (
	MCPToolBinding           = ir.ToolBinding
	MCPToolArgumentBinding   = ir.ToolArgumentBinding
	MCPMethodArgumentBinding = ir.ToolMethodArgumentBinding
)

// ToolsOutput contains all generated tool definitions
type ToolsOutput struct {
	SchemaName        string                      // Schema name e.g., "orders-api"
	APIID             string                      // API identifier for MCP binding artifacts
	SDKClassName      string                      // SDK class name e.g., "OrdersApiSDK"
	TypesPackage      string                      // Types package name
	Keys              apigen.ToolKeys             // vendor keys the documents are written with
	Invocation        apigen.ToolInvocationPolicy // the key and values the documents write a tool's invocation policy with
	Tools             []ToolDefinition            // All tool definitions
	VisibleTools      []ToolDefinition            // Tools with a visible @mcp record: the provider tool lists
	Namespaces        []ToolsNamespace            // Tools grouped by namespace
	Timestamp         string                      // Generation timestamp
	MCPBindingVersion string                      // MCP binding manifest schema version

	// HasListOfListsReturns reports whether a tool returns an array of
	// arrays (T[][]), whose return schema nests a second items level.
	HasListOfListsReturns bool
}

// ToolsNamespace groups tools by namespace for TypeScript generation
type ToolsNamespace struct {
	Name       string           // Namespace name e.g., "auth"
	ClassName  string           // Namespace class name e.g., "AuthNamespace"
	Tools      []ToolDefinition // Tools in this namespace
	IsScopedNS bool             // Whether the namespace hoists a scope parameter
	ScopeParam string           // Name of the hoisted scope parameter if IsScopedNS
}

// GenerateTools generates tool calling bindings from SDK and API output. It
// fails when an operation's @docs replay pointers do not resolve against
// its tool arguments.
func GenerateTools(sdkOutput *SDKOutput, apiOutput *apigen.APIOutput, clock codegen.Clock) (*ToolsOutput, error) {
	if sdkOutput == nil || len(sdkOutput.Namespaces) == 0 {
		return nil, nil
	}

	output := &ToolsOutput{
		SchemaName:        sdkOutput.SchemaName,
		APIID:             sdkOutput.SchemaName,
		SDKClassName:      sdkOutput.SDKClassName,
		TypesPackage:      sdkOutput.TypesPackage,
		Keys:              apiOutput.ToolKeys,
		Invocation:        apiOutput.ToolInvocation.OrDefault(),
		Tools:             []ToolDefinition{},
		VisibleTools:      []ToolDefinition{},
		Namespaces:        []ToolsNamespace{},
		Timestamp:         clock.RFC3339(),
		MCPBindingVersion: "1.0",
	}

	inputTypeFields := toolsutil.BuildInputTypeFieldsMap(apiOutput)
	scalars := apiOutput.Scalars

	for _, ns := range sdkOutput.Namespaces {
		toolsNS := ToolsNamespace{
			Name:       ns.Name,
			ClassName:  ns.ClassName,
			Tools:      []ToolDefinition{},
			IsScopedNS: ns.IsScopedNS,
			ScopeParam: ns.ScopeParamName,
		}

		for _, endpoint := range ns.Endpoints {
			tool := endpointToTool(output.APIID, endpoint, ns, inputTypeFields, apiOutput.TypeUnions, scalars, apiOutput.ToolKeys)
			if err := toolsutil.ValidateReplayContract(tool.Parameters, tool.ReplayMode, tool.IdempotencyKeyPointers, tool.ExpectedRevisionPointers); err != nil {
				return nil, fmt.Errorf("operation %s replay contract: %w", tool.OperationID, err)
			}
			toolsNS.Tools = append(toolsNS.Tools, tool)
			output.Tools = append(output.Tools, tool)
			if tool.Returns.Items != nil && tool.Returns.Items.Items != nil {
				output.HasListOfListsReturns = true
			}
			if tool.MCP != nil && !tool.MCP.Hidden {
				output.VisibleTools = append(output.VisibleTools, tool)
			}
		}

		output.Namespaces = append(output.Namespaces, toolsNS)
	}

	return output, nil
}

// endpointToTool converts an SDK endpoint to a tool definition
func endpointToTool(
	apiID string,
	endpoint EndpointInfo,
	ns NamespaceInfo,
	inputTypeFields map[string][]apigen.Param,
	inputTypeUnions map[string]apigen.ToolUnionInfo,
	scalars map[string]apigen.ScalarJSONSchemaInfo,
	keys apigen.ToolKeys,
) ToolDefinition {
	toolName := fmt.Sprintf("%s.%s", ns.Name, endpoint.Name)

	description := endpoint.Description
	if description == "" {
		description = fmt.Sprintf("%s endpoint", endpoint.Name)
	}
	if endpoint.Docs == nil && endpoint.RequiresAuth {
		description = fmt.Sprintf("%s (Requires authentication)", description)
	}

	pathParams := make([]ToolPathParam, len(endpoint.PathParams))
	for i, p := range endpoint.PathParams {
		pathParams[i] = ToolPathParam{
			Name:   p.Name,
			TSName: p.TSName,
			Type:   p.IRType,
		}
	}

	scalarArgs := make([]ToolScalarArg, len(endpoint.ScalarArgs))
	for i, arg := range endpoint.ScalarArgs {
		scalarArgs[i] = ToolScalarArg{
			Name:     arg.Name,
			TSName:   tsutil.ToCamelCase(arg.Name),
			Type:     arg.Type,
			Required: arg.Required,
			// The list shape: the argument schema is T, T[] or T[][].
			IsArray:         arg.IsArray,
			IsArrayOfArrays: arg.IsArrayOfArrays,
		}
	}

	queryArgs := make([]ToolQueryArg, len(endpoint.QueryParams))
	for i, arg := range endpoint.QueryParams {
		queryArgs[i] = ToolQueryArg{
			Name: arg.Name, TSName: tsutil.ToCamelCase(arg.Name), Type: arg.IRType,
			Required: arg.Required, IsArray: arg.IsArray, IsMap: arg.IsMap,
			ValidateMin: arg.ValidateMin, ValidateMax: arg.ValidateMax,
			ValidateMinLength: arg.ValidateMinLength, ValidateMaxLength: arg.ValidateMaxLength,
			ValidateListMin: arg.ValidateListMin, ValidateListMax: arg.ValidateListMax,
			ValidatePattern: arg.ValidatePattern,
		}
	}

	parameters := toolsutil.BuildParametersSchema(
		pathParams, queryArgs, endpoint.HasInput, endpoint.InputType, inputTypeFields, inputTypeUnions,
		scalarArgs, endpoint.Encrypted, scalars,
		func(name, tsName string) string {
			if tsName != "" {
				return tsName
			}
			return tsutil.ToCamelCase(name)
		},
		keys,
	)
	parametersJSON, _ := json.Marshal(parameters)

	bindingStatus := "ready"
	if endpoint.HasFileUpload {
		bindingStatus = "unsupported_multipart"
	}
	guidance := toolsutil.OperationGuidance(endpoint.Docs)

	tool := ToolDefinition{
		Name:                     toolName,
		OperationID:              endpoint.OperationID,
		Title:                    endpoint.Title,
		MCP:                      toolsutil.MCPWithGuidance(endpoint.MCP, guidance, keys.Guidance),
		Guidance:                 guidance,
		ReplayMode:               toolsutil.ReplayMode(endpoint.Docs),
		IdempotencyKeyPointers:   toolsutil.IdempotencyKeyPointers(endpoint.Docs),
		ExpectedRevisionPointers: toolsutil.ExpectedRevisionPointers(endpoint.Docs),
		RequiredPerms:            append([]string(nil), endpoint.RequiredPerms...),
		APIID:                    apiID,
		MethodName:               endpoint.Name,
		HTTPMethod:               endpoint.Method,
		HTTPPath:                 endpoint.Path,
		Description:              description,
		RequiresAuth:             endpoint.RequiresAuth,
		Namespace:                ns.Name,
		IsScopedNS:               ns.IsScopedNS,
		Parameters:               parameters,
		Returns:                  toolsutil.BuildReturnSchemaAtDepth(endpoint.OutputType, endpoint.OutputArrayDepth(), scalars),
		PathParams:               pathParams,
		HasInput:                 endpoint.HasInput,
		InputType:                endpoint.InputType,
		ScalarArgs:               scalarArgs,
		QueryArgs:                queryArgs,
		Encrypted:                endpoint.Encrypted,
		BindingStatus:            bindingStatus,
		InputSchemaDigest:        fmt.Sprintf("sha256:%x", sha256.Sum256(parametersJSON)),
		MCPBinding:               buildMCPToolBinding(apiID, toolName, endpoint, ns, inputTypeFields, parameters),
	}
	if docs := endpoint.Docs; docs != nil {
		tool.Capability = docs.Capability
		tool.Lifecycle = string(docs.Lifecycle)
		tool.Visibility = string(docs.Visibility)
		tool.Audience = string(docs.Audience)
	}
	return tool
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

	nonScopePathSources := make([]string, 0)
	for _, pathParam := range endpoint.PathParams {
		paramName := pathParam.TSName
		_, isRequired := required[paramName]
		arguments = append(arguments, MCPToolArgumentBinding{
			ToolParameter: paramName,
			Required:      isRequired,
			Kind:          "path",
			Target:        fmt.Sprintf("path.%s", pathParam.Name),
		})
		if ns.IsScopedNS && pathParam.Name == ns.ScopeParamName {
			continue
		}
		nonScopePathSources = append(nonScopePathSources, paramName)
	}
	if len(nonScopePathSources) > 0 {
		methodArgs = append(methodArgs, MCPMethodArgumentBinding{
			Position: position,
			Kind:     "path",
			Target:   "path",
			Sources:  nonScopePathSources,
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
	// The generated method takes its query object after the input.
	if len(querySources) > 0 {
		methodArgs = append(methodArgs, MCPMethodArgumentBinding{
			Position: position,
			Kind:     "query",
			Target:   "query",
			Sources:  querySources,
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

	scopeParam := ""
	if ns.IsScopedNS {
		scopeParam = ns.ScopeParamName
	}

	return MCPToolBinding{
		ToolName:   toolName,
		APIID:      apiID,
		Namespace:  ns.Name,
		MethodName: endpoint.Name,
		IsScoped:   ns.IsScopedNS,
		ScopeParam: scopeParam,
		Arguments:  arguments,
		MethodArgs: methodArgs,
	}
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
