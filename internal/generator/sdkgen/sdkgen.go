// Package sdkgen generates TypeScript SDK code from API schema definitions.
package sdkgen

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/toolsutil"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	"github.com/parable-work/superschematic/internal/profile"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// NamespaceInfo represents a namespace with its endpoints
type NamespaceInfo struct {
	Name                   string         // Namespace name (e.g., "auth", "orders")
	ClassName              string         // TypeScript class name (e.g., "AuthNamespace")
	Endpoints              []EndpointInfo // Endpoints in this namespace
	IsScopedNS             bool           // Whether the namespace hoists a scope path parameter to its constructor
	HasAuth                bool           // Whether any endpoint requires auth
	HasEncryptedPayload    bool           // Whether namespace has encrypted POST/PUT/PATCH endpoints
	HasFilterableEndpoints bool           // Whether namespace has any @filterable endpoint
	ScopeParamName         string         // Name of the hoisted scope parameter if IsScopedNS
	Imports                []string       // Unique list of types to import from types package
}

// EndpointInfo represents a single API endpoint for the SDK
type EndpointInfo struct {
	Name           string             // Method name (e.g., "sendMagicLink")
	Path           string             // API path (e.g., "/api/auth/send-magic-link")
	Method         string             // HTTP method (GET, POST, etc.)
	InputType      string             // TypeScript input type (if any)
	OutputType     string             // TypeScript output type
	OutputIsArray  bool               // Whether output is an array
	HasInput       bool               // Whether endpoint has input
	InputRequired  bool               // Whether the input argument is non-null (!) in GraphQL
	RequiresAuth   bool               // Whether endpoint requires authentication
	PathParams     []PathParam        // Path parameters
	QueryParams    []QueryParam       // Query string parameters (from @query directive)
	ScalarArgs     []apigen.ScalarArg // Scalar arguments (not input types)
	Description    string             // Endpoint description
	TSPath         string             // TypeScript template literal path
	ResourceTSPath string             // resource route path as TypeScript template literal
	Encrypted      bool               // Whether endpoint payload must be encrypted
	Filterable     bool               // Whether endpoint accepts bracket-notation filter query params

	// File upload configuration
	HasFileUpload    bool              // True if any input field is a file-type scalar
	FileUploadFields []FileUploadField // File upload field metadata for multipart handling

	// JSON parse configuration
	HasOutputParser bool // True if the output type has a parseFromJSON function
}

// FileUploadField represents a file upload field in an endpoint's input for TypeScript SDK
type FileUploadField struct {
	Name       string // Field name from the input type (camelCase)
	TSName     string // TypeScript property name (camelCase)
	ScalarType string // The file scalar type name (e.g., "Image", "PDF")
	Required   bool   // Whether this file field is required
}

// PathParam represents a path parameter
type PathParam struct {
	Name   string // Parameter name (e.g., "orderId")
	TSName string // TypeScript variable name
	TSType string // TypeScript type
	IRType string // Original IR type name (for JSON Schema conversion)
}

// QueryParam represents a query string parameter (from @query directive)
type QueryParam struct {
	Name     string // Parameter name (e.g., "limit")
	TSName   string // TypeScript variable name (e.g., "limit")
	TSType   string // TypeScript type (e.g., "number")
	IRType   string // Original IR type name (e.g., "number")
	Required bool   // Whether parameter is required

	ValidateMin       *float64 // Minimum numeric value allowed
	ValidateMax       *float64 // Maximum numeric value allowed
	ValidateMinLength *int     // Minimum character length
	ValidateMaxLength *int     // Maximum character length
	ValidateListMin   *int     // Minimum list cardinality
	ValidateListMax   *int     // Maximum list cardinality
	ValidatePattern   string   // Regex pattern validation
}

// SDKOutput contains all generated SDK code metadata
type SDKOutput struct {
	SchemaName             string          // Schema name (e.g., "my-api")
	Author                 string          // package.json author, from Naming.PackageAuthor
	SDKClassName           string          // Main SDK class name (e.g., "WebApiSDK")
	PackageName            string          // NPM package name (e.g., "@schemas/my-api-sdk")
	TypesPackage           string          // Types package name (e.g., "@schemas/my-api-types")
	Namespaces             []NamespaceInfo // All namespaces
	HasAuth                bool            // Whether any endpoint requires auth
	HasFilterableEndpoints bool            // Whether any endpoint is @filterable (gates FilterParam in types.ts)
	Timestamp              string          // Generation timestamp
	Version                string          // Package version
}

