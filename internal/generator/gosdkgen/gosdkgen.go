// Package gosdkgen generates Go SDK code from API schema definitions.
package gosdkgen

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/goutil"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/nestedguard"
	"github.com/parable-work/superschematic/internal/generator/sdkgen"
	"github.com/parable-work/superschematic/internal/generator/toolsutil"
	"github.com/parable-work/superschematic/internal/profile"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// SetReplacePaths sets the go.mod replace directive path for the http
// runtime relative to the output directory. An unset path emits no
// directive, so the module resolves the published runtime.
func SetReplacePaths(output *SDKOutput, paths naming.LocalPaths, outputDir string) error {
	rel, err := naming.RelPath(outputDir, paths.HTTPRuntimeGo)
	if err != nil {
		return fmt.Errorf("http runtime replace path: %w", err)
	}
	output.HTTPRuntimeReplacePath = rel
	return nil
}

// SDKOutput contains all generated Go SDK metadata.
type SDKOutput struct {
	SchemaName             string
	ModulePath             string
	PackageName            string
	TypesModule            string
	TypesReplacePath       string
	TypeModuleReplaces     []ModuleReplace
	HTTPRuntimeReplacePath string // set by SetReplacePaths; empty omits the directive
	SDKStructName          string
	Namespaces             []NamespaceInfo
	HasAuth                bool
	HasFilterableEndpoints bool
	Runtime                RuntimeSurface
	Timestamp              string
	Version                string
	Naming                 naming.Naming
}

// ModuleReplace describes a go.mod replace directive needed by generated SDK modules.
type ModuleReplace struct {
	Module  string
	RelPath string
}

// RuntimeSurface describes runtime/client feature switches for templates.
type RuntimeSurface struct {
	HasAuth              bool
	SupportsTokenStorage bool
	SupportsTokenRefresh bool
	SupportsInterceptors bool
}

// NamespaceInfo represents a namespace with its endpoints.
type NamespaceInfo struct {
	Name             string
	StructName       string
	FieldName        string
	IsScopedNS       bool
	ScopeParamName   string
	ScopeFieldName   string
	ScopeFieldGoName string
	RequiresTypes    bool
	NeedsURLPkg      bool
	NeedsRuntimePkg  bool
	NeedsFmtPkg      bool
	NeedsStringsPkg  bool
	NeedsRegexpPkg   bool
	Endpoints        []EndpointInfo
}

// EndpointInfo represents a single API endpoint for the Go SDK.
type EndpointInfo struct {
	MethodName               string
	HTTPMethod               string
	PathFormat               string
	PathArgs                 []string
	InputType                string
	OutputType               string
	OutputGoType             string
	OutputUnionWrapperGoType string
	IsUnionOutput            bool
	HasInput                 bool
	RequiresAuth             bool
	PathParams               []PathParam
	QueryParams              []QueryParam
	ScalarArgs               []ScalarArg
	HasScalarArgs            bool
	ScalarInputStructName    string
	QueryStructName          string
	HasQueryParams           bool
	Description              string
	Encrypted                bool
	HasEncryptedBody         bool
	HasFileUpload            bool
	FileUploadFields         []FileUploadField
	FilesStructName          string
	Filterable               bool
}

// PathParam represents a path parameter.
type PathParam struct {
	Name   string
	GoName string
	GoType string
}

// QueryParam represents a query string parameter.
type QueryParam struct {
	Name     string
	GoName   string
	GoType   string
	Required bool
	Pointer  bool

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
	Name     string
	GoName   string
	GoType   string
	Required bool
	Pointer  bool
	IsArray  bool

	ValidateMin       *float64
	ValidateMax       *float64
	ValidateMinLength *int
	ValidateMaxLength *int
	ValidateListMin   *int
	ValidateListMax   *int
	ValidatePattern   string
}

// FileUploadField represents file upload field metadata.
type FileUploadField struct {
	Name     string
	GoName   string
	Required bool
}

