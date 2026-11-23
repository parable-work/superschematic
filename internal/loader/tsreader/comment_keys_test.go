package tsreader

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
)

// commentKeysSource has a section banner parted from an enum, a boxed
// banner over a class's doc, a field under a one-line banner, and a
// @privateRemarks section on the class and on a field.
const commentKeysSource = `import { Identity } from "superscalar";
import { key } from "@superschematic/db";

// ---- Plaid -----------------------------------------------------------------

export enum PlaidItemStatus {
  Active = "active",
}

// ============================================================================
// Plaid items
// ============================================================================
// A bank connection, one per institution a user links.
// @privateRemarks
// The nightly sweep deletes an item a day after its user unlinks it.
export abstract class PlaidItem {
  @key id: Identity.UUID;

  // ---- sync -----------------------------------------------------------------
  cursor: string;
  // The status Plaid last reported.
  // @privateRemarks
  // The webhook handler writes it.
  status: PlaidItemStatus;
}
`

// TestTheCommentKeysReadADeclarationAsWritten loads one source under the
// core's comment reading and under type_comments = "leading" and
// private_remarks = "drop". The first is D100's: a banner documents
// nothing, and a @privateRemarks section stays. The second reads a
// declaration's every comment above it as written, banners included, reads
// a member's as D100 does, and leaves out each @privateRemarks section.
func TestTheCommentKeysReadADeclarationAsWritten(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		typeComments, remarks    string
		enum, item, cursor, stat string
	}{
		{
			name: "core", typeComments: naming.TypeCommentsAttached, remarks: naming.PrivateRemarksKeep,
			enum:   "",
			item:   "A bank connection, one per institution a user links.\n@privateRemarks\nThe nightly sweep deletes an item a day after its user unlinks it.",
			cursor: "",
			stat:   "The status Plaid last reported.\n@privateRemarks\nThe webhook handler writes it.",
		},
		{
			name: "leading, drop", typeComments: naming.TypeCommentsLeading, remarks: naming.PrivateRemarksDrop,
			enum: "---- Plaid -----------------------------------------------------------------",
			item: "============================================================================\nPlaid items\n" +
				"============================================================================\nA bank connection, one per institution a user links.",
			cursor: "",
			stat:   "The status Plaid last reported.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := naming.Default()
			n.TypeComments, n.PrivateRemarks = tc.typeComments, tc.remarks
			dir := decoratorTestService(t, "DB", map[string]string{"src/a.schema.ts": commentKeysSource})
			schema, _, _, err := LoadServiceWithConfig(dir, WithRegistry(registry.New(n)))
			if err != nil {
				t.Fatal(err)
			}
			if got := schema.Enums["PlaidItemStatus"].Comment; got != tc.enum {
				t.Errorf("PlaidItemStatus comment = %q, want %q", got, tc.enum)
			}
			item := schema.Types["PlaidItem"]
			if item.Comment != tc.item {
				t.Errorf("PlaidItem comment = %q, want %q", item.Comment, tc.item)
			}
			for _, f := range item.Fields {
				switch f.Name {
				case "cursor":
					if f.Comment != tc.cursor {
						t.Errorf("cursor comment = %q, want %q", f.Comment, tc.cursor)
					}
				case "status":
					if f.Comment != tc.stat {
						t.Errorf("status comment = %q, want %q", f.Comment, tc.stat)
					}
				}
			}
		})
	}
}

// TestWithoutPrivateRemarks drops a section to the next block tag or the
// end, and the empty lines it leaves at the end; "@privateRemarksX" is no
// tag.
func TestWithoutPrivateRemarks(t *testing.T) {
	for in, want := range map[string]string{
		"Doc.":                                   "Doc.",
		"Doc.\n@privateRemarks\nNote.":           "Doc.",
		"Doc.\n@privateRemarks Note.\n@see X":    "Doc.\n@see X",
		"Doc.\n\n@privateRemarks\nNote.\n":       "Doc.",
		"Doc.\n@privateRemarksX\nNote.":          "Doc.\n@privateRemarksX\nNote.",
		"@privateRemarks\nAll of it.":            "",
		"Doc.\n@privateRemarks\nA.\n@example\nB": "Doc.\n@example\nB",
	} {
		if got := withoutPrivateRemarks(in); got != want {
			t.Errorf("withoutPrivateRemarks(%q) = %q, want %q", in, got, want)
		}
	}
}
