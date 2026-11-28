package registry

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// TestSearchOverAttachmentsNeedsAFileProcessor (D154, D159, #702): an API
// over a database whose @searchable table indexes its @attachments' text
// uses the files interface, so the stack refuses an environment with no
// files slot set; attachments alone, or a search that leaves the files'
// text out, use none.
func TestSearchOverAttachmentsNeedsAFileProcessor(t *testing.T) {
	off := false
	for name, tc := range map[string]struct {
		task *ir.TypeDef
		want bool
	}{
		"search over attachments": {&ir.TypeDef{Name: "Task", Attachments: &ir.AttachmentsDef{}, Searchable: &ir.SearchableDef{}}, true},
		"attachments alone":       {&ir.TypeDef{Name: "Task", Attachments: &ir.AttachmentsDef{}}, false},
		"search alone":            {&ir.TypeDef{Name: "Task", Searchable: &ir.SearchableDef{}}, false},
		"files left out":          {&ir.TypeDef{Name: "Task", Attachments: &ir.AttachmentsDef{}, Searchable: &ir.SearchableDef{Attachments: &off}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			db := &ir.Schema{Name: "work-db", Types: map[string]*ir.TypeDef{"Task": tc.task}}
			used := New(naming.Naming{}).RuntimeFeaturesUsed(RuntimeFeatureInput{API: &ir.Schema{Name: "work-api"}, Databases: map[string]*ir.Schema{"work-db": db}})
			got := false
			for _, u := range used {
				if u.Interface == ir.RuntimeFiles {
					got = u.Feature == "attachments' text"
				}
			}
			if got != tc.want {
				t.Errorf("uses files = %v, want %v (%v)", got, tc.want, used)
			}
		})
	}
}
