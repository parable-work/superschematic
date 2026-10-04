// Package rustsdkgen generates Rust SDK code from API schema definitions.
package rustsdkgen

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"unicode"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/rustapigen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// SDKOutput contains all generated Rust SDK metadata.
type SDKOutput struct {
	SchemaName             string
	CrateName              string
	TypesCrate             string
	TypesCrateModule       string
	TypesDependencyPath    string
	SDKStructName          string
	Namespaces             []NamespaceInfo
	HasInputSchemas        bool
	InputSchemasJSON       string
	HasAuth                bool
	HasFilterableEndpoints bool
	// ChecksQueryListItems reports whether a list query parameter's items
	// are sent as text; src/runtime.rs then carries check_query_list_item.
	ChecksQueryListItems bool
	// HasPathParams reports whether an endpoint's path takes a value;
	// src/runtime.rs then carries path_segment, which encodes it.
	HasPathParams bool
	Timestamp     string
	Version       string
	// ValidatesMapValues reports whether a map argument's values are
	// validated (ScalarArg.ValueSchema); src/runtime.rs then carries
	// validate_map_values.
	ValidatesMapValues bool
}

// NamespaceInfo represents a namespace with its endpoints.
type NamespaceInfo struct {
	Name           string
	ModuleName     string
	StructName     string
	FieldName      string
	IsScopedNS     bool
	ScopeParamName string
	ScopeFieldName string
	Endpoints      []EndpointInfo
}

// EndpointInfo represents a single API endpoint for the Rust SDK.
type EndpointInfo struct {
	MethodName             string
	HTTPMethod             string
	PathFormat             string
	PathArgs               []string
	PathQueryBindings      []PathQueryBinding
	InputType              string
	InputSchemaName        string
	OutputType             string
	OutputRustType         string
	HasInput               bool
	RequiresAuth           bool
	PathParams             []PathParam
	QueryParams            []QueryParam
	ScalarArgs             []ScalarArg
	HasScalarArgs          bool
	ScalarInputStructName  string
	QueryStructName        string
	HasQueryParams         bool
	HasRequiredQueryParams bool
	Description            string
	Encrypted              bool
	HasEncryptedBody       bool
	HasFileUpload          bool
	FileUploadFields       []FileUploadField
	FilesStructName        string
	Filterable             bool
	// SupportsListAll marks limit/offset-paginated list endpoints that emit
	// an auto-paginating `{method}_all` variant. Requires a schema-declared
	// max on the `limit` query param so the generated page size matches the
	// server's clamp (a mismatched hand-picked constant silently truncates).
	SupportsListAll bool
	ListAllPageSize int
	// ValidatesMapValues reports whether a map argument's values are
	// validated (ScalarArg.ValueSchema), every failure reported at once.
	ValidatesMapValues bool
}

// PathParam represents a path parameter.
type PathParam struct {
	Name     string
	RustName string
	RustType string
}

// PathQueryBinding represents a path placeholder whose value comes from the
// query struct (a QueryParam<> operation argument embedded in the @rest path).
type PathQueryBinding struct {
	Name     string
	RustName string
	Required bool
}

// QueryParam represents a query string parameter.
type QueryParam struct {
	Name     string
	RustName string
	RustType string
	Required bool
	// IsArray marks a list parameter (QueryParam<T[]>), sent as one
	// comma-separated value (?name=a,b). An empty list is left out, the
	// route refusing a present empty value, and a required one is refused.
	IsArray bool
	// ItemIsText marks a list whose items are not numbers or booleans, so
	// one may be empty or hold a comma or surrounding space, which the
	// comma-separated value cannot carry.
	ItemIsText        bool
	ValidateMin       *float64
	ValidateMax       *float64
	ValidateMinLength *int
	ValidateMaxLength *int
	ValidateListMin   *int
	ValidateListMax   *int
	ValidatePattern   string
}

// ChecksItems reports whether the SDK checks each item of a list parameter
// before the request: that the comma-separated value carries a text item
// as it is, and the argument's rules, which the route checks per item.
func (p QueryParam) ChecksItems() bool {
	return p.IsArray && (p.ItemIsText ||
		p.ValidateMin != nil ||
		p.ValidateMax != nil ||
		p.ValidateMinLength != nil ||
		p.ValidateMaxLength != nil ||
		p.ValidatePattern != "")
}

// ChecksListMin reports whether listMin bounds a value the SDK sends. A
// list is sent only with at least one item, so a minimum of one bounds
// nothing.
func (p QueryParam) ChecksListMin() bool {
	if p.ValidateListMin == nil {
		return false
	}
	return !p.IsArray || *p.ValidateListMin > 1
}

