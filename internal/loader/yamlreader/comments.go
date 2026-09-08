package yamlreader

import (
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/parable-work/superschematic/internal/loader/schemafile"
	ir "github.com/parable-work/superschematic/ir"
)

// applyComments maps native YAML comments from the node tree onto the
// decoded document's IR Comment metadata. Comments are data, not syntax: a
// head comment above a node (or a line comment on it) becomes the node's
// Comment, so it survives conversion to JSON and TypeScript.
//
// Explicit "comment" keys also work (they are IR fields); a native comment
// wins when both are present. Free-floating comments attached to no node are
// not modeled in the IR.
func applyComments(root, mapping *yaml.Node, doc *schemafile.Document, documentForm bool) {
	// A comment block at the top of the file attaches to the document node,
	// the top mapping, or the first key, depending on layout.
	var firstKeyHead string
	if mapping.Kind == yaml.MappingNode && len(mapping.Content) > 0 {
		firstKeyHead = mapping.Content[0].HeadComment
	}
	topComment := commentText(root.HeadComment, mapping.HeadComment, firstKeyHead)

	if !documentForm {
		if target := singleDefinitionComment(doc); target != nil && topComment != "" {
			*target = topComment
		}
		applySingleDefinitionBodyComments(mapping, doc)
		return
	}

	if topComment != "" {
		doc.Comment = topComment
	}

	for key, value := range mappingPairs(mapping) {
		switch key.Value {
		case "types":
			for entry := range defMapEntries(value) {
				def, ok := doc.Types[entry.name]
				if !ok {
					continue
				}
				if c := entryComment(entry.key, entry.body); c != "" {
					def.Comment = c
				}
				applyTypeBodyComments(entry.body, def)
			}
		case "enums":
			for entry := range defMapEntries(value) {
				def, ok := doc.Enums[entry.name]
				if !ok {
					continue
				}
				if c := entryComment(entry.key, entry.body); c != "" {
					def.Comment = c
				}
				applyEnumBodyComments(entry.body, def)
			}
		case "unions":
			for entry := range defMapEntries(value) {
				if def, ok := doc.Unions[entry.name]; ok {
					if c := entryComment(entry.key, entry.body); c != "" {
						def.Comment = c
					}
				}
			}
		case "scalars":
			for entry := range defMapEntries(value) {
				if def, ok := doc.Scalars[entry.name]; ok {
					if c := entryComment(entry.key, entry.body); c != "" {
						def.Comment = c
					}
				}
			}
		case "operationSets":
			if value.Kind != yaml.SequenceNode {
				continue
			}
			for i, item := range value.Content {
				if i >= len(doc.OperationSets) {
					break
				}
				set := doc.OperationSets[i]
				if c := itemComment(item); c != "" {
					set.Comment = c
				}
				applyFieldSequenceComments(item, "operations", set.Operations)
			}
		}
	}
}

// singleDefinitionComment returns the definition-level Comment target of a
// single-definition file.
func singleDefinitionComment(doc *schemafile.Document) *string {
	for _, def := range doc.Types {
		return &def.Comment
	}
	for _, def := range doc.Enums {
		return &def.Comment
	}
	for _, def := range doc.Unions {
		return &def.Comment
	}
	for _, def := range doc.Scalars {
		return &def.Comment
	}
	if len(doc.OperationSets) == 1 {
		return &doc.OperationSets[0].Comment
	}
	return nil
}

// applySingleDefinitionBodyComments maps comments inside a single-definition
// file, where the top mapping is the definition body itself.
func applySingleDefinitionBodyComments(mapping *yaml.Node, doc *schemafile.Document) {
	for _, def := range doc.Types {
		applyTypeBodyComments(mapping, def)
	}
	for _, def := range doc.Enums {
		applyEnumBodyComments(mapping, def)
	}
	if len(doc.OperationSets) == 1 {
		applyFieldSequenceComments(mapping, "operations", doc.OperationSets[0].Operations)
	}
}

