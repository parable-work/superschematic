package rustsdkgen

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/nestedguard"
	"github.com/parable-work/superschematic/internal/generator/toolsutil"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	ir "github.com/parable-work/superschematic/ir"
)

// ToolDefinition represents a single tool for LLM function calling.
type ToolDefinition struct {
	Name                     string                   // Namespaced name (e.g. "auth.sendMagicLink")
	OperationID              string                   // OpenAPI operation id (the handler name)
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
	APIID                    string                   // generated API identifier
	MethodName               string                   // Rust SDK method name (e.g. "send_magic_link")
	HTTPMethod               string                   // HTTP method e.g., "GET", "POST"
	HTTPPath                 string                   // HTTP path template e.g., "/api/auth/send-magic-link"
	Description              string                   // @docs description, else the comment with an auth note
	RequiresAuth             bool                     // Whether authentication is required
	Namespace                string                   // Namespace for grouping (e.g. "auth")
	IsScopedNS               bool                     // Whether the endpoint's namespace hoists a scope parameter
	Parameters               JSONSchemaObject         // JSON Schema for parameters
	Returns                  JSONSchemaReturn         // JSON Schema for return type
	PathParams               []ToolPathParam          // Path parameters for invocation helpers
	HasInput                 bool                     // Whether endpoint has input type
	InputType                string                   // Input type name when HasInput
	ScalarArgs               []ToolScalarArg          // Scalar arguments (not path params)
	QueryArgs                []ToolQueryArg           // Query arguments, with shape and bounds
	Encrypted                bool                     // Whether endpoint payload must be encrypted
	BindingStatus            string                   // ready, or unsupported_multipart for a file upload
	InputSchemaDigest        string                   // sha256 of the encoded Parameters
}

type ToolPathParam = toolsutil.ToolPathParam
type ToolScalarArg = toolsutil.ToolScalarArg
type ToolQueryArg = toolsutil.ToolQueryArg
type JSONSchemaObject = toolsutil.JSONSchemaObject
type JSONSchemaProperty = toolsutil.JSONSchemaProperty
type JSONSchemaReturn = toolsutil.JSONSchemaReturn

// ToolsOutput contains all generated tool definitions.
type ToolsOutput struct {
	SchemaName    string                      // Schema name (e.g. "orders-api")
	APIID         string                      // API identifier for the audit document
	SDKStructName string                      // SDK struct name (e.g. "OrdersApiSdk")
	TypesCrate    string                      // Rust types crate
	Keys          apigen.ToolKeys             // vendor keys the documents are written with
	Invocation    apigen.ToolInvocationPolicy // the key and values the documents write a tool's invocation policy with
	Tools         []ToolDefinition            // All tool definitions
	VisibleTools  []ToolDefinition            // Tools with a visible @mcp record: the provider tool lists
	Namespaces    []ToolsNamespace            // Tools grouped by namespace
	Timestamp     string                      // Generation timestamp
}

// ToolsNamespace groups tools by namespace.
type ToolsNamespace struct {
	Name       string           // Namespace name
	StructName string           // Namespace struct name
	Tools      []ToolDefinition // Tools in this namespace
	IsScopedNS bool             // Whether the namespace hoists a scope parameter
}

// GenerateTools generates tool-calling bindings metadata from Rust SDK and API output.
func GenerateTools(sdkOutput *SDKOutput, apiOutput *apigen.APIOutput, clock codegen.Clock) (*ToolsOutput, error) {
	if sdkOutput == nil || len(sdkOutput.Namespaces) == 0 || apiOutput == nil {
		return nil, nil
	}
	// nested-arrays guard: remove when toolsutil renders T[][].
	if err := nestedguard.Check("toolsutil", apiOutput); err != nil {
		return nil, err
	}

	output := &ToolsOutput{
		SchemaName:    sdkOutput.SchemaName,
		APIID:         sdkOutput.SchemaName,
		SDKStructName: sdkOutput.SDKStructName,
		TypesCrate:    sdkOutput.TypesCrate,
		Keys:          apiOutput.ToolKeys,
		Invocation:    apiOutput.ToolInvocation.OrDefault(),
		Tools:         []ToolDefinition{},
		VisibleTools:  []ToolDefinition{},
		Namespaces:    []ToolsNamespace{},
		Timestamp:     clock.RFC3339(),
	}

	inputTypeFields := toolsutil.BuildInputTypeFieldsMap(apiOutput)
	scalars := apiOutput.Scalars
	if scalars == nil {
		scalars = map[string]apigen.ScalarJSONSchemaInfo{}
	}

	namespaceMeta := make(map[string]NamespaceInfo, len(sdkOutput.Namespaces))
	for _, ns := range sdkOutput.Namespaces {
		namespaceMeta[ns.Name] = ns
	}

	endpointsByNamespace := make(map[string][]apigen.EndpointInfo)
	for _, endpoint := range apiOutput.Endpoints {
		nsName := endpoint.Namespace
		if strings.TrimSpace(nsName) == "" {
			nsName = "root"
		}
		endpointsByNamespace[nsName] = append(endpointsByNamespace[nsName], endpoint)
	}

	namespaceNames := make([]string, 0, len(endpointsByNamespace))
	for nsName := range endpointsByNamespace {
		namespaceNames = append(namespaceNames, nsName)
	}
	sort.Strings(namespaceNames)

	for _, nsName := range namespaceNames {
		endpoints := endpointsByNamespace[nsName]
		sort.Slice(endpoints, func(i, j int) bool {
			return endpoints[i].Name < endpoints[j].Name
		})

		nsMeta, hasMeta := namespaceMeta[nsName]
		isScopedNS := hasMeta && nsMeta.IsScopedNS
		if !isScopedNS {
			for _, endpoint := range endpoints {
				if endpoint.IsScopedEndpoint {
					isScopedNS = true
					break
				}
			}
		}

		toolsNS := ToolsNamespace{
			Name:       nsName,
			StructName: nsMeta.StructName,
			Tools:      []ToolDefinition{},
			IsScopedNS: isScopedNS,
		}

		for _, endpoint := range endpoints {
			tool := endpointToTool(endpoint, toolsNS, inputTypeFields, apiOutput.TypeUnions, scalars, apiOutput.ToolKeys)
			tool.APIID = output.APIID
			if err := toolsutil.ValidateReplayContract(tool.Parameters, tool.ReplayMode, tool.IdempotencyKeyPointers, tool.ExpectedRevisionPointers); err != nil {
				return nil, fmt.Errorf("operation %s replay contract: %w", tool.OperationID, err)
			}
			toolsNS.Tools = append(toolsNS.Tools, tool)
			output.Tools = append(output.Tools, tool)
			if tool.MCP != nil && !tool.MCP.Hidden {
				output.VisibleTools = append(output.VisibleTools, tool)
			}
		}

		output.Namespaces = append(output.Namespaces, toolsNS)
	}

	return output, nil
}

