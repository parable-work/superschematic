// Package tsrestgen generates TypeScript REST API server packages from
// API-kind schemas. The generated package (Naming.NpmAPIPackage,
// <scope>/<svc>-api) carries one implementation interface per operation
// class, a Hono router built on the TypeScript HTTP runtime
// (Naming.HTTPRuntimeNpmPackage), and the OpenAPI document.
//
// It is the TypeScript sibling of the Go and Rust API generators: endpoints
// come from the shared apigen extraction, so path, method, parameter
// placement, auth decorators and body limits agree with the Go and Rust
// routers by construction. The router is provider-neutral: the operation
// table declares each route's auth requirement, and the service supplies the
// authenticator that establishes the caller.
package tsrestgen

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

const (
	// HonoVersion pins the Hono the generated package is checked against; it
	// must match the runtime's peer range.
	HonoVersion = "4.13.8"
	// TypeScriptVersion pins the compiler the generated package is checked with.
	TypeScriptVersion = "5.9.3"
)

// ParamInfo is one path, query, or body argument as the generated
// TypeScript sees it.
type ParamInfo struct {
	Name   string // wire name
	TSName string // argument key in the implementation's args object
	TSType string // TypeScript type of the decoded value
	// Kind is the runtime ParamKind. A body argument of an object type (T,
	// T[] or T[][]) has Kind "object": the runtime reads it from its JSON
	// value and parses each value with the generated strict parser of T.
	Kind     string
	Required bool
	IsArray  bool
	// IsArrayOfArrays marks a T[][] body argument (IsArray is also set). The
	// runtime decodes it from the JSON body with the list rules: an inner
	// list is never null and each element is checked at name[i][j].
	IsArrayOfArrays bool
	// SpecLiteral is the ParamSpec object literal the router carries.
	SpecLiteral string
}

// EndpointInfo is one generated route.
type EndpointInfo struct {
	Name              string // operation name (camelCase, the schema's)
	Namespace         string // kebab-case namespace
	NamespaceProperty string // camelCase key in Implementations
	ArgsTypeName      string // <Op>Args
	Method            string
	Path              string // /api/... with {param} placeholders
	Description       string
	Title             string

	HasInput  bool
	InputType string
	// InputValidator is parse<Input>Json: the generated decoder that
	// validates the wire object (strict for @strictJSON contracts);
	// InputParser is parse<Input>FromJSON, the wire-to-runtime conversion
	// (dates). The router applies the validator, then the parser.
	InputValidator string
	InputParser    string
	InputRequired  bool

	OutputType string // TypeScript type expression of the result
	// OutputIsArrayOfArrays marks a T[][] result; the runtime sends a
	// nullish inner list as [].
	OutputIsArrayOfArrays bool

	PathParams  []ParamInfo
	QueryParams []ParamInfo
	BodyParams  []ParamInfo

	PublicRoute             bool
	RequiresAuth            bool
	RequiredPerms           []string
	PermsLiteral            string
	ManualRouteRegistration bool
	BodyLimitBytes          *int
	// RateLimitPerMinute and TimeoutSeconds are @rateLimit and @timeout
	// (set-level defaults with operation overrides, resolved by apigen);
	// the adapter applies them with the Go runtime's semantics.
	RateLimitPerMinute *int
	TimeoutSeconds     *int

	// DocLines is the JSDoc body of the implementation method: title,
	// description, route, and the auth requirement.
	DocLines []string
}

// HasArgs reports whether the implementation method takes any argument.
func (e EndpointInfo) HasArgs() bool {
	return e.HasInput || len(e.PathParams) > 0 || len(e.QueryParams) > 0 || len(e.BodyParams) > 0
}

// NamespaceInfo groups the implementable (non-manual) endpoints of one
// operation class.
type NamespaceInfo struct {
	Name          string // kebab-case
	InterfaceName string // <Namespace>Implementation
	Property      string // camelCase key in Implementations
	Endpoints     []EndpointInfo
}

// PackageImport lists the symbols the generated code imports from one package.
type PackageImport struct {
	Package string
	Symbols []string
}