// Generate generates TypeScript SDK from API output.
// parseableTypes is the set of type names that have parseFromJSON functions
// generated by tsgen. When an endpoint's output type is in this set, the SDK
// method will call parseFromJSON on the response to transform wire-format
// values (e.g. date strings) into proper runtime types (e.g. Date objects).
func Generate(apiOutput *apigen.APIOutput, parseableTypes map[string]bool, clock codegen.Clock) (*SDKOutput, error) {
	if apiOutput == nil || len(apiOutput.Endpoints) == 0 {
		return nil, nil
	}

	// Check if this is public api (has authentication)
	hasAuth := apiOutput.IsPublic && apiOutput.HasAuth

	output := &SDKOutput{
		SchemaName:   apiOutput.SchemaName,
		SDKClassName: toSDKClassName(apiOutput.SchemaName),
		PackageName:  apiOutput.Naming.OrDefault().NpmSDKPackage(apiOutput.SchemaName),
		TypesPackage: apiOutput.Naming.OrDefault().NpmTypesPackage(apiOutput.SchemaName),
		Author:       apiOutput.Naming.OrDefault().PackageAuthor,
		Namespaces:   []NamespaceInfo{},
		HasAuth:      hasAuth,
		Timestamp:    clock.RFC3339(),
		Version:      "1.0.0",
	}

	// Group endpoints by namespace
	namespaceMap := make(map[string]*NamespaceInfo)
	importsMap := make(map[string]map[string]bool) // Track unique imports per namespace

	for _, endpoint := range apiOutput.Endpoints {
		if endpoint.IsWebhook {
			continue
		}
		ns := endpoint.Namespace
		if ns == "" {
			ns = "root"
		}

		// Initialize namespace if not exists
		if _, exists := namespaceMap[ns]; !exists {
			namespaceMap[ns] = &NamespaceInfo{
				Name:                ns,
				ClassName:           tsutil.ToClassName(ns) + "Namespace",
				Endpoints:           []EndpointInfo{},
				IsScopedNS:          endpoint.IsScopedEndpoint,
				HasAuth:             false,
				HasEncryptedPayload: false,
				Imports:             []string{},
			}
			importsMap[ns] = make(map[string]bool)
			if endpoint.IsScopedEndpoint {
				namespaceMap[ns].ScopeParamName = endpoint.ScopeParamName
			}
		}

		// Convert endpoint to SDK format
		sdkEndpoint := convertEndpoint(endpoint, parseableTypes)
		namespaceMap[ns].Endpoints = append(namespaceMap[ns].Endpoints, sdkEndpoint)

		// Track unique imports for this namespace
		if endpoint.InputType != "" {
			inputType := normalizeTSTypeIdentifier(endpoint.InputType)
			importsMap[ns][inputType] = true
			// Import validation function for input type
			importsMap[ns]["validate"+inputType] = true
			// Import ValidationResult type for validation return value
			importsMap[ns]["ValidationResult"] = true
		}
		if endpoint.OutputType != "" && !codegen.IsLanguagePrimitive(endpoint.OutputType) {
			// Bare language primitives are built-in TypeScript types; only
			// named types are imported from the types package.
			outputSymbol := normalizeTSTypeIdentifier(endpoint.OutputType)
			importsMap[ns][outputSymbol] = true
			if parseableTypes[endpoint.OutputType] {
				importsMap[ns]["parse"+outputSymbol+"FromJSON"] = true
			}
		}

		// Import scalar types and validation functions for scalar arguments
		if len(endpoint.ScalarArgs) > 0 {
			// Import newValidationErrors and setFieldErrors for scalar args validation
			importsMap[ns]["newValidationErrors"] = true
			importsMap[ns]["setFieldErrors"] = true
			for _, arg := range endpoint.ScalarArgs {
				// Bare language primitives have no generated type alias or
				// validators in the types package; only named scalars import.
				if codegen.IsLanguagePrimitive(arg.Type) {
					continue
				}
				scalarSymbol := normalizeTSTypeIdentifier(arg.Type)
				// Import the scalar type itself (e.g., Email, JWT)
				importsMap[ns][scalarSymbol] = true
				// Import validation functions for the scalar.
				// Array args always need the Required variant because each element
				// is validated with validateXRequired regardless of whether the
				// array itself is required or optional.
				if arg.IsArray || arg.Required {
					importsMap[ns]["validate"+scalarSymbol+"Required"] = true
				} else {
					importsMap[ns]["validate"+scalarSymbol] = true
				}
			}
		}

		// Import enum types used in query parameters
		for _, param := range endpoint.QueryParams {
			if !codegen.IsLanguagePrimitive(param.Type) {
				tsType := IRTypeToTSType(param.Type)
				importsMap[ns][tsType] = true
			}
			if queryParamHasValidation(param) {
				importsMap[ns]["newValidationErrors"] = true
				importsMap[ns]["setFieldErrors"] = true
			}
		}

		// Track if any endpoint in namespace requires auth
		if endpoint.RequiresAuth {
			namespaceMap[ns].HasAuth = true
		}
		if endpoint.Encrypted {
			switch strings.ToUpper(endpoint.Method) {
			case "POST", "PUT", "PATCH":
				namespaceMap[ns].HasEncryptedPayload = true
			}
		}
		if endpoint.Filterable {
			namespaceMap[ns].HasFilterableEndpoints = true
			output.HasFilterableEndpoints = true
		}
	}

	// Build sorted import lists for each namespace
	for ns, imports := range importsMap {
		importList := make([]string, 0, len(imports))
		for imp := range imports {
			importList = append(importList, imp)
		}
		sort.Strings(importList)
		namespaceMap[ns].Imports = importList
	}

	// Convert map to sorted slice
	namespaces := make([]NamespaceInfo, 0, len(namespaceMap))
	for _, ns := range namespaceMap {
		namespaces = append(namespaces, *ns)
	}

	// Sort namespaces by name for consistent output
	sort.Slice(namespaces, func(i, j int) bool {
		return namespaces[i].Name < namespaces[j].Name
	})

	output.Namespaces = namespaces

	return output, nil
}

