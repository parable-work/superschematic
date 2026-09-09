package pysdkgen

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
)

var pythonKeywords = map[string]struct{}{
	"false": {}, "none": {}, "true": {}, "and": {}, "as": {}, "assert": {},
	"async": {}, "await": {}, "break": {}, "class": {}, "continue": {}, "def": {},
	"del": {}, "elif": {}, "else": {}, "except": {}, "finally": {}, "for": {},
	"from": {}, "global": {}, "if": {}, "import": {}, "in": {}, "is": {},
	"lambda": {}, "nonlocal": {}, "not": {}, "or": {}, "pass": {}, "raise": {},
	"return": {}, "try": {}, "while": {}, "with": {}, "yield": {}, "match": {},
	"case": {},
}

var (
	nonIdentifierPattern = regexp.MustCompile(`[^a-zA-Z0-9_]+`)
	segmentPattern       = regexp.MustCompile(`[^a-zA-Z0-9]+`)
	camelBoundaryPattern = regexp.MustCompile(`([a-z0-9])([A-Z])`)
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// SDKOutput contains all generated Python SDK metadata.
type SDKOutput struct {
	SchemaName             string
	Author                 string // manifest author, from Naming.PackageAuthor
	PackageName            string
	TypesPackage           string
	SDKClassName           string
	Namespaces             []NamespaceInfo
	HasAuth                bool
	HasFilterableEndpoints bool // Gates FilterSpec in client.py to avoid dead code in non-filterable SDKs.
	Timestamp              string
	Version                string
}

// NamespaceInfo represents a namespace with its endpoints for the Python SDK.
type NamespaceInfo struct {
	Name                   string
	ModuleName             string
	ClassName              string
	AccessorName           string
	IsScopedNS             bool
	ScopeParamName         string
	ScopeParamType         string
	scopeParamOriginal     string
	Endpoints              []EndpointInfo
	Imports                []string
	HasAuth                bool
	HasEncryptedPayload    bool
	HasFilterableEndpoints bool
}

// EndpointInfo represents a single API endpoint for the Python SDK.
type EndpointInfo struct {
	MethodName           string
	HTTPMethod           string
	PathTemplate         string
	PathIsFString        bool
	Description          string
	InputType            string
	OutputTypeHint       string
	OutputModelName      string // Bare type name when output is an importable model; empty otherwise.
	OutputIsArray        bool
	HasInput             bool
	InputRequired        bool
	RequiresAuth         bool
	PathParams           []PathParam
	QueryParams          []QueryParam
	HasQueryParams       bool
	ScalarArgs           []ScalarArg
	HasScalarArgs        bool
	HasRequestBody       bool
	HasEncryptedBody     bool
	HasFileUpload        bool
	HasRequiredFileField bool
	FileUploadFields     []FileUploadField
	FilesTypeName        string
	MultipartStripKeys   []string
	MethodParams         []MethodParam
	Filterable           bool
}

// PathParam represents a URL path parameter mapped to a Python identifier.
type PathParam struct {
	Name   string
	PyName string
	PyType string
}

// QueryParam represents a query string parameter mapped to a Python identifier.
type QueryParam struct {
	Name     string
	PyName   string
	PyType   string
	Required bool

	ValidateMin       *float64
	ValidateMax       *float64
	ValidateMinLength *int
	ValidateMaxLength *int
	ValidateListMin   *int
	ValidateListMax   *int
	ValidatePattern   string
}

// ScalarArg represents a scalar input argument for an endpoint.
type ScalarArg struct {
	Name          string
	PyName        string
	PyType        string // Full type including list[] wrapper when IsArray is true
	PyElementType string // Element type for per-element validation (only set when IsArray is true)
	Required      bool
	IsArray       bool
}

// FileUploadField represents file upload field metadata for the Python SDK.
type FileUploadField struct {
	Name     string
	PyName   string
	Required bool
}

// MethodParam represents a single parameter declaration in a generated Python method signature.
type MethodParam struct {
	Declaration string
}

var errToolUnavailable = errors.New("required tool unavailable")

// ToolUnavailableError indicates an external tool is unavailable in PATH.
type ToolUnavailableError struct {
	Tool string
	Err  error
}

func (e *ToolUnavailableError) Error() string {
	return fmt.Sprintf("%s is not available: %v", e.Tool, e.Err)
}

func (e *ToolUnavailableError) Unwrap() error {
	return errToolUnavailable
}

// IsToolUnavailable reports whether an error indicates a missing external tool.
func IsToolUnavailable(err error) bool {
	return errors.Is(err, errToolUnavailable)
}

// Generate maps API output metadata to Python SDK metadata.
func Generate(apiOutput *apigen.APIOutput, packageName, typesPackage string, clock codegen.Clock) (*SDKOutput, error) {
	if apiOutput == nil || len(apiOutput.Endpoints) == 0 {
		return nil, nil
	}

	names := apiOutput.Naming.OrDefault()
	resolvedPackageName := resolvePythonSDKPackageName(packageName, apiOutput.SchemaName, names)
	resolvedTypesPackage := resolvePythonTypesPackageName(typesPackage, apiOutput.SchemaName, names)

	output := &SDKOutput{
		SchemaName:   apiOutput.SchemaName,
		PackageName:  resolvedPackageName,
		TypesPackage: resolvedTypesPackage,
		SDKClassName: toPythonClassName(apiOutput.SchemaName) + "SDK",
		Author:       names.PackageAuthor,
		Namespaces:   nil,
		HasAuth:      apiOutput.IsPublic && apiOutput.HasAuth,
		Timestamp:    clock.RFC3339(),
		Version:      "1.0.0",
	}

	namespaceMap := make(map[string]*NamespaceInfo)
	importsByNamespace := make(map[string]map[string]struct{})

	for _, endpoint := range apiOutput.Endpoints {
		namespaceName := endpoint.Namespace
		if namespaceName == "" {
			namespaceName = "root"
		}

		namespace, exists := namespaceMap[namespaceName]
		if !exists {
			namespace = &NamespaceInfo{
				Name:         namespaceName,
				ModuleName:   toPythonIdentifier(namespaceName),
				ClassName:    toPythonClassName(namespaceName) + "Namespace",
				AccessorName: toPythonIdentifier(namespaceName),
				Endpoints:    []EndpointInfo{},
				Imports:      []string{},
			}
			namespaceMap[namespaceName] = namespace
			importsByNamespace[namespaceName] = make(map[string]struct{})
		}

		if endpoint.IsScopedEndpoint {
			namespace.IsScopedNS = true
			if namespace.scopeParamOriginal == "" {
				namespace.scopeParamOriginal = endpoint.ScopeParamName
			}
			namespace.ScopeParamName = toPythonIdentifier(namespace.scopeParamOriginal)
			if namespace.ScopeParamType == "" {
				namespace.ScopeParamType = mapIRTypeToPython(findPathParamType(endpoint.PathParams, namespace.scopeParamOriginal))
			}
			if namespace.ScopeParamType == "" {
				namespace.ScopeParamType = "str"
			}
		}

		converted := convertEndpoint(endpoint, namespace.IsScopedNS, namespace.scopeParamOriginal)
		namespace.Endpoints = append(namespace.Endpoints, converted)

		if endpoint.RequiresAuth {
			namespace.HasAuth = true
			output.HasAuth = true
		}
		if converted.HasEncryptedBody {
			namespace.HasEncryptedPayload = true
		}
		if endpoint.Filterable {
			namespace.HasFilterableEndpoints = true
			output.HasFilterableEndpoints = true
		}

		collectNamespaceImports(importsByNamespace[namespaceName], endpoint)
	}

	for name, importSet := range importsByNamespace {
		imports := make([]string, 0, len(importSet))
		for typeName := range importSet {
			imports = append(imports, typeName)
		}
		sort.Strings(imports)
		namespaceMap[name].Imports = imports
	}

	namespaces := make([]NamespaceInfo, 0, len(namespaceMap))
	for _, namespace := range namespaceMap {
		sort.Slice(namespace.Endpoints, func(i, j int) bool {
			return namespace.Endpoints[i].MethodName < namespace.Endpoints[j].MethodName
		})
		namespaces = append(namespaces, *namespace)
	}
	sort.Slice(namespaces, func(i, j int) bool {
		return namespaces[i].Name < namespaces[j].Name
	})
	output.Namespaces = namespaces

	return output, nil
}

func collectNamespaceImports(target map[string]struct{}, endpoint apigen.EndpointInfo) {
	if endpoint.InputType != "" && shouldImportIRType(endpoint.InputType) {
		target[endpoint.InputType] = struct{}{}
	}
	if endpoint.OutputType != "" && shouldImportIRType(endpoint.OutputType) {
		target[endpoint.OutputType] = struct{}{}
	}
	for _, param := range endpoint.PathParams {
		if shouldImportIRType(param.Type) {
			target[param.Type] = struct{}{}
		}
	}
	for _, param := range endpoint.QueryParams {
		if shouldImportIRType(param.Type) {
			target[param.Type] = struct{}{}
		}
	}
	for _, arg := range endpoint.ScalarArgs {
		if shouldImportIRType(arg.Type) {
			target[arg.Type] = struct{}{}
		}
	}
}

func convertEndpoint(endpoint apigen.EndpointInfo, isScopedNS bool, scopeParamOriginal string) EndpointInfo {
	pathParams := make([]PathParam, 0, len(endpoint.PathParams))
	for _, param := range endpoint.PathParams {
		if isScopedNS && param.Name == scopeParamOriginal {
			continue
		}
		pathParams = append(pathParams, PathParam{
			Name:   param.Name,
			PyName: toPythonIdentifier(param.Name),
			PyType: mapIRTypeToPython(param.Type),
		})
	}

	queryParams := make([]QueryParam, 0, len(endpoint.QueryParams))
	for _, param := range endpoint.QueryParams {
		queryParams = append(queryParams, QueryParam{
			Name:              param.Name,
			PyName:            toPythonIdentifier(param.Name),
			PyType:            mapIRTypeToPython(param.Type),
			Required:          param.Required,
			ValidateMin:       param.ValidateMin,
			ValidateMax:       param.ValidateMax,
			ValidateMinLength: param.ValidateMinLength,
			ValidateMaxLength: param.ValidateMaxLength,
			ValidateListMin:   param.ValidateListMin,
			ValidateListMax:   param.ValidateListMax,
			ValidatePattern:   param.ValidatePattern,
		})
	}

	scalarArgs := make([]ScalarArg, 0, len(endpoint.ScalarArgs))
	for _, arg := range endpoint.ScalarArgs {
		pyElementType := ""
		pyType := mapIRTypeToPython(arg.Type)
		if arg.IsArray {
			pyElementType = pyType
			pyType = "list[" + pyType + "]"
		}
		scalarArgs = append(scalarArgs, ScalarArg{
			Name:          arg.Name,
			PyName:        toPythonIdentifier(arg.Name),
			PyType:        pyType,
			PyElementType: pyElementType,
			Required:      arg.Required,
			IsArray:       arg.IsArray,
		})
	}

	fileFields := make([]FileUploadField, 0, len(endpoint.FileUploadFields))
	multipartStripKeys := make([]string, 0, len(endpoint.FileUploadFields)*2)
	seenStripKeys := map[string]struct{}{}
	for _, field := range endpoint.FileUploadFields {
		pythonFieldName := toPythonIdentifier(field.Name)
		fileFields = append(fileFields, FileUploadField{
			Name:     field.Name,
			PyName:   pythonFieldName,
			Required: field.Required,
		})
		for _, stripKey := range []string{field.Name, pythonFieldName} {
			if _, exists := seenStripKeys[stripKey]; exists {
				continue
			}
			seenStripKeys[stripKey] = struct{}{}
			multipartStripKeys = append(multipartStripKeys, stripKey)
		}
	}
	sort.Strings(multipartStripKeys)

	sdkPath := endpoint.Path
	sdkHTTPMethod := endpoint.Method
	pathTemplate, pathIsFString := convertPathToPythonTemplate(sdkPath, endpoint.PathParams, isScopedNS, scopeParamOriginal)
	hasEncryptedBody := endpoint.Encrypted && isBodyMethod(sdkHTTPMethod)
	// Endpoints with an explicit input object can require a JSON body even on GET
	// (for example, GET /api/users/events with UserEventsSearchInput).
	// Scalar-arg-only GET endpoints still use query parameters.
	hasRequestBody := endpoint.HasInput || (isBodyMethod(sdkHTTPMethod) && len(scalarArgs) > 0)
	hasFileUpload := endpoint.HasFileUpload && len(fileFields) > 0

	hasRequiredFileField := false
	for _, f := range fileFields {
		if f.Required {
			hasRequiredFileField = true
			break
		}
	}

	converted := EndpointInfo{
		MethodName:           toPythonIdentifier(endpoint.Name),
		HTTPMethod:           strings.ToUpper(sdkHTTPMethod),
		PathTemplate:         pathTemplate,
		PathIsFString:        pathIsFString,
		Description:          endpoint.Description,
		InputType:            endpoint.InputType,
		OutputTypeHint:       mapIRTypeToPythonTypeRef(endpoint.OutputType, endpoint.OutputIsArray),
		OutputModelName:      outputModelName(endpoint.OutputType),
		OutputIsArray:        endpoint.OutputIsArray,
		HasInput:             endpoint.HasInput,
		InputRequired:        endpoint.InputRequired,
		RequiresAuth:         endpoint.RequiresAuth,
		PathParams:           pathParams,
		QueryParams:          queryParams,
		HasQueryParams:       len(queryParams) > 0,
		ScalarArgs:           scalarArgs,
		HasScalarArgs:        len(scalarArgs) > 0,
		HasRequestBody:       hasRequestBody,
		HasEncryptedBody:     hasEncryptedBody,
		HasFileUpload:        hasFileUpload,
		HasRequiredFileField: hasRequiredFileField,
		FileUploadFields:     fileFields,
		FilesTypeName:        toPythonClassName(endpoint.Name) + "Files",
		MultipartStripKeys:   multipartStripKeys,
		Filterable:           endpoint.Filterable,
		MethodParams:         buildMethodParams(pathParams, endpoint.HasInput, endpoint.InputType, scalarArgs, queryParams, hasFileUpload, hasRequiredFileField, toPythonClassName(endpoint.Name)+"Files", hasEncryptedBody, endpoint.Filterable),
	}

	if converted.OutputTypeHint == "" {
		converted.OutputTypeHint = "Any"
	}

	return converted
}

func buildMethodParams(
	pathParams []PathParam,
	hasInput bool,
	inputType string,
	scalarArgs []ScalarArg,
	queryParams []QueryParam,
	hasFileUpload bool,
	hasRequiredFileField bool,
	filesTypeName string,
	hasEncryptedBody bool,
	isFilterable bool,
) []MethodParam {
	methodParams := make([]MethodParam, 0, len(pathParams)+len(scalarArgs)+len(queryParams)+6)

	for _, param := range pathParams {
		methodParams = append(methodParams, MethodParam{
			Declaration: fmt.Sprintf("%s: %s", param.PyName, param.PyType),
		})
	}

	if hasInput {
		methodParams = append(methodParams, MethodParam{
			Declaration: fmt.Sprintf("input_data: %s", inputType),
		})
	}

	if hasFileUpload && hasRequiredFileField {
		methodParams = append(methodParams, MethodParam{
			Declaration: fmt.Sprintf("files: %s", filesTypeName),
		})
	}

	for _, arg := range scalarArgs {
		if !arg.Required {
			continue
		}
		methodParams = append(methodParams, MethodParam{
			Declaration: fmt.Sprintf("%s: %s", arg.PyName, arg.PyType),
		})
	}

	for _, param := range queryParams {
		if !param.Required {
			continue
		}
		methodParams = append(methodParams, MethodParam{
			Declaration: fmt.Sprintf("%s: %s", param.PyName, param.PyType),
		})
	}

	if hasFileUpload && !hasRequiredFileField {
		methodParams = append(methodParams, MethodParam{
			Declaration: fmt.Sprintf("files: %s | None = None", filesTypeName),
		})
	}

	for _, arg := range scalarArgs {
		if arg.Required {
			continue
		}
		methodParams = append(methodParams, MethodParam{
			Declaration: fmt.Sprintf("%s: %s | None = None", arg.PyName, arg.PyType),
		})
	}

	for _, param := range queryParams {
		if param.Required {
			continue
		}
		methodParams = append(methodParams, MethodParam{
			Declaration: fmt.Sprintf("%s: %s | None = None", param.PyName, param.PyType),
		})
	}

	if isFilterable {
		methodParams = append(methodParams, MethodParam{
			Declaration: "filters: dict[str, str | FilterSpec] | None = None",
		})
	}

	if hasEncryptedBody {
		methodParams = append(methodParams, MethodParam{
			Declaration: "options: EncryptedRequestOptions | None = None",
		})
	}

	methodParams = append(methodParams, MethodParam{
		Declaration: "timeout_seconds: float | None = None",
	})

	methodParams = append(methodParams, MethodParam{
		Declaration: "extra_headers: Mapping[str, str] | None = None",
	})

	return methodParams
}

func convertPathToPythonTemplate(path string, pathParams []apigen.PathParam, isScopedNS bool, scopeParamOriginal string) (string, bool) {
	result := path
	for _, param := range pathParams {
		placeholder := "{" + param.Name + "}"
		replacementName := toPythonIdentifier(param.Name)
		if isScopedNS && param.Name == scopeParamOriginal {
			replacementName = "self._" + replacementName
		}
		result = strings.ReplaceAll(result, placeholder, "{"+replacementName+"}")
	}

	return result, strings.Contains(result, "{")
}

func resolvePythonTypesPackageName(configuredPackageName, schemaName string, names naming.Naming) string {
	packageName := strings.ReplaceAll(strings.TrimSpace(configuredPackageName), "-", "_")
	if packageName != "" {
		return packageName
	}
	return names.PythonTypesModule(strings.ReplaceAll(schemaName, "-", "_"))
}

func resolvePythonSDKPackageName(configuredPackageName, schemaName string, names naming.Naming) string {
	packageName := strings.ReplaceAll(strings.TrimSpace(configuredPackageName), "-", "_")
	if packageName != "" {
		return packageName
	}
	return names.PythonSDKModule(strings.ReplaceAll(schemaName, "-", "_"))
}

func mapIRTypeToPython(typeName string) string {
	switch typeName {
	case codegen.PrimitiveString:
		return "str"
	case codegen.PrimitiveNumber:
		return "float"
	case codegen.PrimitiveBoolean:
		return "bool"
	default:
		if !strings.Contains(typeName, ".") {
			return typeName
		}
		traits := codegen.GuessScalarTraits(typeName)
		switch {
		case traits.IsIntegerLike:
			return "int"
		case traits.IsFloatLike:
			return "float"
		case traits.IsBooleanLike:
			return "bool"
		default:
			return "str"
		}
	}
}

func mapIRTypeToPythonTypeRef(typeName string, isArray bool) string {
	base := mapIRTypeToPython(typeName)
	if base == "" {
		return ""
	}
	if isArray {
		return "list[" + base + "]"
	}
	return base
}

// outputModelName returns the bare IR type name for endpoint outputs that
// the type library exposes as importable Pydantic models, and the empty string
// for primitives, enums, or void responses. Used by namespace.tmpl to decide
// whether to coerce raw response dicts into typed models via from_dict_non_strict.
func outputModelName(typeName string) string {
	if !shouldImportIRType(typeName) {
		return ""
	}
	return typeName
}

func shouldImportIRType(typeName string) bool {
	if typeName == "" {
		return false
	}
	switch mapIRTypeToPython(typeName) {
	case "str", "int", "float", "bool":
		return false
	default:
		return true
	}
}

func findPathParamType(pathParams []apigen.PathParam, name string) string {
	for _, pathParam := range pathParams {
		if pathParam.Name == name {
			return pathParam.Type
		}
	}
	return ""
}

func isBodyMethod(method string) bool {
	switch strings.ToUpper(method) {
	case "POST", "PUT", "PATCH":
		return true
	default:
		return false
	}
}

func toPythonIdentifier(name string) string {
	if strings.TrimSpace(name) == "" {
		return "value"
	}

	normalized := camelBoundaryPattern.ReplaceAllString(name, "${1}_${2}")
	normalized = nonIdentifierPattern.ReplaceAllString(normalized, "_")
	normalized = strings.Trim(normalized, "_")
	normalized = strings.ToLower(normalized)
	if normalized == "" {
		normalized = "value"
	}
	if normalized[0] >= '0' && normalized[0] <= '9' {
		normalized = "v_" + normalized
	}
	if _, isKeyword := pythonKeywords[normalized]; isKeyword {
		return normalized + "_"
	}
	return normalized
}

func toPythonClassName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "Value"
	}

	normalized := camelBoundaryPattern.ReplaceAllString(name, "${1}_${2}")
	parts := segmentPattern.Split(normalized, -1)

	var builder strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		lower := strings.ToLower(part)
		builder.WriteString(strings.ToUpper(lower[:1]))
		if len(lower) > 1 {
			builder.WriteString(lower[1:])
		}
	}

	if builder.Len() == 0 {
		return "Value"
	}

	return builder.String()
}

