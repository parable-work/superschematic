// Package yamlwriter is the YAML back end of the schema writer:
// Document -> *.schema.yaml bytes. It is the inverse of yamlreader.
//
// The YAML format represents comments natively: IR Comment metadata is
// emitted as '#' head comments via yaml.Node, not as explicit "comment"
// keys. The writer marshals the document into a node tree, then relocates
// every "comment" property onto the node the reader's comment mapping reads
// it back from (see yamlreader/comments.go).
package yamlwriter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/parable-work/superschematic/internal/loader/schemafile"
)

// Write renders a document as YAML. Documents holding exactly one definition
// and no document-level metadata are written in the single-definition file
// form (with the "kind" discriminator the reader strips).
func Write(doc *schemafile.Document) ([]byte, error) {
	root := &yaml.Node{}

	kind, def, single := schemafile.SingleDefinition(doc)
	if single {
		if err := root.Encode(def); err != nil {
			return nil, fmt.Errorf("encoding definition: %w", err)
		}
		if kind != schemafile.SingleDefType {
			prependKind(root, string(kind))
		}
		relocateSingleDefComments(root, kind)
	} else {
		if err := root.Encode(doc); err != nil {
			return nil, fmt.Errorf("encoding document: %w", err)
		}
		relocateDocumentComments(root)
	}
	if err := expandRawJSON(root); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// rawJSONKeys are the IR keys whose values are json.RawMessage maps:
// "extensions" on every node that carries one and "documents" on the root.
var rawJSONKeys = map[string]bool{"extensions": true, "documents": true}

// expandRawJSON rewrites the values under "extensions" and "documents"
// mappings from the byte sequences yaml.v3 emits for json.RawMessage into
// the YAML mappings the reader expects. A mapping is rewritten only when
// every entry is a byte sequence, so a definition that happens to be named
// "extensions" is left alone.
func expandRawJSON(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode || node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			if err := expandRawJSON(child); err != nil {
				return err
			}
		}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for key, value := range mappingPairs(node) {
		if rawJSONKeys[key.Value] && allByteSequences(value) {
			for name, raw := range mappingPairs(value) {
				if err := replaceWithJSON(raw, key.Value, name.Value); err != nil {
					return err
				}
			}
			continue
		}
		if err := expandRawJSON(value); err != nil {
			return err
		}
	}
	return nil
}

func allByteSequences(mapping *yaml.Node) bool {
	if mapping == nil || mapping.Kind != yaml.MappingNode || len(mapping.Content) == 0 {
		return false
	}
	for _, value := range mappingPairs2(mapping) {
		if value.Kind != yaml.SequenceNode {
			return false
		}
		for _, item := range value.Content {
			if item.Kind != yaml.ScalarNode || item.Tag != "!!int" {
				return false
			}
		}
	}
	return true
}

// replaceWithJSON parses the JSON held in a byte sequence node as YAML (JSON
// is a YAML subset, so key order survives) and puts the block-style result
// in place of the sequence.
func replaceWithJSON(seq *yaml.Node, key, name string) error {
	raw := make([]byte, 0, len(seq.Content))
	for _, item := range seq.Content {
		b, err := strconv.Atoi(item.Value)
		if err != nil {
			return fmt.Errorf("%s %q: %w", key, name, err)
		}
		raw = append(raw, byte(b))
	}
	if !json.Valid(raw) {
		return fmt.Errorf("%s %q: value is not valid JSON", key, name)
	}
	var parsed yaml.Node
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("%s %q: %w", key, name, err)
	}
	if parsed.Kind != yaml.DocumentNode || len(parsed.Content) != 1 {
		return fmt.Errorf("%s %q: unexpected YAML structure", key, name)
	}
	clearStyle(parsed.Content[0])
	*seq = *parsed.Content[0]
	return nil
}

// clearStyle drops the flow and quoting styles the YAML parser records for
// JSON input so the value renders in the writer's block style.
func clearStyle(node *yaml.Node) {
	node.Style = 0
	for _, child := range node.Content {
		clearStyle(child)
	}
}

// prependKind inserts the single-definition "kind" discriminator as the
// first entry of a mapping node.
func prependKind(mapping *yaml.Node, kind string) {
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "kind"}
	value := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: kind}
	mapping.Content = append([]*yaml.Node{key, value}, mapping.Content...)
}