// convertEndpoint converts apigen endpoint to SDK endpoint.
func convertEndpoint(ep apigen.EndpointInfo, parseableTypes map[string]bool) EndpointInfo {
	tsPath := convertPathToTS(ep.Path, ep.PathParams)

	tsPathParams := make([]PathParam, len(ep.PathParams))
	for i, param := range ep.PathParams {
		tsPathParams[i] = PathParam{
			Name:   param.Name,
			TSName: tsutil.ToCamelCase(param.Name),
			TSType: tsutil.IRTypeToTSType(param.Type),
			IRType: param.Type,
		}
	}

	fileUploadFields := make([]FileUploadField, len(ep.FileUploadFields))
	for i, field := range ep.FileUploadFields {
		fileUploadFields[i] = FileUploadField{
			Name:       field.Name,
			TSName:     tsutil.ToCamelCase(field.Name),
			ScalarType: field.ScalarType,
			Required:   field.Required,
		}
	}

	tsQueryParams := make([]QueryParam, len(ep.QueryParams))
	for i, param := range ep.QueryParams {
		tsQueryParams[i] = QueryParam{
			Name:     param.Name,
			TSName:   tsutil.ToCamelCase(param.Name),
			TSType:   IRTypeToTSType(param.Type),
			IRType:   param.Type,
			Required: param.Required,

			ValidateMin:       param.ValidateMin,
			ValidateMax:       param.ValidateMax,
			ValidateMinLength: param.ValidateMinLength,
			ValidateMaxLength: param.ValidateMaxLength,
			ValidateListMin:   param.ValidateListMin,
			ValidateListMax:   param.ValidateListMax,
			ValidatePattern:   param.ValidatePattern,
		}
	}

	httpMethod := ep.Method

	hasOutputParser := ep.OutputType != "" &&
		!codegen.IsLanguagePrimitive(ep.OutputType) &&
		parseableTypes[ep.OutputType]

	return EndpointInfo{
		Name:             tsutil.ToCamelCase(ep.Name),
		Path:             ep.Path,
		Method:           strings.ToUpper(httpMethod),
		InputType:        ep.InputType,
		OutputType:       ep.OutputType,
		OutputIsArray:    ep.OutputIsArray,
		HasInput:         ep.HasInput,
		InputRequired:    ep.InputRequired,
		RequiresAuth:     ep.RequiresAuth,
		PathParams:       tsPathParams,
		QueryParams:      tsQueryParams,
		ScalarArgs:       ep.ScalarArgs,
		Description:      ep.Description,
		TSPath:           tsPath,
		Encrypted:        ep.Encrypted,
		Filterable:       ep.Filterable,
		HasFileUpload:    ep.HasFileUpload,
		FileUploadFields: fileUploadFields,
		HasOutputParser:  hasOutputParser,
	}
}