// Generate generates Go SDK metadata from API output.
func Generate(apiOutput *apigen.APIOutput, modulePath, packageName string, clock codegen.Clock) (*SDKOutput, error) {
	if apiOutput == nil || len(apiOutput.Endpoints) == 0 {
		return nil, nil
	}
	// nested-arrays guard: remove when gosdkgen renders T[][].
	if err := nestedguard.Check("gosdkgen", apiOutput); err != nil {
		return nil, err
	}

	names := apiOutput.Naming.OrDefault()
	if modulePath == "" {
		modulePath = names.GoSDKModule(apiOutput.SchemaName)
	}
	if packageName == "" {
		packageName = "sdk"
	}

	hasAuth := apiOutput.IsPublic && apiOutput.HasAuth
	output := &SDKOutput{
		SchemaName:    apiOutput.SchemaName,
		ModulePath:    modulePath,
		PackageName:   packageName,
		TypesModule:   apiOutput.TypesModule,
		SDKStructName: goutil.GoPublicIdentifier(apiOutput.SchemaName) + "SDK",
		HasAuth:       hasAuth,
		Naming:        names,
		Runtime: RuntimeSurface{
			HasAuth:              hasAuth,
			SupportsTokenStorage: true,
			SupportsTokenRefresh: hasAuth,
			SupportsInterceptors: true,
		},
		Timestamp: clock.RFC3339(),
		Version:   "1.0.0",
	}

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
				StructName: goutil.GoPublicIdentifier(nsName) + "Namespace",
				FieldName:  goutil.GoPrivateIdentifier(nsName),
			}
			namespaceMap[nsName] = ns
		}
		if endpoint.IsScopedEndpoint {
			ns.IsScopedNS = true
			ns.ScopeParamName = endpoint.ScopeParamName
			ns.ScopeFieldName = goutil.GoPrivateIdentifier(endpoint.ScopeParamName)
			ns.ScopeFieldGoName = goutil.GoPublicIdentifier(endpoint.ScopeParamName)
		}

		converted := convertEndpoint(endpoint, ns.IsScopedNS, ns.ScopeParamName, goutil.GoPublicIdentifier(nsName))
		if endpointUsesTypes(converted) {
			ns.RequiresTypes = true
		}
		ns.Endpoints = append(ns.Endpoints, converted)
	}

	namespaces := make([]NamespaceInfo, 0, len(namespaceMap))
	for _, ns := range namespaceMap {
		sort.Slice(ns.Endpoints, func(i, j int) bool {
			return ns.Endpoints[i].MethodName < ns.Endpoints[j].MethodName
		})
		for _, ep := range ns.Endpoints {
			if ep.HasQueryParams || (ep.HasScalarArgs && ep.HTTPMethod == "GET") {
				ns.NeedsURLPkg = true
			}
			if ep.HasFileUpload || ep.HasEncryptedBody || ep.HasQueryParams {
				ns.NeedsRuntimePkg = true
			} else if ep.HasScalarArgs && ep.HTTPMethod == "GET" {
				// runtime.AddQueryParam is only called for non-array scalar args;
				// array args use strings.Join (comma-separated format).
				for _, arg := range ep.ScalarArgs {
					if !arg.IsArray {
						ns.NeedsRuntimePkg = true
						break
					}
				}
			}
			if len(ep.PathArgs) > 0 {
				ns.NeedsFmtPkg = true
			}
			if ep.HasScalarArgs && ep.HTTPMethod == "GET" {
				for _, arg := range ep.ScalarArgs {
					if arg.IsArray {
						ns.NeedsStringsPkg = true
						ns.NeedsFmtPkg = true
						break
					}
				}
			}
			if endpointNeedsRegexp(ep) {
				ns.NeedsRegexpPkg = true
			}
		}
		namespaces = append(namespaces, *ns)
	}
	sort.Slice(namespaces, func(i, j int) bool {
		return namespaces[i].Name < namespaces[j].Name
	})
	output.Namespaces = namespaces

	for _, ep := range apiOutput.Endpoints {
		if ep.Filterable {
			output.HasFilterableEndpoints = true
			break
		}
	}

	return output, nil
}

