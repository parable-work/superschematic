package rustsdkgen

import (
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/toolsutil"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
)

// ToolDefinition represents a single tool for LLM function calling.
type ToolDefinition struct {
	Name         string           // Namespaced name (e.g. "auth.sendMagicLink")
	MethodName   string           // Rust SDK method name (e.g. "send_magic_link")
	HTTPMethod   string           // HTTP method e.g., "GET", "POST"
	HTTPPath     string           // HTTP path template e.g., "/api/auth/send-magic-link"
	Description  string           // Description with auth hint when required
	RequiresAuth bool             // Whether authentication is required
	Namespace    string           // Namespace for grouping (e.g. "auth")
	IsScopedNS   bool             // Whether the endpoint's namespace hoists a scope parameter
	Parameters   JSONSchemaObject // JSON Schema for parameters
	Returns      JSONSchemaReturn // JSON Schema for return type
	PathParams   []ToolPathParam  // Path parameters for invocation helpers
	HasInput     bool             // Whether endpoint has input type
	InputType    string           // Input type name when HasInput
	ScalarArgs   []ToolScalarArg  // Scalar arguments (not path params)
	Encrypted    bool             // Whether endpoint payload must be encrypted
}

type ToolPathParam = toolsutil.ToolPathParam
type ToolScalarArg = toolsutil.ToolScalarArg
type JSONSchemaObject = toolsutil.JSONSchemaObject
type JSONSchemaProperty = toolsutil.JSONSchemaProperty
type JSONSchemaReturn = toolsutil.JSONSchemaReturn

// ToolsOutput contains all generated tool definitions.
type ToolsOutput struct {
	SchemaName    string           // Schema name (e.g. "web-api")
	SDKStructName string           // SDK struct name (e.g. "WebApiSdk")
	TypesCrate    string           // Rust types crate
	Tools         []ToolDefinition // All tool definitions
	Namespaces    []ToolsNamespace // Tools grouped by namespace
	Timestamp     string           // Generation timestamp
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

	output := &ToolsOutput{
		SchemaName:    sdkOutput.SchemaName,
		SDKStructName: sdkOutput.SDKStructName,
		TypesCrate:    sdkOutput.TypesCrate,
		Tools:         []ToolDefinition{},
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
			tool := endpointToTool(endpoint, toolsNS, inputTypeFields, scalars)
			toolsNS.Tools = append(toolsNS.Tools, tool)
			output.Tools = append(output.Tools, tool)
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
	scalars map[string]apigen.ScalarJSONSchemaInfo,
) ToolDefinition {
	toolName := fmt.Sprintf("%s.%s", ns.Name, endpoint.Name)
	description := endpoint.Description
	if description == "" {
		description = fmt.Sprintf("%s endpoint", endpoint.Name)
	}
	if endpoint.RequiresAuth {
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

	parameters := buildParametersSchema(endpoint, inputTypeFields, scalars, pathParams, scalarArgs)
	returns := buildReturnSchema(endpoint, scalars)

	sdkPath := endpoint.Path
	sdkHTTPMethod := endpoint.Method

	return ToolDefinition{
		Name:         toolName,
		MethodName:   toRustMethodName(endpoint.Name),
		HTTPMethod:   strings.ToUpper(sdkHTTPMethod),
		HTTPPath:     sdkPath,
		Description:  description,
		RequiresAuth: endpoint.RequiresAuth,
		Namespace:    ns.Name,
		IsScopedNS:   ns.IsScopedNS || endpoint.IsScopedEndpoint,
		Parameters:   parameters,
		Returns:      returns,
		PathParams:   pathParams,
		HasInput:     endpoint.HasInput,
		InputType:    endpoint.InputType,
		ScalarArgs:   scalarArgs,
		Encrypted:    endpoint.Encrypted,
	}
}

// buildParametersSchema builds the JSON Schema for tool parameters.
func buildParametersSchema(
	endpoint apigen.EndpointInfo,
	inputTypeFields map[string][]apigen.FieldArgument,
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
		func(name, _ string) string {
			return tsutil.ToCamelCase(name)
		},
	)
}

// buildReturnSchema builds the JSON Schema for the return type.
func buildReturnSchema(endpoint apigen.EndpointInfo, scalars map[string]apigen.ScalarJSONSchemaInfo) JSONSchemaReturn {
	return toolsutil.BuildReturnSchema(endpoint.OutputType, endpoint.OutputIsArray, scalars)
}

func toolsTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"escapeJSON": toolsutil.EscapeJSON,
	}
}