// ScalarArg represents a scalar input argument for an endpoint.
type ScalarArg struct {
	Name              string
	RustName          string
	RustType          string
	Required          bool
	IsArray           bool
	ValidateMin       *float64
	ValidateMax       *float64
	ValidateMinLength *int
	ValidateMaxLength *int
	ValidateListMin   *int
	ValidateListMax   *int
	ValidatePattern   string
	// IsMap marks a map argument (Record<string, T>; with IsArray,
	// Record<string, T[]>), a HashMap of its values as the route takes it,
	// sent as a JSON object. ValueSchema names the schema each value is
	// validated against, at name[key] and, for a list value, each element at
	// name[key][i]: the route's own, the map's additionalProperties in the
	// API's OpenAPI document. It is "" when the document has none.
	IsMap       bool
	ValueSchema string
}

// NeedsRegex reports whether this namespace emits Regex-based validation.
func (ns NamespaceInfo) NeedsRegex() bool {
	for _, endpoint := range ns.Endpoints {
		if endpoint.NeedsRegex() {
			return true
		}
	}
	return false
}

// NeedsRegex reports whether this endpoint emits Regex-based validation.
func (endpoint EndpointInfo) NeedsRegex() bool {
	for _, param := range endpoint.QueryParams {
		if param.ValidatePattern != "" {
			return true
		}
	}
	for _, arg := range endpoint.ScalarArgs {
		if arg.NeedsRegex() {
			return true
		}
	}
	return false
}

// NeedsRegex reports whether this scalar arg emits Regex-based validation.
// A map's values are validated against their schema in src/runtime.rs.
func (arg ScalarArg) NeedsRegex() bool {
	return !arg.IsArray && !arg.IsMap && arg.ValidatePattern != ""
}

// ListMinIsOne reports whether listMin only refuses an empty list, which
// the namespace template checks with is_empty() (clippy::len_zero).
func (arg ScalarArg) ListMinIsOne() bool {
	return arg.ValidateListMin != nil && *arg.ValidateListMin == 1
}

// NeedsGeneratedValidation reports whether the namespace template emits scalar validation for this arg.
// A map's values are validated against their schema instead (ValueSchema).
func (arg ScalarArg) NeedsGeneratedValidation() bool {
	if arg.IsMap {
		return false
	}
	if arg.IsArray {
		return arg.ValidateListMin != nil || arg.ValidateListMax != nil
	}
	return arg.ValidateMin != nil ||
		arg.ValidateMax != nil ||
		arg.ValidateMinLength != nil ||
		arg.ValidateMaxLength != nil ||
		arg.ValidatePattern != "" ||
		arg.ValidateListMin != nil ||
		arg.ValidateListMax != nil
}

// FileUploadField represents file upload field metadata.
type FileUploadField struct {
	Name     string
	RustName string
	Required bool
}

// IsToolUnavailable reports whether an error indicates a missing external tool.
func IsToolUnavailable(err error) bool {
	return codegen.IsToolUnavailable(err)
}

