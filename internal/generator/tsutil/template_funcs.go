package tsutil

import (
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// BaseTemplateFuncs returns the template functions of the TypeScript
// generators: codegen's, none of which it overrides, so a name means the
// same in every generator's templates, and the TypeScript ones.
// joinCamelCase is ToCamelCase and toClassName ToClassName, the names the
// TypeScript SDK gives its members and classes; toCamelCase stays
// codegen.ToCamelCase. Generators can add their own functions by merging
// with this map.
func BaseTemplateFuncs() template.FuncMap {
	return codegen.MergeTemplateFuncs(codegen.BaseTemplateFuncs(), template.FuncMap{
		"joinCamelCase":     ToCamelCase,
		"toClassName":       ToClassName,
		"typescriptVersion": func() string { return TypeScriptVersion },
	})
}
