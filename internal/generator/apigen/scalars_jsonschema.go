package apigen

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// ScalarJSONSchemaInfo contains JSON Schema metadata for a scalar type.
// SDK tool bindings use this when describing parameter shapes to LLMs.
type ScalarJSONSchemaInfo struct {
	Type        string
	Format      string
	Description string
	Pattern     string
	MinLength   *int
	MaxLength   *int
	Minimum     *int64
	Maximum     *int64
}

func extractScalarJSONSchemaInfo(schema *ir.Schema) map[string]ScalarJSONSchemaInfo {
	scalars := map[string]ScalarJSONSchemaInfo{
		codegen.PrimitiveString:  {Type: "string", Description: "A string value"},
		codegen.PrimitiveNumber:  {Type: "number", Description: "A numeric value"},
		codegen.PrimitiveBoolean: {Type: "boolean", Description: "A boolean value (true or false)"},
	}

	for name, scalarDef := range schema.Scalars {
		info := ScalarJSONSchemaInfo{
			Description: codegen.DocText(scalarDef.Description, scalarDef.Comment),
			Pattern:     scalarDef.Pattern,
		}

		if jsType, ok := scalarDef.TypeMappings["json_schema"]; ok {
			info.Type = jsType
		}

		if scalarDef.MaxLength > 0 {
			v := scalarDef.MaxLength
			info.MaxLength = &v
		}
		if scalarDef.MinLength > 0 {
			v := scalarDef.MinLength
			info.MinLength = &v
		}
		info.Minimum = scalarDef.Minimum
		info.Maximum = scalarDef.Maximum

		tokens := codegen.BuildScalarTokens(name)
		traits := codegen.BuildScalarTraits(scalarDef, tokens, "")

		if info.Type == "" {
			switch scalarDef.LanguagePrimitive {
			case ir.LanguageNumber:
				if traits.IsIntegerLike {
					info.Type = "integer"
				} else {
					info.Type = "number"
				}
			case ir.LanguageBoolean:
				info.Type = "boolean"
			default:
				info.Type = "string"
			}
		}

		if info.Description == "" {
			info.Description = fmt.Sprintf("A %s value", name)
		}

		if scalarDef.Format != "" {
			info.Format = scalarDef.Format
		} else {
			switch {
			case traits.IsEmailLike:
				info.Format = "email"
			case traits.IsDateTimeLike:
				info.Format = "date-time"
			case traits.IsDateLike:
				info.Format = "date"
			case traits.IsURLLike:
				info.Format = "uri"
			}
		}

		scalars[name] = info
	}

	return scalars
}