// IRTypeToTSType converts an IR type-reference name to a TypeScript type for
// query parameters. The IR language primitives are spelled exactly like the
// TypeScript primitives, so they pass through.
func IRTypeToTSType(irType string) string {
	if codegen.IsLanguagePrimitive(irType) {
		return irType
	}
	// For custom scalars and enums, return an identifier-safe symbol.
	return normalizeTSTypeIdentifier(irType)
}

// convertPathToTS converts path with parameters to TypeScript template literal.
func convertPathToTS(path string, pathParams []apigen.Param) string {
	tsPath := path
	for _, param := range pathParams {
		placeholder := fmt.Sprintf("{%s}", param.Name)
		replacement := fmt.Sprintf("${%s}", tsutil.ToCamelCase(param.Name))
		tsPath = strings.ReplaceAll(tsPath, placeholder, replacement)
	}
	return tsPath
}

// toSDKClassName converts schema name to SDK class name
// Example: "my-api" -> "MyApiSDK"
func toSDKClassName(schemaName string) string {
	return tsutil.ToClassName(schemaName) + "SDK"
}

// WriteSDKWithTools writes the generated SDK and tool calling bindings to files.
func WriteSDKWithTools(output *SDKOutput, apiOutput *apigen.APIOutput, outputDir string, clock codegen.Clock) error {
	return WriteSDKWithToolsProfiled(output, apiOutput, outputDir, clock, nil, false)
}