// applyTypeBodyComments maps comments inside a type definition body: fields,
// their arguments, and configurable-trait config fields.
func applyTypeBodyComments(defNode *yaml.Node, def *ir.TypeDef) {
	applyFieldSequenceComments(defNode, "fields", def.Fields)
	for key, value := range mappingPairs(defNode) {
		if key.Value == "traitConfig" && def.TraitConfig != nil {
			applyFieldSequenceComments(value, "fields", def.TraitConfig.Fields)
		}
	}
}

// applyEnumBodyComments maps comments on enum value sequence items.
func applyEnumBodyComments(defNode *yaml.Node, def *ir.EnumDef) {
	for key, value := range mappingPairs(defNode) {
		if key.Value != "values" || value.Kind != yaml.SequenceNode {
			continue
		}
		for i, item := range value.Content {
			if i >= len(def.Values) {
				break
			}
			if c := itemComment(item); c != "" {
				def.Values[i].Comment = c
			}
		}
	}
}

// applyFieldSequenceComments maps comments on the items of a named field
// sequence (fields, operations, inputs) and on each item's arguments.
func applyFieldSequenceComments(defNode *yaml.Node, sequenceKey string, fields []*ir.FieldDef) {
	for key, value := range mappingPairs(defNode) {
		if key.Value != sequenceKey || value.Kind != yaml.SequenceNode {
			continue
		}
		for i, item := range value.Content {
			if i >= len(fields) {
				break
			}
			if c := itemComment(item); c != "" {
				fields[i].Comment = c
			}
			applyArgumentComments(item, fields[i])
		}
	}
}

// applyArgumentComments maps comments on a field's argument sequence items.
func applyArgumentComments(fieldNode *yaml.Node, field *ir.FieldDef) {
	for key, value := range mappingPairs(fieldNode) {
		if key.Value != "arguments" || value.Kind != yaml.SequenceNode {
			continue
		}
		for i, item := range value.Content {
			if i >= len(field.Arguments) {
				break
			}
			if c := itemComment(item); c != "" {
				field.Arguments[i].Comment = c
			}
		}
	}
}

// mappingPairs iterates the key/value node pairs of a mapping node.
func mappingPairs(node *yaml.Node) func(yield func(*yaml.Node, *yaml.Node) bool) {
	return func(yield func(*yaml.Node, *yaml.Node) bool) {
		if node == nil || node.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			if !yield(node.Content[i], node.Content[i+1]) {
				return
			}
		}
	}
}

// defEntry is one entry of a name -> definition-body mapping.
type defEntry struct {
	name string
	key  *yaml.Node
	body *yaml.Node
}

// defMapEntries iterates a name -> definition-body mapping.
func defMapEntries(node *yaml.Node) func(yield func(defEntry) bool) {
	return func(yield func(defEntry) bool) {
		for key, value := range mappingPairs(node) {
			if value.Kind != yaml.MappingNode {
				continue
			}
			if !yield(defEntry{name: key.Value, key: key, body: value}) {
				return
			}
		}
	}
}

// entryComment extracts the comment attached to a name -> definition map
// entry: the head comment above the key, or the line comment on it.
func entryComment(keyNode, defNode *yaml.Node) string {
	return commentText(keyNode.HeadComment, defNode.HeadComment, keyNode.LineComment)
}

// itemComment extracts the comment attached to a sequence item: the head
// comment above the item (yaml.v3 attaches it to the item node or its first
// key, depending on style), or the line comment on the item's first line.
func itemComment(item *yaml.Node) string {
	heads := []string{item.HeadComment}
	lines := []string{item.LineComment}
	if item.Kind == yaml.MappingNode && len(item.Content) >= 2 {
		heads = append(heads, item.Content[0].HeadComment)
		lines = append(lines, item.Content[0].LineComment, item.Content[1].LineComment)
	}
	if c := commentText(heads...); c != "" {
		return c
	}
	return commentText(lines...)
}

// commentText returns the first non-empty candidate, normalized: '#' markers
// stripped per line, whitespace trimmed, lines joined with newlines.
func commentText(candidates ...string) string {
	for _, raw := range candidates {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var lines []string
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			line = strings.TrimPrefix(line, "#")
			line = strings.TrimPrefix(line, " ")
			if line != "" {
				lines = append(lines, line)
			}
		}
		if len(lines) > 0 {
			return strings.Join(lines, "\n")
		}
	}
	return ""
}