// APIOutput is the template data for the generated package.
type APIOutput struct {
	SchemaName   string
	PackageName  string
	TypesPackage string
	// RuntimePackage is the TypeScript HTTP runtime the router is built on
	// (Naming.HTTPRuntimeNpmPackage); ScalarPackage is the scalar library
	// subpath scalar wire types come from, the one tsgen re-exports.
	RuntimePackage string
	ScalarPackage  string
	Author         string
	HonoVersion    string
	TSVersion      string
	Timestamp      string

	Namespaces      []NamespaceInfo
	Endpoints       []EndpointInfo // every endpoint, manual ones included
	ManualEndpoints []EndpointInfo

	// TypeImports are the type-only imports of interfaces.ts from
	// `<package>/types` (inputs, outputs, enum parameters);
	// RouterTypeImports the subset router.ts references (inputs and enum
	// parameters, the casts around the decoded request);
	// ValidatorImports are value imports from `<package>/validators`;
	// ScalarImports are type-only imports from ScalarPackage.
	TypeImports       []PackageImport
	RouterTypeImports []PackageImport
	ValidatorImports  []PackageImport
	ScalarImports     []string
	// PeerPackages lists every generated types package the generated
	// code imports from, for package.json peerDependencies.
	PeerPackages []string

	OpenAPISpecRaw string
}

// HasManualRoutes reports whether any operation is @manualRouteRegistration.
func (o *APIOutput) HasManualRoutes() bool {
	return len(o.ManualEndpoints) > 0
}

// Options configures TypeScript REST API generation.
type Options struct {
	SchemaName string
	// Dependencies are the loaded dependency schemas, used to find the
	// package that owns an imported input or output type.
	Dependencies map[string]*ir.Schema
	// Naming supplies the npm scope, the runtime and scalar package names
	// and the package author. Empty fields fall back to naming.Default().
	Naming naming.Naming
	Clock  codegen.Clock
}

// Generate produces the TypeScript API package metadata from the endpoints
// apigen extracted (the same APIOutput the Go server and the SDKs read, so
// the OpenAPI document carries every registered hook's edits). Returns
// (nil, nil) when the schema declares no operations.
func Generate(schema *ir.Schema, apiOutput *apigen.APIOutput, opts Options) (*APIOutput, error) {
	if opts.Clock == nil {
		opts.Clock = codegen.DefaultClock()
	}
	opts.Naming = opts.Naming.OrDefault()
	if apiOutput == nil || len(apiOutput.Endpoints) == 0 {
		return nil, nil
	}

	b := newBuilder(schema, opts)
	output := &APIOutput{
		SchemaName:     opts.SchemaName,
		PackageName:    opts.Naming.NpmAPIPackage(opts.SchemaName),
		TypesPackage:   opts.Naming.NpmTypesPackage(opts.SchemaName),
		RuntimePackage: opts.Naming.HTTPRuntimeNpmPackage,
		ScalarPackage:  opts.Naming.ScalarNpmPackage + "/scalars",
		Author:         opts.Naming.PackageAuthor,
		HonoVersion:    HonoVersion,
		TSVersion:      TypeScriptVersion,
		Timestamp:      opts.Clock.RFC3339(),
		OpenAPISpecRaw: apiOutput.OpenAPISpecRaw,
	}

	byNamespace := map[string]*NamespaceInfo{}
	for _, ep := range apiOutput.Endpoints {
		endpoint, err := b.endpoint(ep)
		if err != nil {
			return nil, err
		}
		output.Endpoints = append(output.Endpoints, endpoint)
		if endpoint.ManualRouteRegistration {
			output.ManualEndpoints = append(output.ManualEndpoints, endpoint)
			continue
		}
		ns, ok := byNamespace[endpoint.Namespace]
		if !ok {
			ns = &NamespaceInfo{
				Name:          endpoint.Namespace,
				InterfaceName: codegen.ToPascalCase(endpoint.Namespace) + "Implementation",
				Property:      endpoint.NamespaceProperty,
			}
			byNamespace[endpoint.Namespace] = ns
		}
		ns.Endpoints = append(ns.Endpoints, endpoint)
	}
	for _, name := range sortedKeys(byNamespace) {
		output.Namespaces = append(output.Namespaces, *byNamespace[name])
	}

	output.TypeImports = b.packageImports(b.typeImports)
	output.RouterTypeImports = b.packageImports(b.routerTypeImports)
	output.ValidatorImports = b.packageImports(b.validatorImports)
	output.ScalarImports = sortedKeys(b.scalarImports)
	peers := map[string]struct{}{}
	for pkg := range b.typeImports {
		peers[pkg] = struct{}{}
	}
	for pkg := range b.validatorImports {
		peers[pkg] = struct{}{}
	}
	peers[output.TypesPackage] = struct{}{}
	output.PeerPackages = sortedKeys(peers)

	return output, nil
}