// WriteSDKWithToolsProfiled writes the generated SDK and tool calling bindings with shared codegen profiling.
func WriteSDKWithToolsProfiled(output *SDKOutput, apiOutput *apigen.APIOutput, outputDir string, clock codegen.Clock, prof *profile.Profiler, skipFormat bool, phasePrefixes ...string) error {
	// Create output directory structure
	dirs := []string{
		outputDir,
		filepath.Join(outputDir, "namespaces"),
		filepath.Join(outputDir, "tools"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Get custom template functions for this generator
	customFuncs := customTemplateFuncs()
	renderer := codegen.NewFileGenerator(
		templatesFS,
		codegen.MergeTemplateFuncs(tsutil.BaseTemplateFuncs(), customFuncs),
		codegen.WithProfiler(prof, phasePrefixes...),
		codegen.WithSkipFormat(skipFormat),
	)

	// Generate package.json
	if err := generateFile(renderer, "package.tmpl", filepath.Join(outputDir, "package.json"), output, customFuncs); err != nil {
		return fmt.Errorf("failed to generate package.json: %w", err)
	}

	// Generate tsconfig.json
	if err := generateFile(renderer, "tsconfig.tmpl", filepath.Join(outputDir, "tsconfig.json"), output, customFuncs); err != nil {
		return fmt.Errorf("failed to generate tsconfig.json: %w", err)
	}

	// Generate types.ts (SDK types and errors)
	if err := generateFile(renderer, "types.tmpl", filepath.Join(outputDir, "types.ts"), output, customFuncs); err != nil {
		return fmt.Errorf("failed to generate types.ts: %w", err)
	}

	// Generate client.ts (HTTP client with auth)
	if err := generateFile(renderer, "client.tmpl", filepath.Join(outputDir, "client.ts"), output, customFuncs); err != nil {
		return fmt.Errorf("failed to generate client.ts: %w", err)
	}

	// Generate namespace files
	for _, ns := range output.Namespaces {
		namespaceOutput := struct {
			SDK       *SDKOutput
			Namespace NamespaceInfo
		}{
			SDK:       output,
			Namespace: ns,
		}

		filename := fmt.Sprintf("%s.ts", strings.ToLower(ns.Name))
		namespacePath := filepath.Join(outputDir, "namespaces", filename)

		if err := generateFile(renderer, "namespace.tmpl", namespacePath, namespaceOutput, customFuncs); err != nil {
			return fmt.Errorf("failed to generate namespace %s: %w", ns.Name, err)
		}
	}

	// Generate main SDK file (index.ts)
	if err := generateFile(renderer, "sdk.tmpl", filepath.Join(outputDir, "index.ts"), output, customFuncs); err != nil {
		return fmt.Errorf("failed to generate index.ts: %w", err)
	}

	// Generate README.md
	if err := generateFile(renderer, "readme.tmpl", filepath.Join(outputDir, "README.md"), output, customFuncs); err != nil {
		return fmt.Errorf("failed to generate README.md: %w", err)
	}

	// Generate tool calling bindings if apiOutput is provided
	if apiOutput != nil {
		toolsOutput, err := GenerateTools(output, apiOutput, clock)
		if err != nil {
			return fmt.Errorf("failed to generate tools: %w", err)
		}

		if toolsOutput != nil && len(toolsOutput.Tools) > 0 {
			toolsFuncs := toolsTemplateFuncs()
			toolsRenderer := codegen.NewFileGenerator(
				templatesFS,
				codegen.MergeTemplateFuncs(tsutil.BaseTemplateFuncs(), toolsFuncs),
				codegen.WithProfiler(prof, suffixProfilePrefixes(phasePrefixes, "tools")...),
				codegen.WithSkipFormat(skipFormat),
			)

			// Generate TypeScript tools module
			if err := generateFile(toolsRenderer, "tools-index.tmpl", filepath.Join(outputDir, "tools", "index.ts"), toolsOutput, toolsFuncs); err != nil {
				return fmt.Errorf("failed to generate tools/index.ts: %w", err)
			}

			// Generate OpenAI format JSON
			if err := generateFile(toolsRenderer, "tools-openai.tmpl", filepath.Join(outputDir, "tools", "openai.json"), toolsOutput, toolsFuncs); err != nil {
				return fmt.Errorf("failed to generate tools/openai.json: %w", err)
			}

			// Generate Anthropic format JSON
			if err := generateFile(toolsRenderer, "tools-anthropic.tmpl", filepath.Join(outputDir, "tools", "anthropic.json"), toolsOutput, toolsFuncs); err != nil {
				return fmt.Errorf("failed to generate tools/anthropic.json: %w", err)
			}

			// Generate generic JSON Schema format
			if err := generateFile(toolsRenderer, "tools-schema.tmpl", filepath.Join(outputDir, "tools", "schema.json"), toolsOutput, toolsFuncs); err != nil {
				return fmt.Errorf("failed to generate tools/schema.json: %w", err)
			}
			if err := generateFile(toolsRenderer, "tools-mcp-binding.tmpl", filepath.Join(outputDir, "tools", "mcp-binding.json"), toolsOutput, toolsFuncs); err != nil {
				return fmt.Errorf("failed to generate tools/mcp-binding.json: %w", err)
			}
		}
	}

	// Write a minimal prettier config for generated TypeScript.
	if err := tsutil.CopyPrettierConfig("", outputDir); err != nil {
		return err
	}

	return nil
}

func suffixProfilePrefixes(phasePrefixes []string, suffix string) []string {
	if len(phasePrefixes) == 0 {
		return []string{"codegen." + suffix}
	}
	suffixed := make([]string, 0, len(phasePrefixes))
	for _, prefix := range phasePrefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			prefix = "codegen"
		}
		suffixed = append(suffixed, prefix+"."+suffix)
	}
	return suffixed
}