// Generate generates Rust SDK metadata from API output.
func Generate(apiOutput *apigen.APIOutput, crateName, typesCrate string, clock codegen.Clock) (*SDKOutput, error) {
	if apiOutput == nil || len(apiOutput.Endpoints) == 0 {
		return nil, nil
	}

	names := apiOutput.Naming.OrDefault()
	if crateName == "" {
		crateName = names.RustSDKCrate(apiOutput.SchemaName)
	}
	if typesCrate == "" {
		typesCrate = names.RustTypesCrate(apiOutput.SchemaName)
	}

	output := &SDKOutput{
		SchemaName:       apiOutput.SchemaName,
		CrateName:        crateName,
		TypesCrate:       typesCrate,
		TypesCrateModule: rustutil.CrateNameToModulePath(typesCrate),
		SDKStructName:    toRustTypeName(apiOutput.SchemaName) + "Sdk",
		HasAuth:          apiOutput.HasAuth,
		Timestamp:        clock.RFC3339(),
		Version:          "1.0.0",
	}

	inputSchemasJSON, mapValueSchemas, err := extractInputSchemasForValidation(apiOutput)
	if err != nil {
		return nil, fmt.Errorf("extract input validation schemas: %w", err)
	}
	output.HasInputSchemas = inputSchemasJSON != ""
	output.InputSchemasJSON = inputSchemasJSON

	namespaceMap := make(map[string]*NamespaceInfo)
	for _, endpoint := range apiOutput.Endpoints {
		if endpoint.IsWebhook {
			continue
		}
		nsName := endpoint.Namespace
		if nsName == "" {
			nsName = "root"
		}

		ns, ok := namespaceMap[nsName]
		if !ok {
			ns = &NamespaceInfo{
				Name:       nsName,
				ModuleName: toRustModuleName(nsName),
				StructName: toRustTypeName(nsName) + "Namespace",
				FieldName:  toRustFieldName(nsName),
			}
			namespaceMap[nsName] = ns
		}

		if endpoint.IsScopedEndpoint {
			ns.IsScopedNS = true
			ns.ScopeParamName = endpoint.ScopeParamName
			ns.ScopeFieldName = toRustFieldName(endpoint.ScopeParamName)
		}

		converted := convertEndpoint(endpoint, ns.IsScopedNS, ns.ScopeParamName, mapValueSchemas)
		for _, param := range converted.QueryParams {
			if param.ItemIsText {
				output.ChecksQueryListItems = true
			}
		}
		if converted.ValidatesMapValues {
			output.ValidatesMapValues = true
		}
		if len(converted.PathArgs) > 0 {
			output.HasPathParams = true
		}
		ns.Endpoints = append(ns.Endpoints, converted)
	}

	namespaces := make([]NamespaceInfo, 0, len(namespaceMap))
	for _, ns := range namespaceMap {
		sort.Slice(ns.Endpoints, func(i, j int) bool {
			return ns.Endpoints[i].MethodName < ns.Endpoints[j].MethodName
		})
		for _, ep := range ns.Endpoints {
			if ep.Filterable {
				output.HasFilterableEndpoints = true
				break
			}
		}
		namespaces = append(namespaces, *ns)
	}
	sort.Slice(namespaces, func(i, j int) bool {
		return namespaces[i].Name < namespaces[j].Name
	})
	output.Namespaces = namespaces

	return output, nil
}

const openAPISchemaRefPrefix = "#/components/schemas/"

// extractInputSchemasForValidation returns, as one JSON object, the OpenAPI
// schemas the SDK validates with before a request: each input type's, and
// each map argument's value schema (the additionalProperties of its request
// body property) under mapValueSchemaName, with every component they
// reference. It returns "" when there is none, and the names of the map
// value schemas it holds.
func extractInputSchemasForValidation(apiOutput *apigen.APIOutput) (string, map[string]bool, error) {
	if apiOutput == nil || len(apiOutput.Endpoints) == 0 {
		return "", nil, nil
	}

	inputTypes := make(map[string]struct{})
	hasMapArgs := false
	for _, endpoint := range apiOutput.Endpoints {
		// File upload endpoints split payload fields and files separately.
		// Skip strict input-schema validation to avoid requiring file placeholders.
		// The SDK has no method for a webhook, so nothing validates its input.
		if endpoint.HasFileUpload || endpoint.IsWebhook {
			continue
		}
		for _, arg := range endpoint.BodyArgs {
			hasMapArgs = hasMapArgs || arg.IsMap
		}
		if !endpoint.HasInput || strings.TrimSpace(endpoint.InputType) == "" {
			continue
		}
		inputTypes[endpoint.InputType] = struct{}{}
	}
	if len(inputTypes) == 0 && !hasMapArgs {
		return "", nil, nil
	}

	if strings.TrimSpace(apiOutput.OpenAPISpecRaw) == "" {
		return "", nil, nil
	}

	var openapi struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(apiOutput.OpenAPISpecRaw), &openapi); err != nil {
		return "", nil, fmt.Errorf("parse OpenAPI spec: %w", err)
	}

	includedSchemas := make(map[string]json.RawMessage)
	visited := make(map[string]struct{})

	var includeRefs func(name string, rawSchema json.RawMessage) error
	includeSchema := func(schemaName string) error {
		schemaName = strings.TrimSpace(schemaName)
		if schemaName == "" {
			return nil
		}
		if _, seen := visited[schemaName]; seen {
			return nil
		}
		visited[schemaName] = struct{}{}

		rawSchema, ok := openapi.Components.Schemas[schemaName]
		if !ok {
			return fmt.Errorf("missing OpenAPI schema %q", schemaName)
		}
		includedSchemas[schemaName] = rawSchema
		return includeRefs(schemaName, rawSchema)
	}
	includeRefs = func(name string, rawSchema json.RawMessage) error {
		refSchemas, err := collectSchemaRefs(rawSchema)
		if err != nil {
			return fmt.Errorf("collect refs for schema %q: %w", name, err)
		}
		for _, refSchema := range refSchemas {
			if err := includeSchema(refSchema); err != nil {
				return err
			}
		}
		return nil
	}

	inputNames := make([]string, 0, len(inputTypes))
	for inputName := range inputTypes {
		inputNames = append(inputNames, inputName)
	}
	sort.Strings(inputNames)

	for _, inputName := range inputNames {
		if err := includeSchema(inputName); err != nil {
			return "", nil, err
		}
	}

	mapValueSchemas := make(map[string]bool)
	for _, endpoint := range apiOutput.Endpoints {
		if endpoint.HasFileUpload || endpoint.IsWebhook {
			continue
		}
		for _, arg := range endpoint.BodyArgs {
			if !arg.IsMap {
				continue
			}
			valueSchema, err := requestBodyMapValueSchema(openapi.Paths, endpoint, arg.Name)
			if err != nil {
				return "", nil, err
			}
			if valueSchema == nil {
				continue
			}
			name := mapValueSchemaName(endpoint, arg.Name)
			includedSchemas[name] = valueSchema
			mapValueSchemas[name] = true
			if err := includeRefs(name, valueSchema); err != nil {
				return "", nil, err
			}
		}
	}

	if len(includedSchemas) == 0 {
		return "", nil, nil
	}

	serialized, err := json.Marshal(includedSchemas)
	if err != nil {
		return "", nil, fmt.Errorf("serialize input schemas: %w", err)
	}

	return string(serialized), mapValueSchemas, nil
}