func convertEndpoint(ep apigen.EndpointInfo, isScopedNS bool, scopeParamName string, nsPrefix string) EndpointInfo {
	sdkPath := ep.Path
	sdkHTTPMethod := ep.Method
	pathFormat, pathArgs := convertPathToGoFormat(sdkPath, isScopedNS, scopeParamName)

	pathParams := make([]PathParam, 0, len(ep.PathParams))
	for _, param := range ep.PathParams {
		if isScopedNS && param.Name == scopeParamName {
			continue
		}
		pathParams = append(pathParams, PathParam{
			Name:   param.Name,
			GoName: goutil.GoPublicIdentifier(param.Name),
			GoType: mapScalarToGo(param),
		})
	}

	queryParams := make([]QueryParam, 0, len(ep.QueryParams))
	for _, param := range ep.QueryParams {
		queryParams = append(queryParams, QueryParam{
			Name:              param.Name,
			GoName:            goutil.GoPublicIdentifier(param.Name),
			GoType:            mapScalarToGo(param),
			Required:          param.Required,
			Pointer:           !param.Required,
			ValidateMin:       param.ValidateMin,
			ValidateMax:       param.ValidateMax,
			ValidateMinLength: param.ValidateMinLength,
			ValidateMaxLength: param.ValidateMaxLength,
			ValidateListMin:   param.ValidateListMin,
			ValidateListMax:   param.ValidateListMax,
			ValidatePattern:   param.ValidatePattern,
		})
	}
	pathArgs = qualifyGoPathArgs(pathArgs, pathParams, queryParams)

	scalarArgs := make([]ScalarArg, 0, len(ep.ScalarArgs))
	for _, arg := range ep.ScalarArgs {
		goType := qualifyType(arg.Type)
		isArray := arg.IsArray
		pointer := !arg.Required
		if isArray {
			goType = "[]" + goType
			pointer = false // slices are nil-able, no pointer needed
		}
		scalarArgs = append(scalarArgs, ScalarArg{
			Name:              arg.Name,
			GoName:            goutil.GoPublicIdentifier(arg.Name),
			GoType:            goType,
			Required:          arg.Required,
			Pointer:           pointer,
			IsArray:           isArray,
			ValidateMin:       arg.ValidateMin,
			ValidateMax:       arg.ValidateMax,
			ValidateMinLength: arg.ValidateMinLength,
			ValidateMaxLength: arg.ValidateMaxLength,
			ValidateListMin:   arg.ValidateListMin,
			ValidateListMax:   arg.ValidateListMax,
			ValidatePattern:   arg.ValidatePattern,
		})
	}

	fileUploadFields := make([]FileUploadField, 0, len(ep.FileUploadFields))
	for _, field := range ep.FileUploadFields {
		fileUploadFields = append(fileUploadFields, FileUploadField{
			Name:     field.Name,
			GoName:   goutil.GoPublicIdentifier(field.Name),
			Required: field.Required,
		})
	}

	methodName := goutil.GoPublicIdentifier(ep.Name)
	hasEncryptedBody := ep.Encrypted && isBodyMethod(sdkHTTPMethod)

	return EndpointInfo{
		MethodName:            methodName,
		HTTPMethod:            strings.ToUpper(sdkHTTPMethod),
		PathFormat:            pathFormat,
		PathArgs:              pathArgs,
		InputType:             qualifyType(ep.InputType),
		OutputType:            ep.OutputType,
		OutputGoType:          outputType(ep.OutputType, ep.OutputIsArray),
		HasInput:              ep.HasInput,
		RequiresAuth:          ep.RequiresAuth,
		PathParams:            pathParams,
		QueryParams:           queryParams,
		ScalarArgs:            scalarArgs,
		HasScalarArgs:         len(scalarArgs) > 0,
		ScalarInputStructName: nsPrefix + methodName + "Input",
		QueryStructName:       nsPrefix + methodName + "QueryParams",
		HasQueryParams:        len(queryParams) > 0,
		Description:           ep.Description,
		Encrypted:             ep.Encrypted,
		HasEncryptedBody:      hasEncryptedBody,
		HasFileUpload:         ep.HasFileUpload,
		FileUploadFields:      fileUploadFields,
		FilesStructName:       nsPrefix + methodName + "Files",
		Filterable:            ep.Filterable,
	}
}