type builder struct {
	schema   *ir.Schema
	opts     Options
	depNames []string

	typeImports       map[string]map[string]struct{}
	routerTypeImports map[string]map[string]struct{}
	validatorImports  map[string]map[string]struct{}
	scalarImports     map[string]struct{}
}

func newBuilder(schema *ir.Schema, opts Options) *builder {
	depNames := make([]string, 0, len(opts.Dependencies))
	for name := range opts.Dependencies {
		depNames = append(depNames, name)
	}
	sort.Strings(depNames)
	return &builder{
		schema:            schema,
		opts:              opts,
		depNames:          depNames,
		typeImports:       map[string]map[string]struct{}{},
		routerTypeImports: map[string]map[string]struct{}{},
		validatorImports:  map[string]map[string]struct{}{},
		scalarImports:     map[string]struct{}{},
	}
}

func (b *builder) endpoint(ep apigen.EndpointInfo) (EndpointInfo, error) {
	endpoint := EndpointInfo{
		Name:                    tsutil.ToCamelCase(ep.Name),
		Namespace:               ep.Namespace,
		NamespaceProperty:       codegen.ToCamelCase(ep.Namespace),
		ArgsTypeName:            codegen.ToPascalCase(ep.Name) + "Args",
		Method:                  strings.ToUpper(ep.Method),
		Path:                    ep.Path,
		Description:             ep.Description,
		Title:                   ep.Title,
		HasInput:                ep.HasInput,
		InputType:               ep.InputType,
		InputRequired:           ep.InputRequired,
		PublicRoute:             ep.PublicRoute,
		RequiresAuth:            ep.RequiresAuth,
		RequiredPerms:           append([]string(nil), ep.RequiredPerms...),
		PermsLiteral:            tsStringList(ep.RequiredPerms),
		ManualRouteRegistration: ep.ManualRouteRegistration,
	}
	if ep.BodyLimit != nil {
		bytes := *ep.BodyLimit * 1024 * 1024
		endpoint.BodyLimitBytes = &bytes
	}
	if ep.RateLimit != nil {
		rateLimit := *ep.RateLimit
		endpoint.RateLimitPerMinute = &rateLimit
	}
	if ep.Timeout != nil {
		timeout := *ep.Timeout
		endpoint.TimeoutSeconds = &timeout
	}
	if ep.HasFileUpload && !ep.ManualRouteRegistration {
		return endpoint, fmt.Errorf("tsrestgen: operation %s.%s uploads files; the TypeScript router has no multipart adapter, declare it @manualRouteRegistration", ep.Namespace, ep.Name)
	}

	if ep.HasInput {
		pkg, ok := b.ownerPackage(ep.InputType)
		if !ok {
			return endpoint, fmt.Errorf("tsrestgen: operation %s.%s input type %s is not declared by the schema or its dependencies", ep.Namespace, ep.Name, ep.InputType)
		}
		endpoint.InputValidator = "parse" + ep.InputType + "Json"
		endpoint.InputParser = "parse" + ep.InputType + "FromJSON"
		b.addImport(b.typeImports, pkg, ep.InputType)
		b.addImport(b.routerTypeImports, pkg, ep.InputType)
		b.addImport(b.validatorImports, pkg, endpoint.InputValidator)
		b.addImport(b.validatorImports, pkg, endpoint.InputParser)
	}

	outputType, err := b.outputType(ep)
	if err != nil {
		return endpoint, fmt.Errorf("tsrestgen: operation %s.%s: %w", ep.Namespace, ep.Name, err)
	}
	endpoint.OutputType = outputType
	endpoint.OutputIsArrayOfArrays = ep.OutputIsArrayOfArrays
	endpoint.DocLines = docLines(endpoint)

	// b.param fails only for a body argument whose type it cannot decode;
	// the error names the argument.
	param := func(p apigen.Param, place paramPlace) (ParamInfo, error) {
		info, err := b.param(p, place)
		switch {
		case err == nil:
			return info, nil
		case p.IsArrayOfArrays:
			return info, fmt.Errorf("tsrestgen does not support arrays of arrays yet (%s.%s(%s)): %w", ep.Namespace, ep.Name, p.Name, err)
		default:
			return info, fmt.Errorf("tsrestgen cannot decode body argument %s.%s(%s): %w", ep.Namespace, ep.Name, p.Name, err)
		}
	}
	for _, p := range ep.PathParams {
		info, err := param(p, inPath)
		if err != nil {
			return endpoint, err
		}
		endpoint.PathParams = append(endpoint.PathParams, info)
	}
	for _, p := range ep.QueryParams {
		info, err := param(p, inQuery)
		if err != nil {
			return endpoint, err
		}
		endpoint.QueryParams = append(endpoint.QueryParams, info)
	}
	// Undecorated scalar arguments travel in the query string on GET (the Go
	// router's rule) and as fields of the JSON body object otherwise.
	scalarArgPlace := inBody
	if endpoint.Method == "GET" {
		scalarArgPlace = inQuery
	}
	for _, p := range ep.ScalarArgs {
		info, err := param(p, scalarArgPlace)
		if err != nil {
			return endpoint, err
		}
		if scalarArgPlace == inQuery {
			endpoint.QueryParams = append(endpoint.QueryParams, info)
		} else {
			endpoint.BodyParams = append(endpoint.BodyParams, info)
		}
	}
	return endpoint, nil
}