// endpointToTool converts an API endpoint to a tool definition.
func endpointToTool(
	endpoint apigen.EndpointInfo,
	ns ToolsNamespace,
	inputTypeFields map[string][]apigen.FieldArgument,
	inputTypeUnions map[string]apigen.ToolUnionInfo,
	scalars map[string]apigen.ScalarJSONSchemaInfo,
	keys apigen.ToolKeys,
) ToolDefinition {
	toolName := fmt.Sprintf("%s.%s", ns.Name, endpoint.Name)
	description := endpoint.Description
	if endpoint.Docs != nil {
		description = endpoint.Docs.Description
	}
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
			TSName: tsutil.ToCamelCase(p.Name),
			Type:   p.Type,
		}
	}

	scalarArgs := make([]ToolScalarArg, len(endpoint.ScalarArgs))
	for i, arg := range endpoint.ScalarArgs {
		scalarArgs[i] = ToolScalarArg{
			Name:     arg.Name,
			TSName:   tsutil.ToCamelCase(arg.Name),
			Type:     arg.Type,
			Required: arg.Required,
		}
	}
	queryArgs := make([]ToolQueryArg, len(endpoint.QueryParams))
	for i, arg := range endpoint.QueryParams {
		queryArgs[i] = ToolQueryArg{
			Name: arg.Name, TSName: tsutil.ToCamelCase(arg.Name), Type: arg.Type,
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
		func(name, _ string) string {
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
		OperationID:              endpoint.HandlerName,
		Title:                    endpoint.Title,
		MCP:                      toolsutil.MCPWithGuidance(endpoint.MCP, guidance, keys.Guidance),
		Guidance:                 guidance,
		ReplayMode:               toolsutil.ReplayMode(endpoint.Docs),
		IdempotencyKeyPointers:   toolsutil.IdempotencyKeyPointers(endpoint.Docs),
		ExpectedRevisionPointers: toolsutil.ExpectedRevisionPointers(endpoint.Docs),
		RequiredPerms:            append([]string(nil), endpoint.RequiredPerms...),
		MethodName:               toRustMethodName(endpoint.Name),
		HTTPMethod:               strings.ToUpper(endpoint.Method),
		HTTPPath:                 endpoint.Path,
		Description:              description,
		RequiresAuth:             endpoint.RequiresAuth,
		Namespace:                ns.Name,
		IsScopedNS:               ns.IsScopedNS || endpoint.IsScopedEndpoint,
		Parameters:               parameters,
		Returns:                  toolsutil.BuildReturnSchema(endpoint.OutputType, endpoint.OutputIsArray, scalars),
		PathParams:               pathParams,
		HasInput:                 endpoint.HasInput,
		InputType:                endpoint.InputType,
		ScalarArgs:               scalarArgs,
		QueryArgs:                queryArgs,
		Encrypted:                endpoint.Encrypted,
		BindingStatus:            bindingStatus,
		InputSchemaDigest:        fmt.Sprintf("sha256:%x", sha256.Sum256(parametersJSON)),
	}
	if docs := endpoint.Docs; docs != nil {
		tool.Capability = docs.Capability
		tool.Lifecycle = string(docs.Lifecycle)
		tool.Visibility = string(docs.Visibility)
		tool.Audience = string(docs.Audience)
	}
	return tool
}

func toolsTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"escapeJSON": toolsutil.EscapeJSON,
		"schemaJSON": toolsutil.JSONSchemaPropertyLiteral,
		"jsonValue":  toolsutil.JSONLiteral,
	}
}