func outputType(typeName string, isArray bool) string {
	base := qualifyType(typeName)
	if isArray {
		return "[]" + base
	}
	return base
}

func isBodyMethod(method string) bool {
	switch strings.ToUpper(method) {
	case "POST", "PUT", "PATCH":
		return true
	default:
		return false
	}
}

var pathParamPattern = regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)

func convertPathToGoFormat(path string, isScopedNS bool, scopeParamName string) (string, []string) {
	matches := pathParamPattern.FindAllStringSubmatch(path, -1)
	if len(matches) == 0 {
		return path, nil
	}

	pathFormat := path
	args := make([]string, 0, len(matches))
	for _, match := range matches {
		paramName := match[1]
		pathFormat = strings.Replace(pathFormat, "{"+paramName+"}", "%v", 1)

		if isScopedNS && paramName == scopeParamName {
			args = append(args, "n."+goutil.GoPrivateIdentifier(paramName))
			continue
		}
		args = append(args, goutil.GoPublicIdentifier(paramName))
	}

	return pathFormat, args
}

func qualifyGoPathArgs(args []string, pathParams []PathParam, queryParams []QueryParam) []string {
	pathParamNames := make(map[string]struct{}, len(pathParams))
	for _, param := range pathParams {
		pathParamNames[param.GoName] = struct{}{}
	}
	queryParamNames := make(map[string]QueryParam, len(queryParams))
	for _, param := range queryParams {
		queryParamNames[param.GoName] = param
	}

	qualified := make([]string, len(args))
	for i, arg := range args {
		if strings.HasPrefix(arg, "n.") {
			qualified[i] = arg
			continue
		}
		if _, ok := pathParamNames[arg]; ok {
			qualified[i] = arg
			continue
		}
		if param, ok := queryParamNames[arg]; ok {
			if param.Pointer {
				qualified[i] = "*query." + arg
			} else {
				qualified[i] = "query." + arg
			}
			continue
		}
		qualified[i] = arg
	}
	return qualified
}

// WriteSDK writes the generated Go SDK to files.
func WriteSDK(output *SDKOutput, outputDir, typesDir string, clock codegen.Clock) error {
	return WriteSDKWithTools(output, nil, outputDir, typesDir, clock)
}

// WriteSDKWithTools writes the generated Go SDK and optional tool artifacts to files.
func WriteSDKWithTools(output *SDKOutput, apiOutput *apigen.APIOutput, outputDir, typesDir string, clock codegen.Clock) error {
	return WriteSDKWithToolsProfiled(output, apiOutput, outputDir, typesDir, clock, nil, false)
}