// mapValueSchemaName is the name of a map argument's value schema among the
// SDK's validation schemas: the operation's id and the argument's name,
// which no component name can be.
func mapValueSchemaName(endpoint apigen.EndpointInfo, argName string) string {
	return endpoint.HandlerName + "." + argName
}

// requestBodyMapValueSchema is the value schema of the map argument argName
// of endpoint in the OpenAPI paths: the additionalProperties of its
// property in the JSON request body, which the route validates each value
// against. It is nil when the document has none.
func requestBodyMapValueSchema(paths map[string]map[string]json.RawMessage, endpoint apigen.EndpointInfo, argName string) (json.RawMessage, error) {
	rawOperation, ok := paths[endpoint.Path][strings.ToLower(endpoint.Method)]
	if !ok {
		return nil, nil
	}
	var operation struct {
		RequestBody struct {
			Content map[string]struct {
				Schema struct {
					Properties map[string]struct {
						AdditionalProperties json.RawMessage `json:"additionalProperties"`
					} `json:"properties"`
				} `json:"schema"`
			} `json:"content"`
		} `json:"requestBody"`
	}
	if err := json.Unmarshal(rawOperation, &operation); err != nil {
		return nil, fmt.Errorf("parse OpenAPI operation %s %s: %w", endpoint.Method, endpoint.Path, err)
	}
	valueSchema := operation.RequestBody.Content["application/json"].Schema.Properties[argName].AdditionalProperties
	if len(valueSchema) == 0 {
		return nil, nil
	}
	return valueSchema, nil
}

func collectSchemaRefs(rawSchema json.RawMessage) ([]string, error) {
	var node interface{}
	if err := json.Unmarshal(rawSchema, &node); err != nil {
		return nil, err
	}

	refs := make(map[string]struct{})
	collectSchemaRefsFromNode(node, refs)

	refNames := make([]string, 0, len(refs))
	for refName := range refs {
		refNames = append(refNames, refName)
	}
	sort.Strings(refNames)
	return refNames, nil
}

func collectSchemaRefsFromNode(node interface{}, refs map[string]struct{}) {
	switch typed := node.(type) {
	case map[string]interface{}:
		if rawRef, ok := typed["$ref"].(string); ok {
			if schemaName, ok := schemaNameFromRef(rawRef); ok {
				refs[schemaName] = struct{}{}
			}
		}
		for _, child := range typed {
			collectSchemaRefsFromNode(child, refs)
		}
	case []interface{}:
		for _, child := range typed {
			collectSchemaRefsFromNode(child, refs)
		}
	}
}

func schemaNameFromRef(reference string) (string, bool) {
	if !strings.HasPrefix(reference, openAPISchemaRefPrefix) {
		return "", false
	}
	schemaName := strings.TrimSpace(strings.TrimPrefix(reference, openAPISchemaRefPrefix))
	if schemaName == "" {
		return "", false
	}
	return schemaName, true
}