// paramPlace is where a parameter travels: the path, the query string, or
// a field of the JSON body object.
type paramPlace int

const (
	inPath paramPlace = iota
	inQuery
	inBody
)

// param resolves the runtime kind, the TypeScript type, and the ParamSpec
// literal of one parameter from apigen's parse flags. A body argument
// arrives as a JSON value, so its type may also be an object type, alone
// or as the element of T[] or T[][] (apigen admits an array of arrays only
// in the body); the runtime parses each value with the generated strict
// parser of that type. param fails for a body argument of any other type
// that is not a scalar or an enum: a union has no parser.
func (b *builder) param(p apigen.Param, place paramPlace) (ParamInfo, error) {
	info := ParamInfo{
		Name:            p.Name,
		TSName:          tsutil.ToCamelCase(p.Name),
		Required:        p.Required || place == inPath,
		IsArray:         p.IsArray,
		IsArrayOfArrays: p.IsArrayOfArrays,
	}
	var enumValues []string
	var valueParser string
	switch {
	case p.IsInt:
		info.Kind, info.TSType = "integer", "number"
	case p.IsFloat:
		info.Kind, info.TSType = "number", "number"
	case p.IsBool:
		info.Kind, info.TSType = "boolean", "boolean"
	case p.IsUUID:
		info.Kind, info.TSType = "uuid", b.scalarType(p.Type)
	case p.IsDateTime:
		info.Kind, info.TSType = "datetime", "Date"
	case p.IsString:
		info.Kind, info.TSType = "string", "string"
	default:
		if enumDef, pkg, ok := b.findEnum(p.Type); ok {
			info.Kind = "enum"
			info.TSType = enumDef.Name
			b.addImport(b.typeImports, pkg, enumDef.Name)
			b.addImport(b.routerTypeImports, pkg, enumDef.Name)
			for _, value := range enumDef.Values {
				serialized := value.SerializedAs
				if serialized == "" {
					serialized = value.Name
				}
				enumValues = append(enumValues, serialized)
			}
		} else if _, isScalar := b.findScalar(p.Type); isScalar || place != inBody {
			info.Kind, info.TSType = "string", b.scalarType(p.Type)
		} else {
			pkg, ok := b.ownerPackage(p.Type)
			if !ok {
				what := "type"
				if p.IsArray {
					what = "element type"
				}
				return info, fmt.Errorf("%s %s is not a scalar, enum or object type", what, p.Type)
			}
			info.Kind, info.TSType = "object", p.Type
			b.addImport(b.typeImports, pkg, p.Type)
			b.addImport(b.routerTypeImports, pkg, p.Type)
			validator, parser := "parse"+p.Type+"Json", "parse"+p.Type+"FromJSON"
			b.addImport(b.validatorImports, pkg, validator)
			b.addImport(b.validatorImports, pkg, parser)
			valueParser = fmt.Sprintf("(value: unknown) => %s(%s(value))", parser, validator)
		}
	}
	info.TSType = tsListType(info.TSType, p.ArrayDepth())

	fields := []string{
		"name: " + tsString(p.Name),
		"kind: " + tsString(info.Kind),
		fmt.Sprintf("required: %t", info.Required),
	}
	if p.IsArray {
		fields = append(fields, "isArray: true")
	}
	if p.IsArrayOfArrays {
		fields = append(fields, "isArrayOfArrays: true")
	}
	if len(enumValues) > 0 {
		fields = append(fields, "enumValues: "+tsStringList(enumValues))
	}
	if p.DefaultValue != nil {
		fields = append(fields, "defaultValue: "+tsString(*p.DefaultValue))
	}
	if p.ValidateMin != nil {
		fields = append(fields, fmt.Sprintf("min: %v", *p.ValidateMin))
	}
	if p.ValidateMax != nil {
		fields = append(fields, fmt.Sprintf("max: %v", *p.ValidateMax))
	}
	if p.ValidateMinLength != nil {
		fields = append(fields, fmt.Sprintf("minLength: %d", *p.ValidateMinLength))
	}
	if p.ValidateMaxLength != nil {
		fields = append(fields, fmt.Sprintf("maxLength: %d", *p.ValidateMaxLength))
	}
	if p.ValidateListMin != nil {
		fields = append(fields, fmt.Sprintf("listMin: %d", *p.ValidateListMin))
	}
	if p.ValidateListMax != nil {
		fields = append(fields, fmt.Sprintf("listMax: %d", *p.ValidateListMax))
	}
	if p.ValidatePattern != "" {
		fields = append(fields, "pattern: "+tsString(p.ValidatePattern))
	}
	if valueParser != "" {
		fields = append(fields, "parse: "+valueParser)
	}
	info.SpecLiteral = "{ " + strings.Join(fields, ", ") + " }"
	return info, nil
}

