package codegen

// UnionShape is what a generated decoder compares a payload with to pick a
// member of a union that has no member-keyed discriminator.
type UnionShape struct {
	// Fields lists the member's JSON field names in declaration order.
	Fields []string

	// Tags lists the values the member fixes for fields it shares with
	// other members.
	Tags []UnionTag
}

// UnionTag is a value a member fixes for a field it shares with other
// members, such as kind: Default<Kind, Kind.NEW_MESSAGE>. A payload that
// states the field must state this value for the member to be picked.
type UnionTag struct {
	Field string
	Value string
}

// UnionShapes returns the shape of each member of a union without a
// member-keyed discriminator, index-aligned with members, where members[i]
// holds member i's fields as its generated decoder declares them.
//
// A member's decoder ignores keys the member does not declare, so trying
// each member in turn lets the first one whose required fields are present
// take every payload. The Go and Rust union decoders instead take the first
// member whose Fields include every payload key and whose Tags the payload
// does not contradict. When no member declares every key, they take the
// first member whose Tags the payload allows.
//
// Members with identical fields differ only by a tag: a field that at least
// two members declare, each with a distinct string or enum default. A field
// that any declaring member leaves without such a default is not a tag.
func UnionShapes(members [][]FieldInfo, enumLookup EnumLookup) []UnionShape {
	defaults := map[string][]string{}
	for _, fields := range members {
		for _, field := range fields {
			value := ""
			if field.Default != nil && !field.IsArray && !field.IsMap && (field.Type == PrimitiveString || enumLookup(field.Type)) {
				value = *field.Default
			}
			defaults[field.Name] = append(defaults[field.Name], value)
		}
	}
	tags := map[string]bool{}
	for name, values := range defaults {
		if len(values) < 2 {
			continue
		}
		distinct := map[string]bool{}
		for _, value := range values {
			if value == "" || distinct[value] {
				break
			}
			distinct[value] = true
		}
		tags[name] = len(distinct) == len(values)
	}
	shapes := make([]UnionShape, len(members))
	for i, fields := range members {
		for _, field := range fields {
			shapes[i].Fields = append(shapes[i].Fields, field.Name)
			if tags[field.Name] {
				shapes[i].Tags = append(shapes[i].Tags, UnionTag{Field: field.Name, Value: *field.Default})
			}
		}
	}
	return shapes
}