func convertEndpoint(ep apigen.EndpointInfo, isScopedNS bool, scopeParamName string, mapValueSchemas map[string]bool) EndpointInfo {
	pathFormat, pathArgs, pathQueryBindings := convertPathToRustFormat(ep, isScopedNS, scopeParamName)

	pathParams := make([]PathParam, 0, len(ep.PathParams))
	for _, param := range ep.PathParams {
		if isScopedNS && param.Name == scopeParamName {
			continue
		}
		pathParams = append(pathParams, PathParam{
			Name:     param.Name,
			RustName: toRustFieldName(param.Name),
			RustType: mapURLParamToRust(param.Type),
		})
	}

	queryParams := make([]QueryParam, 0, len(ep.QueryParams))
	hasRequiredQueryParams := false
	for _, param := range ep.QueryParams {
		rustType := mapURLParamToRust(param.Type)
		if param.IsArray {
			rustType = "Vec<" + rustType + ">"
		}
		queryParams = append(queryParams, QueryParam{
			Name:              param.Name,
			RustName:          toRustFieldName(param.Name),
			RustType:          rustType,
			Required:          param.Required,
			IsArray:           param.IsArray,
			ItemIsText:        param.IsArray && sendsText(param),
			ValidateMin:       param.ValidateMin,
			ValidateMax:       param.ValidateMax,
			ValidateMinLength: param.ValidateMinLength,
			ValidateMaxLength: param.ValidateMaxLength,
			ValidateListMin:   runtimeListMinimum(param.ValidateListMin),
			ValidateListMax:   param.ValidateListMax,
			ValidatePattern:   param.ValidatePattern,
		})
		if param.Required {
			hasRequiredQueryParams = true
		}
	}

	// A map argument is a HashMap of its values, each a Vec for a map of
	// lists, as the route takes it (bodyargs.Map, bodyargs.MapOfLists).
	scalarArgs := make([]ScalarArg, 0, len(ep.ScalarArgs))
	validatesMapValues := false
	for _, arg := range ep.ScalarArgs {
		rustType := qualifyType(arg.Type)
		isArray := arg.IsArray
		if isArray {
			rustType = rustListType(rustType, arg.ArrayDepth())
		}
		valueSchema := ""
		if arg.IsMap {
			rustType = "std::collections::HashMap<String, " + rustType + ">"
			if name := mapValueSchemaName(ep, arg.Name); mapValueSchemas[name] {
				valueSchema = name
				validatesMapValues = true
			}
		}
		if !arg.Required {
			rustType = rustutil.WrapOptionalType(rustType)
		}
		scalarArgs = append(scalarArgs, ScalarArg{
			Name:              arg.Name,
			RustName:          toRustFieldName(arg.Name),
			RustType:          rustType,
			Required:          arg.Required,
			IsArray:           isArray,
			ValidateMin:       arg.ValidateMin,
			ValidateMax:       arg.ValidateMax,
			ValidateMinLength: arg.ValidateMinLength,
			ValidateMaxLength: arg.ValidateMaxLength,
			ValidateListMin:   runtimeListMinimum(arg.ValidateListMin),
			ValidateListMax:   arg.ValidateListMax,
			ValidatePattern:   arg.ValidatePattern,
			IsMap:             arg.IsMap,
			ValueSchema:       valueSchema,
		})
	}

	fileUploadFields := make([]FileUploadField, 0, len(ep.FileUploadFields))
	for _, field := range ep.FileUploadFields {
		fileUploadFields = append(fileUploadFields, FileUploadField{
			Name:     field.Name,
			RustName: toRustFieldName(field.Name),
			Required: field.Required,
		})
	}

	methodHTTP := strings.ToUpper(ep.Method)
	methodPrefix := toRustTypeName(ep.Name)
	hasEncryptedBody := ep.Encrypted && apigen.EncryptedBodyMethod(methodHTTP)
	supportsListAll, listAllPageSize := listAllPagination(ep, pathParams, pathQueryBindings, queryParams, scalarArgs, hasRequiredQueryParams)

	return EndpointInfo{
		MethodName:             toRustMethodName(ep.Name),
		HTTPMethod:             methodHTTP,
		PathFormat:             pathFormat,
		PathArgs:               pathArgs,
		PathQueryBindings:      pathQueryBindings,
		InputType:              qualifyType(ep.InputType),
		InputSchemaName:        ep.InputType,
		OutputType:             ep.OutputType,
		OutputRustType:         outputType(ep.OutputType, ep.OutputArrayDepth()),
		HasInput:               ep.HasInput,
		RequiresAuth:           ep.RequiresAuth,
		PathParams:             pathParams,
		QueryParams:            queryParams,
		ScalarArgs:             scalarArgs,
		HasScalarArgs:          len(scalarArgs) > 0,
		ValidatesMapValues:     validatesMapValues,
		ScalarInputStructName:  methodPrefix + "Input",
		QueryStructName:        methodPrefix + "QueryParams",
		HasQueryParams:         len(queryParams) > 0,
		HasRequiredQueryParams: hasRequiredQueryParams,
		Description:            ep.Description,
		Encrypted:              ep.Encrypted,
		HasEncryptedBody:       hasEncryptedBody,
		HasFileUpload:          ep.HasFileUpload,
		FileUploadFields:       fileUploadFields,
		FilesStructName:        methodPrefix + "Files",
		Filterable:             ep.Filterable,
		SupportsListAll:        supportsListAll,
		ListAllPageSize:        listAllPageSize,
	}
}