// customTemplateFuncs returns custom template functions specific to the SDK generator
// These are merged with the base functions from tsutil
func customTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"join_params": func(params []PathParam) string {
			names := make([]string, len(params))
			for i, p := range params {
				names[i] = fmt.Sprintf("%s: %s", p.TSName, p.TSType)
			}
			return strings.Join(names, ", ")
		},
		"join_params_filtered": func(params []PathParam, isScopedNS bool, scopeParamName string) string {
			var names []string
			for _, p := range params {
				// Skip the scope parameter for scoped namespaces since the constructor holds it
				if isScopedNS && p.TSName == scopeParamName {
					continue
				}
				names = append(names, fmt.Sprintf("%s: %s", p.TSName, p.TSType))
			}
			return strings.Join(names, ", ")
		},
		"has_non_scope_params": func(params []PathParam, isScopedNS bool, scopeParamName string) bool {
			for _, p := range params {
				if isScopedNS && p.TSName == scopeParamName {
					continue
				}
				return true
			}
			return false
		},
		"scoped_path": func(path string, isScopedNS bool, scopeParamName string) string {
			// For scoped namespaces, replace ${<scope>} with ${this.<scope>}
			if isScopedNS {
				placeholder := fmt.Sprintf("${%s}", scopeParamName)
				replacement := fmt.Sprintf("${this.%s}", scopeParamName)
				return strings.ReplaceAll(path, placeholder, replacement)
			}
			return path
		},
		"tsType": func(gqlType string) string {
			return IRTypeToTSType(gqlType)
		},
		"type_imports": func(imports []string) []string {
			var typeImports []string
			for _, imp := range imports {
				if isSDKValueImport(imp) {
					continue
				}
				typeImports = append(typeImports, imp)
			}
			return typeImports
		},
		"value_imports": func(imports []string) []string {
			var valueImports []string
			for _, imp := range imports {
				if isSDKValueImport(imp) {
					valueImports = append(valueImports, imp)
				}
			}
			return valueImports
		},
		"type_ref":                normalizeTSTypeIdentifier,
		"scalar_validator_symbol": normalizeTSTypeIdentifier,
		"scalar_needs_validation": func(irType string) bool {
			// Bare language primitives have no generated validators; only
			// named scalars carry scalar-lib validation.
			return !codegen.IsLanguagePrimitive(irType)
		},
		"file_upload_field_union": func(fields []FileUploadField) string {
			// Generate a TypeScript string union for file upload field names
			// Used for Omit<InputType, 'field1' | 'field2'>
			if len(fields) == 0 {
				return "never"
			}
			names := make([]string, len(fields))
			for i, f := range fields {
				names[i] = fmt.Sprintf("'%s'", f.Name)
			}
			return strings.Join(names, " | ")
		},
		"has_query_params": func(params []QueryParam) bool {
			return len(params) > 0
		},
		"has_query_param_validation": func(params []QueryParam) bool {
			for _, param := range params {
				if queryParamDefHasValidation(param) {
					return true
				}
			}
			return false
		},
		"escape_backslash": func(s string) string {
			s = strings.ReplaceAll(s, "\\", "\\\\")
			s = strings.ReplaceAll(s, "'", "\\'")
			return s
		},
		"has_encrypted_body": func(method string, encrypted bool) bool {
			if !encrypted {
				return false
			}
			switch method {
			case "POST", "PUT", "PATCH":
				return true
			default:
				return false
			}
		},
		"query_params_type": func(params []QueryParam) string {
			// Generate TypeScript type for query params object
			// Example: { limit?: number; offset?: number }
			if len(params) == 0 {
				return ""
			}
			var fields []string
			for _, p := range params {
				optional := "?"
				if p.Required {
					optional = ""
				}
				fields = append(fields, fmt.Sprintf("%s%s: %s", p.TSName, optional, p.TSType))
			}
			return "{ " + strings.Join(fields, "; ") + " }"
		},
		"multipart_method": func(method string) string {
			switch method {
			case "PUT":
				return "putMultipart"
			case "PATCH":
				return "patchMultipart"
			default:
				return "postMultipart"
			}
		},
	}
}

func isSDKValueImport(name string) bool {
	return strings.HasPrefix(name, "parse") ||
		strings.HasPrefix(name, "validate") ||
		name == "newValidationErrors" ||
		name == "setFieldErrors"
}

