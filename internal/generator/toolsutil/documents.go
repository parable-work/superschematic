package toolsutil

import (
	"embed"
	"fmt"
	"path/filepath"
	"sort"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// Document is one JSON tool document an SDK writes under tools/. Every SDK
// writes it from the same template, so the TypeScript, Go and Rust SDKs
// describe a tool alike.
type Document struct {
	Template string // the template, under toolsutil/templates
	File     string // the file, under the SDK's tools directory
}

// The tool documents: the OpenAI and Anthropic tool lists, the generic JSON
// Schema, the MCP audit document and the MCP binding manifest.
var (
	OpenAIDocument     = Document{Template: "tools-openai.tmpl", File: "openai.json"}
	AnthropicDocument  = Document{Template: "tools-anthropic.tmpl", File: "anthropic.json"}
	SchemaDocument     = Document{Template: "tools-schema.tmpl", File: "schema.json"}
	MCPAuditDocument   = Document{Template: "tools-mcp-audit.tmpl", File: "mcp-audit.json"}
	MCPBindingDocument = Document{Template: "tools-mcp-binding.tmpl", File: "mcp-binding.json"}
)

// TemplateFuncs returns the template functions the tool documents use,
// which an SDK's own tool templates can extend.
func TemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"escapeJSON": EscapeJSON,
		"schemaJSON": JSONSchemaPropertyLiteral,
		"jsonValue":  JSONLiteral,
		"sortedKeys": func(m map[string]JSONSchemaProperty) []string {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return keys
		},
	}
}

// WriteDocuments writes docs into toolsDir from data, an SDK generator's
// tools output. The templates read its SDKClassName, SchemaName, APIID,
// Keys, Invocation, Tools and VisibleTools, and the MCP binding manifest
// its MCPBindingVersion and Timestamp and each tool's MCPBinding.
func WriteDocuments(toolsDir string, data any, docs []Document, opts ...codegen.FileGeneratorOption) error {
	generator := codegen.NewFileGenerator(templatesFS, TemplateFuncs(), opts...)
	for _, doc := range docs {
		path := filepath.Join(toolsDir, doc.File)
		if err := generator.GenerateFile(codegen.NewFileConfig(templatesFS, doc.Template, path, data, nil)); err != nil {
			return fmt.Errorf("failed to generate tools/%s: %w", doc.File, err)
		}
	}
	return nil
}