// listAllPagination decides whether an endpoint gets an auto-paginating
// `{method}_all` variant and returns the page size to generate. Eligible
// endpoints are plain GET-style lists: array output, optional-only query
// params including f64 `limit`/`offset`, and a schema-declared max on
// `limit` (the server's page-size clamp). Anything with a body, path
// params, filters, or encryption keeps only the single-page method.
func listAllPagination(
	ep apigen.EndpointInfo,
	pathParams []PathParam,
	pathQueryBindings []PathQueryBinding,
	queryParams []QueryParam,
	scalarArgs []ScalarArg,
	hasRequiredQueryParams bool,
) (bool, int) {
	if !ep.OutputIsArray || ep.HasInput || ep.HasFileUpload || ep.Filterable || ep.Encrypted {
		return false, 0
	}
	if len(pathParams) > 0 || len(pathQueryBindings) > 0 || len(scalarArgs) > 0 || hasRequiredQueryParams {
		return false, 0
	}

	var limitMax *float64
	hasOffset := false
	for _, param := range queryParams {
		switch param.Name {
		case "limit":
			if param.RustType != "f64" {
				return false, 0
			}
			limitMax = param.ValidateMax
		case "offset":
			if param.RustType != "f64" {
				return false, 0
			}
			hasOffset = true
		}
	}
	if limitMax == nil || !hasOffset {
		return false, 0
	}
	pageSize := int(*limitMax)
	if pageSize < 1 {
		return false, 0
	}
	return true, pageSize
}

// outputType is the Rust type of a response: the qualified output type in
// arrayDepth Vec levels.
func outputType(typeName string, arrayDepth int) string {
	base := qualifyType(typeName)
	if base == "" {
		base = "serde_json::Value"
	}
	return rustListType(base, arrayDepth)
}

// rustListType wraps a Rust element type in depth Vec levels: "T",
// "Vec<T>" or "Vec<Vec<T>>".
func rustListType(elem string, depth int) string {
	return codegen.WrapArray(elem, depth, func(inner string) string { return "Vec<" + inner + ">" })
}

var pathParamPattern = regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)

// convertPathToRustFormat converts an /api/... path with {param} placeholders
// into a Rust format! string plus its argument expressions. Placeholders are
// bound, in order of preference, to: the namespace scope field (`self.<name>`),
// a generated method path parameter, or a query-struct field (operations
// declared with QueryParam<> arguments embed the same values in the @rest
// path). Query-bound placeholders are returned so the template can
// destructure the query struct into locals before building the path.
func convertPathToRustFormat(ep apigen.EndpointInfo, isScopedNS bool, scopeParamName string) (string, []string, []PathQueryBinding) {
	matches := pathParamPattern.FindAllStringSubmatch(ep.Path, -1)
	if len(matches) == 0 {
		return ep.Path, nil, nil
	}

	pathParamNames := make(map[string]struct{}, len(ep.PathParams))
	for _, param := range ep.PathParams {
		pathParamNames[param.Name] = struct{}{}
	}
	queryParamRequired := make(map[string]bool, len(ep.QueryParams))
	for _, param := range ep.QueryParams {
		queryParamRequired[param.Name] = param.Required
	}

	pathFormat := ep.Path
	args := make([]string, 0, len(matches))
	var queryBindings []PathQueryBinding
	for _, match := range matches {
		paramName := match[1]
		pathFormat = strings.Replace(pathFormat, "{"+paramName+"}", "{}", 1)
		rustName := toRustFieldName(paramName)

		if isScopedNS && paramName == scopeParamName {
			args = append(args, "self."+rustName)
			continue
		}
		if _, isPathParam := pathParamNames[paramName]; !isPathParam {
			if required, isQueryParam := queryParamRequired[paramName]; isQueryParam {
				queryBindings = append(queryBindings, PathQueryBinding{
					Name:     paramName,
					RustName: rustName,
					Required: required,
				})
			}
		}
		args = append(args, rustName)
	}

	return pathFormat, args, queryBindings
}