// WriteSDKWithToolsProfiled writes the generated Go SDK and optional tool artifacts with shared codegen profiling.
func WriteSDKWithToolsProfiled(output *SDKOutput, apiOutput *apigen.APIOutput, outputDir, typesDir string, clock codegen.Clock, prof *profile.Profiler, skipFormat bool, phasePrefixes ...string) error {
	dirs := []string{
		outputDir,
		filepath.Join(outputDir, "namespaces"),
		filepath.Join(outputDir, "runtime"),
	}
	if apiOutput != nil && len(apiOutput.Endpoints) > 0 {
		dirs = append(dirs, filepath.Join(outputDir, "tools"))
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	output.TypesReplacePath = ""
	output.TypeModuleReplaces = nil
	if typesDir != "" {
		relPath, err := filepath.Rel(outputDir, typesDir)
		if err == nil {
			output.TypesReplacePath = filepath.ToSlash(relPath)
		}
		replaces, err := typeModuleReplacesFromGoMod(typesDir, outputDir, output.TypesModule)
		if err != nil {
			return fmt.Errorf("failed to read type module replaces from %s: %w", typesDir, err)
		}
		output.TypeModuleReplaces = replaces

		unionTypes, err := loadUnionTypeNames(typesDir)
		if err != nil {
			return fmt.Errorf("failed to read union metadata from %s: %w", typesDir, err)
		}
		if len(unionTypes) > 0 {
			for namespaceIndex := range output.Namespaces {
				for endpointIndex := range output.Namespaces[namespaceIndex].Endpoints {
					endpoint := &output.Namespaces[namespaceIndex].Endpoints[endpointIndex]
					outputType := normalizeTypeName(endpoint.OutputType)
					if _, ok := unionTypes[outputType]; !ok {
						continue
					}
					endpoint.IsUnionOutput = true
					endpoint.OutputUnionWrapperGoType = "types." + outputType + "Wrapper"
				}
			}
		}
	}

	customFuncs := customTemplateFuncs()
	renderer := codegen.NewFileGenerator(
		templatesFS,
		customFuncs,
		codegen.WithProfiler(prof, phasePrefixes...),
		codegen.WithSkipFormat(skipFormat),
	)
	if err := generateFile(renderer, "module.tmpl", filepath.Join(outputDir, "go.mod"), output, customFuncs); err != nil {
		return fmt.Errorf("failed to generate go.mod: %w", err)
	}
	if err := generateFile(renderer, "types.tmpl", filepath.Join(outputDir, "types.go"), output, customFuncs); err != nil {
		return fmt.Errorf("failed to generate types.go: %w", err)
	}
	if err := generateFile(renderer, "runtime.tmpl", filepath.Join(outputDir, "runtime", "runtime.go"), output, customFuncs); err != nil {
		return fmt.Errorf("failed to generate runtime/runtime.go: %w", err)
	}
	if err := generateFile(renderer, "errors.tmpl", filepath.Join(outputDir, "errors.go"), output, customFuncs); err != nil {
		return fmt.Errorf("failed to generate errors.go: %w", err)
	}
	if err := generateFile(renderer, "client.tmpl", filepath.Join(outputDir, "client.go"), output, customFuncs); err != nil {
		return fmt.Errorf("failed to generate client.go: %w", err)
	}
	if err := generateFile(renderer, "sdk.tmpl", filepath.Join(outputDir, "sdk.go"), output, customFuncs); err != nil {
		return fmt.Errorf("failed to generate sdk.go: %w", err)
	}
	if err := generateFile(renderer, "readme.tmpl", filepath.Join(outputDir, "README.md"), output, customFuncs); err != nil {
		return fmt.Errorf("failed to generate README.md: %w", err)
	}

	validationPath := filepath.Join(outputDir, "namespaces", "validation.go")
	if err := generateFile(renderer, "validation.tmpl", validationPath, output, customFuncs); err != nil {
		return fmt.Errorf("failed to generate namespaces/validation.go: %w", err)
	}

	commonPath := filepath.Join(outputDir, "namespaces", "common.go")
	if err := generateFile(renderer, "namespaces_common.tmpl", commonPath, output, customFuncs); err != nil {
		return fmt.Errorf("failed to generate namespaces/common.go: %w", err)
	}

	for _, ns := range output.Namespaces {
		needsFmt, needsURL, needsRuntime, needsTypes, needsStrings, needsRegexp, needsFilterparse := namespaceImportFlags(ns)
		namespaceOutput := struct {
			SDK              *SDKOutput
			Namespace        NamespaceInfo
			NeedsFmt         bool
			NeedsURL         bool
			NeedsRuntime     bool
			NeedsTypes       bool
			NeedsStrings     bool
			NeedsRegexp      bool
			NeedsFilterparse bool
		}{
			SDK:              output,
			Namespace:        ns,
			NeedsFmt:         needsFmt,
			NeedsURL:         needsURL,
			NeedsRuntime:     needsRuntime,
			NeedsTypes:       needsTypes,
			NeedsStrings:     needsStrings,
			NeedsRegexp:      needsRegexp,
			NeedsFilterparse: needsFilterparse,
		}
		namespacePath := filepath.Join(outputDir, "namespaces", strings.ToLower(ns.Name)+".go")
		if err := generateFile(renderer, "namespace.tmpl", namespacePath, namespaceOutput, customFuncs); err != nil {
			return fmt.Errorf("failed to generate namespace %s: %w", ns.Name, err)
		}
	}

	if apiOutput != nil && len(apiOutput.Endpoints) > 0 {
		typescriptSDKOutput, err := sdkgen.Generate(apiOutput, nil, clock)
		if err != nil {
			return fmt.Errorf("failed to derive shared tools metadata: %w", err)
		}
		toolsOutput, err := sdkgen.GenerateTools(typescriptSDKOutput, apiOutput, clock)
		if err != nil {
			return fmt.Errorf("failed to generate tools metadata: %w", err)
		}

		if toolsOutput != nil && len(toolsOutput.Tools) > 0 {
			toolsOutput.SchemaName = output.SchemaName
			toolsOutput.SDKClassName = output.SDKStructName

			toolsFuncs := toolsTemplateFuncs()
			toolsRenderer := codegen.NewFileGenerator(
				templatesFS,
				toolsFuncs,
				codegen.WithProfiler(prof, suffixProfilePrefixes(phasePrefixes, "tools")...),
				codegen.WithSkipFormat(skipFormat),
			)
			toolsDir := filepath.Join(outputDir, "tools")
			if err := generateFile(toolsRenderer, "tools-openai.tmpl", filepath.Join(toolsDir, "openai.json"), toolsOutput, toolsFuncs); err != nil {
				return fmt.Errorf("failed to generate tools/openai.json: %w", err)
			}
			if err := generateFile(toolsRenderer, "tools-anthropic.tmpl", filepath.Join(toolsDir, "anthropic.json"), toolsOutput, toolsFuncs); err != nil {
				return fmt.Errorf("failed to generate tools/anthropic.json: %w", err)
			}
			if err := generateFile(toolsRenderer, "tools-schema.tmpl", filepath.Join(toolsDir, "schema.json"), toolsOutput, toolsFuncs); err != nil {
				return fmt.Errorf("failed to generate tools/schema.json: %w", err)
			}
			if err := generateFile(toolsRenderer, "tools-mcp-audit.tmpl", filepath.Join(toolsDir, "mcp-audit.json"), toolsOutput, toolsFuncs); err != nil {
				return fmt.Errorf("failed to generate tools/mcp-audit.json: %w", err)
			}
			if err := generateFile(toolsRenderer, "tools-mcp-binding.tmpl", filepath.Join(toolsDir, "mcp-binding.json"), toolsOutput, toolsFuncs); err != nil {
				return fmt.Errorf("failed to generate tools/mcp-binding.json: %w", err)
			}
		}
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

func typeModuleReplacesFromGoMod(typesDir, sdkOutputDir, typesModule string) ([]ModuleReplace, error) {
	seen := map[string]struct{}{}
	if typesModule != "" {
		seen[typesModule] = struct{}{}
	}

	replaces, err := collectReplacesFromDir(typesDir, sdkOutputDir, seen)
	if err != nil {
		return nil, err
	}

	// Transitively collect replace directives from replaced modules so that
	// deeply-nested local dependencies (e.g. schema-ir -> ptr) are resolved
	// in the SDK's go.mod where Go does not propagate replaces.
	for i := 0; i < len(replaces); i++ {
		absDir := replaces[i].RelPath
		if !filepath.IsAbs(absDir) {
			absDir = filepath.Join(sdkOutputDir, absDir)
		}
		transitive, err := collectReplacesFromDir(filepath.Clean(absDir), sdkOutputDir, seen)
		if err != nil {
			continue
		}
		replaces = append(replaces, transitive...)
	}

	if len(replaces) == 0 {
		return nil, nil
	}

	sort.Slice(replaces, func(i, j int) bool {
		return replaces[i].Module < replaces[j].Module
	})

	return replaces, nil
}

// collectReplacesFromDir reads go.mod in dir and returns local replace
// directives re-relativized to sdkOutputDir, skipping modules already in seen.
func collectReplacesFromDir(dir, sdkOutputDir string, seen map[string]struct{}) ([]ModuleReplace, error) {
	goModBytes, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read go.mod in %s: %w", dir, err)
	}

	var replaces []ModuleReplace
	for _, line := range strings.Split(string(goModBytes), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "replace ") || !strings.Contains(line, "=>") {
			continue
		}

		parts := strings.SplitN(line, "=>", 2)
		if len(parts) != 2 {
			continue
		}

		modulePart := strings.TrimSpace(strings.TrimPrefix(parts[0], "replace "))
		moduleFields := strings.Fields(modulePart)
		if len(moduleFields) == 0 {
			continue
		}
		module := moduleFields[0]
		if _, exists := seen[module]; exists {
			continue
		}

		targetFields := strings.Fields(strings.TrimSpace(parts[1]))
		if len(targetFields) == 0 {
			continue
		}
		replacePath := targetFields[0]
		if !strings.HasPrefix(replacePath, ".") && !filepath.IsAbs(replacePath) {
			continue
		}

		targetDir := replacePath
		if !filepath.IsAbs(replacePath) {
			targetDir = filepath.Join(dir, replacePath)
		}

		relPath, err := filepath.Rel(sdkOutputDir, filepath.Clean(targetDir))
		if err != nil {
			continue
		}

		replaces = append(replaces, ModuleReplace{
			Module:  module,
			RelPath: filepath.ToSlash(relPath),
		})
		seen[module] = struct{}{}
	}

	return replaces, nil
}

func customTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"join": strings.Join,
		"docLine": func(value, fallback string) string {
			normalized := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
			if normalized == "" {
				normalized = fallback
			}
			return normalized
		},
		"hasQueryParamValidation": func(params []QueryParam) bool {
			for _, param := range params {
				if queryParamHasValidationConstraints(param) {
					return true
				}
			}
			return false
		},
		"escapeGoString": func(value string) string {
			escaped := strings.ReplaceAll(value, "\\", "\\\\")
			return strings.ReplaceAll(escaped, "\"", "\\\"")
		},
	}
}

func toolsTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"escapeJSON": func(s string) string {
			s = strings.ReplaceAll(s, "\\", "\\\\")
			s = strings.ReplaceAll(s, "\"", "\\\"")
			s = strings.ReplaceAll(s, "\n", "\\n")
			s = strings.ReplaceAll(s, "\r", "\\r")
			s = strings.ReplaceAll(s, "\t", "\\t")
			return s
		},
		"schemaJSON": toolsutil.JSONSchemaPropertyLiteral,
		"jsonValue":  toolsutil.JSONLiteral,
		"sortedKeys": func(m map[string]sdkgen.JSONSchemaProperty) []string {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return keys
		},
	}
}

func generateFile(generator *codegen.FileGenerator, templateName, outputPath string, data interface{}, customFuncs template.FuncMap) error {
	return generator.GenerateFile(
		codegen.NewGoFileConfig(templatesFS, templateName, outputPath, data, customFuncs),
	)
}

func qualifyType(typeName string) string {
	typeName = normalizeTypeName(typeName)
	switch typeName {
	case "", codegen.PrimitiveString:
		return "string"
	case codegen.PrimitiveNumber:
		return "float64"
	case codegen.PrimitiveBoolean:
		return "bool"
	default:
		return "types." + typeName
	}
}

func normalizeTypeName(typeName string) string {
	if typeName == "" {
		return typeName
	}
	if idx := strings.LastIndex(typeName, "."); idx >= 0 && idx+1 < len(typeName) {
		return typeName[idx+1:]
	}
	return typeName
}

