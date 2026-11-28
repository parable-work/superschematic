package registry

import ir "github.com/parable-work/superschematic/ir"

// filesFeature is attachments' text as a runtime feature (D154, D159,
// #702): a database whose @searchable table indexes the extracted text of
// its @attachments needs a file processor to extract it, so the stack
// refuses an environment that runs an API over such a database with no
// files slot set. Attachments alone need none: a file whose text and
// preview no processor makes stays pending, and downloads as it was
// uploaded; search over its text is what would find nothing.
func filesFeature() RuntimeFeature {
	return RuntimeFeature{
		Name:      "attachments' text",
		Interface: ir.RuntimeFiles,
		Uses: func(in RuntimeFeatureInput) bool {
			if indexesAttachments(in.API) {
				return true
			}
			for _, db := range in.Databases {
				if indexesAttachments(db) {
					return true
				}
			}
			return false
		},
	}
}

// indexesAttachments reports whether a table of schema is @searchable with
// the extracted text of its @attachments indexed (D155).
func indexesAttachments(schema *ir.Schema) bool {
	if schema == nil {
		return false
	}
	for _, td := range schema.Types {
		if td != nil && td.Attachments != nil && td.Searchable.IndexesAttachments() {
			return true
		}
	}
	return false
}