// WriteSDKWithTools writes the generated Rust SDK crate and optional tool calling artifacts.
func WriteSDKWithTools(output *SDKOutput, apiOutput *apigen.APIOutput, outputDir, typesDir string, clock codegen.Clock) error {
	if output == nil {
		return nil
	}

	srcDir := filepath.Join(outputDir, "src")
	namespacesDir := filepath.Join(srcDir, "namespaces")
	dirs := []string{srcDir, namespacesDir}
	if apiOutput != nil {
		dirs = append(dirs, filepath.Join(outputDir, "tools"))
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	output.TypesDependencyPath = rustapigen.ResolveTypesDependencyPath(outputDir, typesDir)

	files := []struct {
		templateName string
		outputPath   string
		label        string
	}{
		{"cargo.tmpl", filepath.Join(outputDir, "Cargo.toml"), "Cargo.toml"},
		{"lib.tmpl", filepath.Join(srcDir, "lib.rs"), "src/lib.rs"},
		{"errors.tmpl", filepath.Join(srcDir, "errors.rs"), "src/errors.rs"},
		{"runtime.tmpl", filepath.Join(srcDir, "runtime.rs"), "src/runtime.rs"},
		{"client.tmpl", filepath.Join(srcDir, "client.rs"), "src/client.rs"},
		{"sdk.tmpl", filepath.Join(srcDir, "sdk.rs"), "src/sdk.rs"},
		{"namespace-mod.tmpl", filepath.Join(namespacesDir, "mod.rs"), "src/namespaces/mod.rs"},
		{"readme.tmpl", filepath.Join(outputDir, "README.md"), "README.md"},
	}
	for _, file := range files {
		if err := generateFile(file.templateName, file.outputPath, output); err != nil {
			return fmt.Errorf("failed to generate %s: %w", file.label, err)
		}
	}

	for _, ns := range output.Namespaces {
		namespaceOutput := struct {
			SDK       *SDKOutput
			Namespace NamespaceInfo
		}{
			SDK:       output,
			Namespace: ns,
		}

		path := filepath.Join(namespacesDir, ns.ModuleName+".rs")
		if err := generateFile("namespace.tmpl", path, namespaceOutput); err != nil {
			return fmt.Errorf("failed to generate namespace %s: %w", ns.Name, err)
		}
	}

	if apiOutput != nil {
		toolsOutput, err := GenerateTools(output, apiOutput, clock)
		if err != nil {
			return fmt.Errorf("failed to generate tools metadata: %w", err)
		}
		if toolsOutput != nil && len(toolsOutput.Tools) > 0 {
			toolsFuncs := toolsTemplateFuncs()
			if err := generateFile(
				"tools-openai.tmpl",
				filepath.Join(outputDir, "tools", "openai.json"),
				toolsOutput,
				toolsFuncs,
			); err != nil {
				return fmt.Errorf("failed to generate tools/openai.json: %w", err)
			}
			if err := generateFile(
				"tools-anthropic.tmpl",
				filepath.Join(outputDir, "tools", "anthropic.json"),
				toolsOutput,
				toolsFuncs,
			); err != nil {
				return fmt.Errorf("failed to generate tools/anthropic.json: %w", err)
			}
			if err := generateFile(
				"tools-schema.tmpl",
				filepath.Join(outputDir, "tools", "schema.json"),
				toolsOutput,
				toolsFuncs,
			); err != nil {
				return fmt.Errorf("failed to generate tools/schema.json: %w", err)
			}
			if err := generateFile(
				"tools-mcp-audit.tmpl",
				filepath.Join(outputDir, "tools", "mcp-audit.json"),
				toolsOutput,
				toolsFuncs,
			); err != nil {
				return fmt.Errorf("failed to generate tools/mcp-audit.json: %w", err)
			}
		}
	}

	return nil
}

func generateFile(templateName, outputPath string, data interface{}, customFuncs ...template.FuncMap) error {
	funcs := templateFuncs()
	if len(customFuncs) > 0 && customFuncs[0] != nil {
		funcs = codegen.MergeTemplateFuncs(funcs, customFuncs[0])
	}

	return codegen.GenerateFile(
		codegen.NewFileConfig(templatesFS, templateName, outputPath, data, funcs),
	)
}

// sendsText reports whether a parameter's value is text on the wire: not a
// number or a boolean, whose text never is empty or holds a comma or
// surrounding space.
func sendsText(param apigen.Param) bool {
	return !param.IsInt && !param.IsFloat && !param.IsBool &&
		param.Type != codegen.PrimitiveNumber && param.Type != codegen.PrimitiveBoolean
}

// runtimeListMinimum drops a zero list minimum: a Rust collection length is
// unsigned, so the check is always true and `len() < 0` draws a compiler
// warning. A positive minimum is kept.
func runtimeListMinimum(value *int) *int {
	if value == nil || *value == 0 {
		return nil
	}
	return value
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"join": strings.Join,
		"rustDoc": func(s string) string {
			return strings.ReplaceAll(s, "\n", "\n    /// ")
		},
		// float64Lit renders a schema validate bound as a Rust f64 literal.
		// Go's default float formatting drops the fraction for integral
		// values ("250"), which fails to compile against an f64 operand.
		"float64Lit": func(value *float64) string {
			if value == nil {
				return "0.0"
			}
			formatted := strconv.FormatFloat(*value, 'f', -1, 64)
			if !strings.ContainsAny(formatted, ".eE") {
				formatted += ".0"
			}
			return formatted
		},
	}
}