// relocateDocumentComments moves explicit "comment" entries onto native YAML
// comments for the multi-definition document form, mirroring the placements
// yamlreader/comments.go applyComments reads from.
func relocateDocumentComments(mapping *yaml.Node) {
	// The document-level comment renders above the first key.
	if c := removeCommentEntry(mapping); c != "" {
		setHeadComment(firstKey(mapping), c)
	}

	for key, value := range mappingPairs(mapping) {
		switch key.Value {
		case "types":
			for entryKey, body := range mappingPairs(value) {
				if c := removeCommentEntry(body); c != "" {
					setHeadComment(entryKey, c)
				}
				relocateTypeBodyComments(body)
			}
		case "enums":
			for entryKey, body := range mappingPairs(value) {
				if c := removeCommentEntry(body); c != "" {
					setHeadComment(entryKey, c)
				}
				relocateEnumBodyComments(body)
			}
		case "unions", "scalars":
			for entryKey, body := range mappingPairs(value) {
				if c := removeCommentEntry(body); c != "" {
					setHeadComment(entryKey, c)
				}
			}
		case "operationSets":
			for _, item := range sequenceItems(value) {
				if c := removeCommentEntry(item); c != "" {
					setHeadComment(item, c)
				}
				relocateFieldSequenceComments(item, "operations")
			}
		}
	}
}

// relocateSingleDefComments moves comments for a single-definition file,
// where the top mapping is the definition body itself: the definition's own
// comment becomes the file's top comment.
func relocateSingleDefComments(body *yaml.Node, kind schemafile.SingleDefKind) {
	if c := removeCommentEntry(body); c != "" {
		setHeadComment(firstKey(body), c)
	}
	switch kind {
	case schemafile.SingleDefType:
		relocateTypeBodyComments(body)
	case schemafile.SingleDefEnum:
		relocateEnumBodyComments(body)
	case schemafile.SingleDefOperationSet:
		relocateFieldSequenceComments(body, "operations")
	}
}

// relocateTypeBodyComments handles a type definition body: fields, their
// arguments, and configurable-trait config fields.
func relocateTypeBodyComments(body *yaml.Node) {
	relocateFieldSequenceComments(body, "fields")
	for key, value := range mappingPairs(body) {
		if key.Value == "traitConfig" {
			relocateFieldSequenceComments(value, "fields")
		}
	}
}

// relocateEnumBodyComments handles enum value sequence items.
func relocateEnumBodyComments(body *yaml.Node) {
	for key, value := range mappingPairs(body) {
		if key.Value != "values" {
			continue
		}
		for _, item := range sequenceItems(value) {
			if c := removeCommentEntry(item); c != "" {
				setHeadComment(item, c)
			}
		}
	}
}

// relocateFieldSequenceComments handles the items of a named field sequence
// (fields, operations, inputs) and each item's arguments.
func relocateFieldSequenceComments(body *yaml.Node, sequenceKey string) {
	for key, value := range mappingPairs(body) {
		if key.Value != sequenceKey {
			continue
		}
		for _, item := range sequenceItems(value) {
			if c := removeCommentEntry(item); c != "" {
				setHeadComment(item, c)
			}
			for argKey, argValue := range mappingPairs(item) {
				if argKey.Value != "arguments" {
					continue
				}
				for _, argItem := range sequenceItems(argValue) {
					if c := removeCommentEntry(argItem); c != "" {
						setHeadComment(argItem, c)
					}
				}
			}
		}
	}
}

// removeCommentEntry removes a mapping's "comment" key/value pair and
// returns the comment text, or "" when the mapping has none.
func removeCommentEntry(mapping *yaml.Node) string {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != "comment" {
			continue
		}
		text := mapping.Content[i+1].Value
		mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
		return text
	}
	return ""
}

// setHeadComment attaches comment text as a node's head comment. yaml.v3
// renders each line with a '# ' marker; the reader strips them back off.
func setHeadComment(node *yaml.Node, text string) {
	if node == nil || text == "" {
		return
	}
	node.HeadComment = text
}

// firstKey returns the first key node of a mapping, or nil when empty.
func firstKey(mapping *yaml.Node) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode || len(mapping.Content) == 0 {
		return nil
	}
	return mapping.Content[0]
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

// mappingPairs2 iterates only the value nodes of a mapping node.
func mappingPairs2(node *yaml.Node) func(yield func(string, *yaml.Node) bool) {
	return func(yield func(string, *yaml.Node) bool) {
		for key, value := range mappingPairs(node) {
			if !yield(key.Value, value) {
				return
			}
		}
	}
}

// sequenceItems returns the items of a sequence node.
func sequenceItems(node *yaml.Node) []*yaml.Node {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	return node.Content
}