// toolsTemplateFuncs returns template functions specific to the tools generator
func toolsTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"escapeJSON": toolsutil.EscapeJSON,
		"sortedKeys": func(m map[string]JSONSchemaProperty) []string {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return keys
		},
		"escapeBacktick": func(s string) string {
			// Escape backticks for TypeScript template literals
			return strings.ReplaceAll(s, "`", "\\`")
		},
		"escapeBackslash": func(s string) string {
			// Double-escape backslashes for TypeScript string literals containing regex
			s = strings.ReplaceAll(s, "\\", "\\\\")
			// Escape single quotes for single-quoted string literals
			s = strings.ReplaceAll(s, "'", "\\'")
			return s
		},
		"formatToolName": formatToolNameForTS,
		"toPascalCase":   toPascalCaseToolName,
		"contains": func(slice []string, item string) bool {
			for _, s := range slice {
				if s == item {
					return true
				}
			}
			return false
		},
		"jsonSchemaToTSType": func(prop JSONSchemaProperty) string {
			// Handle enum types - generate a union of string literals
			if len(prop.Enum) > 0 {
				var enumValues []string
				for _, v := range prop.Enum {
					enumValues = append(enumValues, fmt.Sprintf("'%s'", v))
				}
				return strings.Join(enumValues, " | ")
			}

			switch prop.Type {
			case "string":
				return "string"
			case "integer", "number":
				return "number"
			case "boolean":
				return "boolean"
			case "array":
				if prop.Items != nil {
					itemType := jsonSchemaTypeToTS(prop.Items.Type)
					return itemType + "[]"
				}
				return "unknown[]"
			case "object":
				if len(prop.Properties) > 0 {
					fields := make([]string, 0, len(prop.Properties))
					for name, nested := range prop.Properties {
						nestedType := jsonSchemaTypeToTS(nested.Type)
						if len(nested.Enum) > 0 {
							enumValues := make([]string, 0, len(nested.Enum))
							for _, v := range nested.Enum {
								enumValues = append(enumValues, fmt.Sprintf("'%s'", v))
							}
							nestedType = strings.Join(enumValues, " | ")
						}

						optional := "?"
						for _, requiredField := range prop.Required {
							if requiredField == name {
								optional = ""
								break
							}
						}
						fields = append(fields, fmt.Sprintf("%s%s: %s", name, optional, nestedType))
					}
					sort.Strings(fields)
					return "{ " + strings.Join(fields, "; ") + " }"
				}
				return "Record<string, unknown>"
			default:
				return "unknown"
			}
		},
		"buildInvocationArgs": func(tool ToolDefinition, ns ToolsNamespace) string {
			var args []string

			// A scoped namespace takes its scope parameter in the factory,
			// so the invocation skips it.
			for _, param := range tool.PathParams {
				if ns.IsScopedNS && param.Name == ns.ScopeParam {
					continue
				}
				args = append(args, fmt.Sprintf("(params as %sParams).%s", toPascalCaseToolName(tool.Name), param.TSName))
			}

			// Handle input type or scalar args
			if tool.HasInput {
				// Pass the entire params object minus path params
				args = append(args, fmt.Sprintf("params as %sParams", toPascalCaseToolName(tool.Name)))
			} else if len(tool.ScalarArgs) > 0 {
				// Build object with scalar args
				args = append(args, fmt.Sprintf("params as %sParams", toPascalCaseToolName(tool.Name)))
			}
			if tool.Encrypted {
				args = append(
					args,
					fmt.Sprintf(
						"(params as %sParams).publicEncryptionKey ? { publicEncryptionKey: (params as %sParams).publicEncryptionKey as { publicKey: string; algorithm: string; keyId: string } } : undefined",
						toPascalCaseToolName(tool.Name),
						toPascalCaseToolName(tool.Name),
					),
				)
			}

			return strings.Join(args, ", ")
		},
	}
}

// jsonSchemaTypeToTS converts JSON Schema type to TypeScript type
func jsonSchemaTypeToTS(schemaType string) string {
	switch schemaType {
	case "string":
		return "string"
	case "integer", "number":
		return "number"
	case "boolean":
		return "boolean"
	case "object":
		return "Record<string, unknown>"
	case "array":
		return "unknown[]"
	default:
		return "unknown"
	}
}

// toPascalCaseToolName converts namespaced tool name to PascalCase.
// Splits on "." (namespace separator), "-", and "_" so that names like
// "connector-requests.completeConnectorResearch" become
// "ConnectorRequestsCompleteConnectorResearch".
func toPascalCaseToolName(name string) string {
	parts := strings.Split(name, ".")
	var result strings.Builder
	for _, part := range parts {
		segments := strings.FieldsFunc(part, func(r rune) bool {
			return r == '-' || r == '_'
		})
		for _, seg := range segments {
			if len(seg) > 0 {
				result.WriteString(strings.ToUpper(seg[:1]))
				result.WriteString(seg[1:])
			}
		}
	}
	return result.String()
}