func qualifyType(typeName string) string {
	if mapped, ok := rustutil.PrimitiveToRustType(typeName); ok {
		return mapped
	}

	if typeName == "" {
		return ""
	}
	symbol := codegen.BuildScalarTokens(typeName).Symbol
	if symbol == "" {
		symbol = typeName
	}
	return "types::" + symbol
}

func mapURLParamToRust(typeName string) string {
	mapped := qualifyType(typeName)
	if mapped == "" {
		return "String"
	}
	return mapped
}

func toRustTypeName(name string) string {
	normalized := codegen.ToSnakeCase(strings.TrimSpace(name))
	if normalized == "" {
		return "Value"
	}

	var cleaned strings.Builder
	for _, r := range normalized {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			cleaned.WriteRune(unicode.ToLower(r))
		default:
			cleaned.WriteRune('_')
		}
	}
	normalized = rustutil.CollapseUnderscores(strings.Trim(cleaned.String(), "_"))
	if normalized == "" {
		return "Value"
	}

	parts := strings.Split(normalized, "_")
	var b strings.Builder
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		wroteFirst := false
		for _, r := range part {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				continue
			}
			if !wroteFirst {
				if unicode.IsDigit(r) {
					b.WriteRune('N')
				}
				b.WriteRune(unicode.ToUpper(r))
				wroteFirst = true
				continue
			}
			b.WriteRune(unicode.ToLower(r))
		}
	}

	result := b.String()
	if result == "" {
		return "Value"
	}
	if result[0] >= '0' && result[0] <= '9' {
		return "N" + result
	}
	return result
}

func toRustMethodName(name string) string {
	return toRustIdentifier(name, "call")
}

func toRustFieldName(name string) string {
	return toRustIdentifier(name, "value")
}

func toRustModuleName(name string) string {
	return toRustIdentifier(name, "root")
}

func toRustIdentifier(name, fallback string) string {
	base := codegen.ToSnakeCase(strings.TrimSpace(name))
	if base == "" {
		return fallback
	}

	var b strings.Builder
	for _, r := range base {
		switch {
		case r == '_':
			b.WriteRune(r)
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune('_')
		}
	}

	identifier := rustutil.CollapseUnderscores(strings.Trim(b.String(), "_"))
	if identifier == "" {
		identifier = fallback
	}
	if identifier[0] >= '0' && identifier[0] <= '9' {
		identifier = "_" + identifier
	}
	if rustutil.IsRustKeyword(identifier) {
		identifier = "r#" + identifier
	}
	return identifier
}

// FormatSDK runs cargo fmt on the generated Rust SDK crate.
func FormatSDK(outputDir string) error {
	if err := codegen.RunTool(outputDir, "cargo", "fmt"); err != nil {
		return fmt.Errorf("format rust sdk files with cargo fmt: %w", err)
	}
	return nil
}

// CompileSDK validates generated Rust SDK code with cargo check.
func CompileSDK(outputDir string) error {
	if err := codegen.RunTool(outputDir, "cargo", "check"); err != nil {
		return fmt.Errorf("rust sdk compilation failed: %w", err)
	}
	return nil
}