// mapScalarToGo maps a path or query parameter to its Go SDK type, keeping
// the parameter's JSON shape: an integer scalar is int64, a number float64,
// a boolean bool, anything else a string. The URL encoding formats the value
// later; a tool call's JSON arguments decode into these fields first, so a
// numeric argument needs a numeric field.
func mapScalarToGo(param apigen.Param) string {
	switch {
	case param.IsInt:
		return "int64"
	case param.IsFloat || param.Type == codegen.PrimitiveNumber:
		return "float64"
	case param.IsBool || param.Type == codegen.PrimitiveBoolean:
		return "bool"
	default:
		return "string"
	}
}

func namespaceImportFlags(ns NamespaceInfo) (needsFmt, needsURL, needsRuntime, needsTypes, needsStrings, needsRegexp, needsFilterparse bool) {
	needsTypes = ns.RequiresTypes
	for _, ep := range ns.Endpoints {
		if len(ep.PathArgs) > 0 {
			needsFmt = true
		}
		if ep.IsUnionOutput {
			needsFmt = true
		}
		if ep.HasFileUpload {
			needsRuntime = true
			for _, f := range ep.FileUploadFields {
				if f.Required {
					needsFmt = true
				}
			}
		}
		if ep.HasQueryParams || (ep.HasScalarArgs && ep.HTTPMethod == "GET") {
			needsURL = true
			for _, arg := range ep.ScalarArgs {
				if arg.IsArray {
					needsStrings = true
					needsFmt = true
				} else {
					// Non-array scalar args on GET use runtime.AddQueryParam.
					needsRuntime = true
				}
			}
			if ep.HasQueryParams {
				needsRuntime = true
			}
		}
		if ep.HasEncryptedBody {
			needsRuntime = true
		}
		if ep.HasInput || ep.HasScalarArgs {
			needsTypes = true
		}
		if endpointNeedsRegexp(ep) {
			needsRegexp = true
		}
		if ep.Filterable {
			needsFilterparse = true
			needsURL = true
		}
	}
	return
}