// tsListType wraps a TypeScript element type in depth list levels: "T",
// "T[]" or "T[][]".
func tsListType(elem string, depth int) string {
	return codegen.WrapArray(elem, depth, func(inner string) string { return inner + "[]" })
}

// scalarType returns the scalar library wire type symbol of a named scalar,
// recording the import; unknown names fall back to string.
func (b *builder) scalarType(typeName string) string {
	if _, ok := b.findScalar(typeName); !ok {
		return "string"
	}
	symbol := strings.TrimSpace(codegen.BuildScalarTokens(typeName).Symbol)
	if symbol == "" {
		return "string"
	}
	b.scalarImports[symbol] = struct{}{}
	return symbol
}

func (b *builder) outputType(ep apigen.EndpointInfo) (string, error) {
	name := ep.OutputType
	var tsType string
	switch {
	case name == "":
		return "void", nil
	case codegen.IsLanguagePrimitive(name):
		tsType = name
	default:
		if scalar, ok := b.findScalar(name); ok {
			traits := codegen.BuildScalarTraits(scalar, codegen.BuildScalarTokens(name), scalar.TypeMappings["typescript"])
			if traits.IsDateTimeLike {
				tsType = "Date"
			} else {
				tsType = b.scalarType(name)
			}
		} else if pkg, ok := b.ownerPackage(name); ok {
			b.addImport(b.typeImports, pkg, name)
			tsType = name
		} else {
			return "", fmt.Errorf("output type %s is not declared by the schema or its dependencies", name)
		}
	}
	return tsListType(tsType, ep.OutputArrayDepth()), nil
}

// ownerPackage returns the generated types package that declares a type or
// enum: the schema's own package, or a dependency's.
func (b *builder) ownerPackage(name string) (string, bool) {
	if _, ok := b.schema.Types[name]; ok {
		return b.opts.Naming.NpmTypesPackage(b.opts.SchemaName), true
	}
	if _, ok := b.schema.Enums[name]; ok {
		return b.opts.Naming.NpmTypesPackage(b.opts.SchemaName), true
	}
	for _, depName := range b.depNames {
		dep := b.opts.Dependencies[depName]
		if dep == nil {
			continue
		}
		if _, ok := dep.Types[name]; ok {
			return b.opts.Naming.NpmTypesPackage(depName), true
		}
		if _, ok := dep.Enums[name]; ok {
			return b.opts.Naming.NpmTypesPackage(depName), true
		}
	}
	return "", false
}

