package registry

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// TestTheBucketKind: a Bucket service gets a sentinel and no generator, and
// its schema declares nothing: an empty one passes, and each declaration
// is refused by name (D54).
func TestTheBucketKind(t *testing.T) {
	spec, ok := New(naming.Naming{}).Kind(string(ir.SchemaKindBucket))
	if !ok {
		t.Fatal("no Bucket kind")
	}
	if spec.NoSentinel || len(spec.Pipeline) != 0 || !spec.ForbiddenPackages[pkgAPI] || !spec.ForbiddenPackages[pkgDB] || !spec.ForbiddenPackages[pkgStack] {
		t.Errorf("Bucket kind = %+v", spec)
	}
	var empty findings
	spec.Verify(ir.NewSchema("shop-media", ir.SchemaKindBucket), &empty)
	if len(empty.errs) != 0 {
		t.Errorf("an empty Bucket schema: %v", empty.errs)
	}
	schema := ir.NewSchema("shop-media", ir.SchemaKindBucket)
	schema.Types = map[string]*ir.TypeDef{"Image": {Name: "Image"}}
	schema.Enums = map[string]*ir.EnumDef{"Kind": {Name: "Kind"}}
	var got findings
	spec.Verify(schema, &got)
	if len(got.errs) != 2 || !strings.HasSuffix(got.errs[0], "(enum Kind)") || !strings.HasSuffix(got.errs[1], "(type Image)") ||
		!strings.Contains(got.errs[0], "shop-media is a Bucket service, which declares nothing in its schema files") {
		t.Errorf("a Bucket schema with declarations: %v", got.errs)
	}
}