func endpointUsesTypes(endpoint EndpointInfo) bool {
	if endpointHasValidationConstraints(endpoint) {
		return true
	}
	if strings.Contains(endpoint.InputType, "types.") {
		return true
	}
	if strings.Contains(endpoint.OutputGoType, "types.") {
		return true
	}
	for _, arg := range endpoint.ScalarArgs {
		if strings.Contains(arg.GoType, "types.") {
			return true
		}
	}
	return false
}

func endpointHasValidationConstraints(endpoint EndpointInfo) bool {
	for _, queryParam := range endpoint.QueryParams {
		if queryParamHasValidationConstraints(queryParam) {
			return true
		}
	}
	for _, scalarArg := range endpoint.ScalarArgs {
		if scalarArgHasValidationConstraints(scalarArg) {
			return true
		}
	}
	return false
}

func endpointNeedsRegexp(endpoint EndpointInfo) bool {
	for _, queryParam := range endpoint.QueryParams {
		if queryParam.ValidatePattern != "" {
			return true
		}
	}
	for _, scalarArg := range endpoint.ScalarArgs {
		if scalarArg.ValidatePattern != "" {
			return true
		}
	}
	return false
}

func queryParamHasValidationConstraints(param QueryParam) bool {
	return param.ValidateMin != nil ||
		param.ValidateMax != nil ||
		param.ValidateMinLength != nil ||
		param.ValidateMaxLength != nil ||
		param.ValidateListMin != nil ||
		param.ValidateListMax != nil ||
		param.ValidatePattern != ""
}

func scalarArgHasValidationConstraints(arg ScalarArg) bool {
	return arg.ValidateMin != nil ||
		arg.ValidateMax != nil ||
		arg.ValidateMinLength != nil ||
		arg.ValidateMaxLength != nil ||
		arg.ValidateListMin != nil ||
		arg.ValidateListMax != nil ||
		arg.ValidatePattern != ""
}

func loadUnionTypeNames(typesDir string) (map[string]struct{}, error) {
	unionsPath := filepath.Join(typesDir, "unions.go")
	content, err := os.ReadFile(unionsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read unions.go: %w", err)
	}

	interfacePattern := regexp.MustCompile(`(?m)^type\s+([A-Za-z0-9_]+)\s+interface\s+\{`)
	wrapperPattern := regexp.MustCompile(`(?m)^type\s+([A-Za-z0-9_]+)Wrapper\s+struct\s+\{`)

	interfaces := interfacePattern.FindAllStringSubmatch(string(content), -1)
	wrappers := wrapperPattern.FindAllStringSubmatch(string(content), -1)
	if len(interfaces) == 0 || len(wrappers) == 0 {
		return nil, nil
	}

	wrapperSet := make(map[string]struct{}, len(wrappers))
	for _, match := range wrappers {
		if len(match) < 2 {
			continue
		}
		wrapperSet[match[1]] = struct{}{}
	}

	unionTypes := make(map[string]struct{})
	for _, match := range interfaces {
		if len(match) < 2 {
			continue
		}
		unionTypeName := match[1]
		if _, ok := wrapperSet[unionTypeName]; ok {
			unionTypes[unionTypeName] = struct{}{}
		}
	}
	if len(unionTypes) == 0 {
		return nil, nil
	}

	return unionTypes, nil
}