func (b *builder) findEnum(name string) (*ir.EnumDef, string, bool) {
	if enumDef, ok := b.schema.Enums[name]; ok {
		return enumDef, b.opts.Naming.NpmTypesPackage(b.opts.SchemaName), true
	}
	for _, depName := range b.depNames {
		dep := b.opts.Dependencies[depName]
		if dep == nil {
			continue
		}
		if enumDef, ok := dep.Enums[name]; ok {
			return enumDef, b.opts.Naming.NpmTypesPackage(depName), true
		}
	}
	return nil, "", false
}

func (b *builder) findScalar(name string) (*ir.ScalarDef, bool) {
	if scalar, ok := b.schema.Scalars[name]; ok {
		return scalar, true
	}
	for _, depName := range b.depNames {
		dep := b.opts.Dependencies[depName]
		if dep == nil {
			continue
		}
		if scalar, ok := dep.Scalars[name]; ok {
			return scalar, true
		}
	}
	return nil, false
}

func (b *builder) addImport(set map[string]map[string]struct{}, pkg, symbol string) {
	if set[pkg] == nil {
		set[pkg] = map[string]struct{}{}
	}
	set[pkg][symbol] = struct{}{}
}

func (b *builder) packageImports(set map[string]map[string]struct{}) []PackageImport {
	imports := make([]PackageImport, 0, len(set))
	for _, pkg := range sortedKeys(set) {
		imports = append(imports, PackageImport{Package: pkg, Symbols: sortedKeys(set[pkg])})
	}
	return imports
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// docLines assembles the JSDoc body of an implementation method.
func docLines(ep EndpointInfo) []string {
	var lines []string
	if ep.Title != "" {
		lines = append(lines, ep.Title)
	}
	if desc := strings.TrimSpace(ep.Description); desc != "" {
		lines = append(lines, strings.Split(desc, "\n")...)
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, ep.Method+" "+ep.Path)
	if ep.RateLimitPerMinute != nil {
		lines = append(lines, fmt.Sprintf("Rate limited to %d requests per minute per client.", *ep.RateLimitPerMinute))
	}
	if ep.TimeoutSeconds != nil {
		lines = append(lines, fmt.Sprintf("Times out after %d seconds (504).", *ep.TimeoutSeconds))
	}
	switch {
	case ep.PublicRoute:
		lines = append(lines, "Public route: no authentication.")
	case len(ep.RequiredPerms) > 0:
		lines = append(lines, "Requires a principal holding one of: "+strings.Join(ep.RequiredPerms, ", ")+".")
	case ep.RequiresAuth:
		lines = append(lines, "Requires an authenticated principal.")
	}
	return lines
}

// tsString renders a single-quoted TypeScript string literal.
func tsString(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", `\r`)
	return "'" + replacer.Replace(s) + "'"
}

func tsStringList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, tsString(value))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// WriteAPI writes the generated package into outputDir.
func WriteAPI(output *APIOutput, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir %s: %w", outputDir, err)
	}

	files := []struct {
		templateName string
		outputName   string
	}{
		{templateName: "package.tmpl", outputName: "package.json"},
		{templateName: "tsconfig.tmpl", outputName: "tsconfig.json"},
		{templateName: "index.tmpl", outputName: "index.ts"},
		{templateName: "interfaces.tmpl", outputName: "interfaces.ts"},
		{templateName: "router.tmpl", outputName: "router.ts"},
		{templateName: "readme.tmpl", outputName: "README.md"},
	}
	for _, file := range files {
		if err := generateFile(file.templateName, filepath.Join(outputDir, file.outputName), output); err != nil {
			return fmt.Errorf("generate %s: %w", file.outputName, err)
		}
	}

	if output.OpenAPISpecRaw != "" {
		if !json.Valid([]byte(output.OpenAPISpecRaw)) {
			return fmt.Errorf("openapi document for %s is not valid JSON", output.SchemaName)
		}
		if err := os.WriteFile(filepath.Join(outputDir, "openapi.json"), []byte(output.OpenAPISpecRaw), 0o644); err != nil {
			return fmt.Errorf("write openapi.json: %w", err)
		}
	}
	return nil
}

func generateFile(templateName, outputPath string, data any) error {
	return codegen.GenerateFile(
		codegen.NewFileConfig(templatesFS, templateName, outputPath, data, templateFuncs()),
	)
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"tsString":     tsString,
		"toPascalCase": codegen.ToPascalCase,
		"splitLines":   func(s string) []string { return strings.Split(strings.TrimSpace(s), "\n") },
		"join":         strings.Join,
		"sub":          func(a, b int) int { return a - b },
	}
}