func queryParamHasValidation(param apigen.Param) bool {
	return param.ValidateMin != nil ||
		param.ValidateMax != nil ||
		param.ValidateMinLength != nil ||
		param.ValidateMaxLength != nil ||
		param.ValidateListMin != nil ||
		param.ValidateListMax != nil ||
		param.ValidatePattern != ""
}

func queryParamDefHasValidation(param QueryParam) bool {
	return param.ValidateMin != nil ||
		param.ValidateMax != nil ||
		param.ValidateMinLength != nil ||
		param.ValidateMaxLength != nil ||
		param.ValidateListMin != nil ||
		param.ValidateListMax != nil ||
		param.ValidatePattern != ""
}

func normalizeTSTypeIdentifier(typeName string) string {
	trimmed := strings.TrimSpace(typeName)
	if trimmed == "" || !strings.Contains(trimmed, ".") {
		return trimmed
	}

	tokens := codegen.BuildScalarTokens(trimmed)
	if symbol := strings.TrimSpace(tokens.Symbol); symbol != "" {
		return symbol
	}

	return strings.ReplaceAll(trimmed, ".", "")
}

func generateFile(generator *codegen.FileGenerator, templateName, outputPath string, data interface{}, customFuncs template.FuncMap) (err error) {
	return generator.GenerateFile(
		codegen.NewFileConfig(
			templatesFS,
			templateName,
			outputPath,
			data,
			codegen.MergeTemplateFuncs(tsutil.BaseTemplateFuncs(), customFuncs),
		),
	)
}

// FormatSDK runs prettier on the generated TypeScript files
func FormatSDK(outputDir string) error {
	return tsutil.FormatTypeScript(outputDir)
}

// CompileSDK runs tsc to compile the generated TypeScript SDK.
// typesDir is the filesystem path to the generated TypeScript types package; if non-empty the
// types package is symlinked into node_modules AFTER bun install (before tsc) so that the
// peerDependency is resolvable even in Docker where node_modules is excluded from the build context.
func CompileSDK(outputDir, typesDir string) error {
	if _, err := exec.LookPath("bun"); err != nil {
		return fmt.Errorf("bun not found: %w", err)
	}

	// Install dependencies first. This creates/refreshes node_modules and would remove any
	// pre-existing symlinks, so the types link must be created afterwards.
	installCmd := exec.Command("bun", "install")
	installCmd.Dir = outputDir
	if installOut, err := installCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bun install failed: %w\nOutput: %s", err, string(installOut))
	}

	// Link the types package AFTER bun install so tsc can resolve the peerDependency.
	if typesDir != "" {
		if err := linkTypesPackage(typesDir, outputDir); err != nil {
			// Non-fatal: compilation will likely fail but we surface the real tsc error.
			fmt.Printf("  ⚠ Warning: failed to link types package: %v\n", err)
		}
	}

	buildCmd := exec.Command("bun", "run", "build")
	buildCmd.Dir = outputDir
	if buildOut, err := buildCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("TypeScript compilation failed: %w\nOutput: %s", err, string(buildOut))
	}

	return nil
}

func linkTypesPackage(typesDir, sdkOutputDir string) error {
	packageJSONPath := filepath.Join(typesDir, "package.json")
	packageJSON, err := os.ReadFile(packageJSONPath)
	if err != nil {
		return fmt.Errorf("read types package manifest %q: %w", packageJSONPath, err)
	}

	var manifest struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(packageJSON, &manifest); err != nil {
		return fmt.Errorf("parse types package manifest %q: %w", packageJSONPath, err)
	}

	packageName := strings.TrimSpace(manifest.Name)
	if packageName == "" {
		return fmt.Errorf("types package manifest %q is missing name", packageJSONPath)
	}

	packagePathParts := strings.Split(packageName, "/")
	linkPath := filepath.Join(append([]string{sdkOutputDir, "node_modules"}, packagePathParts...)...)
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return fmt.Errorf("create package scope directory %q: %w", filepath.Dir(linkPath), err)
	}

	if _, err := os.Lstat(linkPath); err == nil {
		if err := os.RemoveAll(linkPath); err != nil {
			return fmt.Errorf("remove existing package path %q: %w", linkPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect existing package path %q: %w", linkPath, err)
	}

	if err := os.Symlink(typesDir, linkPath); err != nil {
		return fmt.Errorf("symlink %q -> %q: %w", linkPath, typesDir, err)
	}

	return nil
}