// WriteSDK writes generated Python SDK files.
func WriteSDK(output *SDKOutput, outputDir string) error {
	if output == nil {
		return nil
	}

	packageDir := filepath.Join(outputDir, output.PackageName)
	namespacesDir := filepath.Join(packageDir, "namespaces")
	for _, dir := range []string{outputDir, packageDir, namespacesDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	if err := generateFile("pyproject.tmpl", filepath.Join(outputDir, "pyproject.toml"), output); err != nil {
		return fmt.Errorf("failed to generate pyproject.toml: %w", err)
	}
	if err := generateFile("setup.tmpl", filepath.Join(outputDir, "setup.py"), output); err != nil {
		return fmt.Errorf("failed to generate setup.py: %w", err)
	}
	if err := generateFile("readme.tmpl", filepath.Join(outputDir, "README.md"), output); err != nil {
		return fmt.Errorf("failed to generate README.md: %w", err)
	}
	if err := generateFile("package_init.tmpl", filepath.Join(packageDir, "__init__.py"), output); err != nil {
		return fmt.Errorf("failed to generate package __init__.py: %w", err)
	}
	if err := generateFile("errors.tmpl", filepath.Join(packageDir, "errors.py"), output); err != nil {
		return fmt.Errorf("failed to generate errors.py: %w", err)
	}
	if err := generateFile("client.tmpl", filepath.Join(packageDir, "client.py"), output); err != nil {
		return fmt.Errorf("failed to generate client.py: %w", err)
	}
	if err := generateFile("sdk.tmpl", filepath.Join(packageDir, "sdk.py"), output); err != nil {
		return fmt.Errorf("failed to generate sdk.py: %w", err)
	}
	if err := generateFile("namespaces_init.tmpl", filepath.Join(namespacesDir, "__init__.py"), output); err != nil {
		return fmt.Errorf("failed to generate namespaces __init__.py: %w", err)
	}

	for _, namespace := range output.Namespaces {
		namespaceOutput := struct {
			SDK       *SDKOutput
			Namespace NamespaceInfo
		}{
			SDK:       output,
			Namespace: namespace,
		}

		outputPath := filepath.Join(namespacesDir, namespace.ModuleName+".py")
		if err := generateFile("namespace.tmpl", outputPath, namespaceOutput); err != nil {
			return fmt.Errorf("failed to generate namespace %s: %w", namespace.Name, err)
		}
	}

	if err := os.WriteFile(filepath.Join(packageDir, "py.typed"), []byte(""), 0644); err != nil {
		return fmt.Errorf("failed to create py.typed marker: %w", err)
	}

	return nil
}

func generateFile(templateName, outputPath string, data interface{}) error {
	return codegen.GenerateFile(codegen.NewFileConfig(templatesFS, templateName, outputPath, data, nil))
}

// FormatSDK runs ruff format on the generated Python SDK files.
func FormatSDK(outputDir string) error {
	if err := runTool(outputDir, "ruff", "format", outputDir); err != nil {
		return fmt.Errorf("format python sdk with ruff: %w", err)
	}

	return nil
}

// CompileSDK validates generated Python SDK code for syntax and imports.
func CompileSDK(outputDir string) error {
	python, err := findCompatiblePython()
	if err != nil {
		return err
	}

	if err := runTool(outputDir, python, "-m", "compileall", "-q", outputDir); err != nil {
		return fmt.Errorf("python syntax validation failed: %w", err)
	}

	packageDirs, err := discoverPythonPackageDirs(outputDir)
	if err != nil {
		return fmt.Errorf("discover generated python sdk packages: %w", err)
	}
	if len(packageDirs) == 0 {
		return nil
	}

	for _, packageDir := range packageDirs {
		moduleName := filepath.Base(packageDir)
		if err := runTool(
			outputDir,
			python,
			"-c",
			fmt.Sprintf(
				"import importlib; import sys; sys.path.insert(0, %q); importlib.import_module(%q)",
				outputDir,
				moduleName,
			),
		); err != nil {
			return fmt.Errorf("python import smoke test failed for %s: %w", moduleName, err)
		}
	}

	return nil
}

// minPythonMajor and minPythonMinor must match requires-python in templates/pyproject.tmpl.
const (
	minPythonMajor = 3
	minPythonMinor = 12
)

// findCompatiblePython locates a python binary on PATH whose version satisfies
// the SDK's requires-python. It prefers explicitly-versioned binaries
// (python3.13, python3.12) and falls back to plain python3 / python only if
// their version meets the minimum. Returns ToolUnavailableError when no
// compatible interpreter is available, so callers can downgrade to a warning
// in local environments where a stale system python (e.g. macOS 3.9) is on
// PATH ahead of a newer one.
func findCompatiblePython() (string, error) {
	candidates := []string{"python3.13", "python3.12", "python3", "python"}

	var firstFound string
	for _, name := range candidates {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if firstFound == "" {
			firstFound = path
		}
		ok, vErr := pythonMeetsMinimum(path)
		if vErr != nil {
			continue
		}
		if ok {
			return path, nil
		}
	}

	if firstFound != "" {
		return "", &ToolUnavailableError{
			Tool: "python3",
			Err:  fmt.Errorf("no python>=%d.%d on PATH (found %s)", minPythonMajor, minPythonMinor, firstFound),
		}
	}
	return "", &ToolUnavailableError{
		Tool: "python3",
		Err:  &exec.Error{Name: "python3", Err: exec.ErrNotFound},
	}
}

var pythonVersionPattern = regexp.MustCompile(`Python\s+(\d+)\.(\d+)`)

func pythonMeetsMinimum(binary string) (bool, error) {
	out, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil {
		return false, err
	}
	match := pythonVersionPattern.FindStringSubmatch(string(out))
	if len(match) != 3 {
		return false, fmt.Errorf("unrecognized python version output: %q", strings.TrimSpace(string(out)))
	}
	major, err := strconv.Atoi(match[1])
	if err != nil {
		return false, fmt.Errorf("parse python major version %q: %w", match[1], err)
	}
	minor, err := strconv.Atoi(match[2])
	if err != nil {
		return false, fmt.Errorf("parse python minor version %q: %w", match[2], err)
	}
	if major > minPythonMajor {
		return true, nil
	}
	if major == minPythonMajor && minor >= minPythonMinor {
		return true, nil
	}
	return false, nil
}

func discoverPythonPackageDirs(outputDir string) ([]string, error) {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil, err
	}

	packages := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		initFile := filepath.Join(outputDir, entry.Name(), "__init__.py")
		if _, err := os.Stat(initFile); err == nil {
			packages = append(packages, filepath.Join(outputDir, entry.Name()))
		}
	}

	return packages, nil
}

func runTool(workingDir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = workingDir
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	var execErr *exec.Error
	if errors.As(err, &execErr) && errors.Is(execErr.Err, exec.ErrNotFound) {
		return &ToolUnavailableError{
			Tool: name,
			Err:  execErr,
		}
	}

	return fmt.Errorf("%s %s failed: %w (output: %s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
}
