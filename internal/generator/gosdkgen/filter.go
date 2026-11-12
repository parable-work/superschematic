package gosdkgen

import (
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// FilterExample is a call of the SDK's first filtered method (D106) as
// the README writes one: its filter with one field held to a value of a
// list and, when another takes one, a field under Gte or Contains. The
// fields it shows are ones whose values are Go's own types, which the
// README can write without the types package. It is "" when the SDK has no
// filtered method with such a field that takes its query alone beside a
// page, in a namespace without a scope.
func (o *SDKOutput) FilterExample() string {
	for _, ns := range o.Namespaces {
		if ns.IsScopedNS {
			continue
		}
		for _, ep := range ns.Endpoints {
			if ep.Filter == nil || len(ep.PathParams) > 0 || ep.HasInput || ep.HasScalarArgs || ep.HasFileUpload || ep.HasEncryptedBody {
				continue
			}
			if example := filterExample(ns, ep); example != "" {
				return example
			}
		}
	}
	return ""
}

// filterExampleValue is a value of a Go type as Go source, or "" for a
// type of the types package, whose values the README does not write.
func filterExampleValue(goType string) string {
	switch goType {
	case "int64":
		return "10"
	case "float64":
		return "2.5"
	case "bool":
		return "true"
	case "string":
		return `"anvil"`
	}
	return ""
}

// filterExample is the call FilterExample writes for one endpoint, or ""
// when no field of its filter holds values of a Go type of its own.
func filterExample(ns NamespaceInfo, ep EndpointInfo) string {
	// A member is written as gofmt aligns it: its name, padded to the
	// longest, then its value.
	type member struct{ name, value string }
	var lines []string
	var chosen []member
	first := -1
	for i, field := range ep.FilterFields {
		if value := filterExampleValue(field.ValueGoType); value != "" {
			first = i
			chosen = append(chosen, member{field.GoName, "&" + field.GoType + "{In: []" + field.ValueGoType + "{" + value + "}},"})
			break
		}
	}
	if first < 0 {
		return ""
	}
	for _, op := range []string{ir.FilterOpGte, ir.FilterOpContains} {
		if len(chosen) > 1 {
			break
		}
		for i, field := range ep.Filter.Fields {
			goType := ep.FilterFields[i].ValueGoType
			operand := filterExampleValue(goType)
			if i == first || operand == "" || !field.Takes(op) {
				continue
			}
			if op == ir.FilterOpContains {
				operand, goType = `"an"`, "string"
			}
			if goType == "string" || goType == "bool" {
				lines = append(lines, "bound := "+operand)
			} else {
				lines = append(lines, "bound := "+goType+"("+operand+")")
			}
			chosen = append(chosen, member{ep.FilterFields[i].GoName, "&" + ep.FilterFields[i].GoType +
				"{" + strings.ToUpper(op[:1]) + op[1:] + ": &bound},"})
			break
		}
	}
	result := "items"
	if ep.Page != nil {
		result = "page"
	}
	lines = append(lines,
		result+", err := client."+ns.StructName+"."+ep.MethodName+"(ctx, &namespaces."+ep.QueryStructName+"{",
		"\t"+ep.FilterGoName+": &namespaces."+ep.FilterStructName+"{")
	width := 0
	for _, m := range chosen {
		width = max(width, len(m.name))
	}
	for _, m := range chosen {
		lines = append(lines, "\t\t"+m.name+": "+strings.Repeat(" ", width-len(m.name))+m.value)
	}
	lines = append(lines, "\t},")
	if ep.Page != nil {
		lines = append(lines, "}, runtime.PageRequest{})")
	} else {
		lines = append(lines, "})")
	}
	return strings.Join(lines, "\n")
}

// FilterMaxInValues and FilterMaxFilters are the most values a filter's
// In may list and the most filters a request may hold, which a filter's
// struct documents and filterparse.Check holds a request to.
func (namespaceTemplateData) FilterMaxInValues() int { return ir.FilterMaxInValues }

func (namespaceTemplateData) FilterMaxFilters() int { return ir.FilterMaxFilters }

// FilterMaxInValues and FilterMaxFilters are the same limits, for the
// README.
func (*SDKOutput) FilterMaxInValues() int { return ir.FilterMaxInValues }

func (*SDKOutput) FilterMaxFilters() int { return ir.FilterMaxFilters }
