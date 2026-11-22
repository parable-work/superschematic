package naming

import (
	"strings"
	"testing"
)

// TestTheCommentKeys: a file without them, and a zero Naming filled by
// OrDefault, carry the core's comment reading; a file sets each; and a
// value neither key takes fails the load, naming the key and its values.
func TestTheCommentKeys(t *testing.T) {
	n, err := Parse([]byte("npm_scope = \"@x\"\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]Naming{"a parsed file": n, "a zero Naming": (Naming{}).OrDefault()} {
		if got.TypeComments != TypeCommentsAttached || got.PrivateRemarks != PrivateRemarksKeep {
			t.Errorf("%s: type_comments %q, private_remarks %q; want %q and %q", name, got.TypeComments, got.PrivateRemarks, TypeCommentsAttached, PrivateRemarksKeep)
		}
	}
	n, err = Parse([]byte("type_comments = \"leading\"\nprivate_remarks = \"drop\"\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if n.TypeComments != TypeCommentsLeading || n.PrivateRemarks != PrivateRemarksDrop {
		t.Errorf("type_comments %q, private_remarks %q; want leading and drop", n.TypeComments, n.PrivateRemarks)
	}
	for _, tc := range []struct{ file, want string }{
		{"type_comments = \"all\"\n", `type_comments is "all": use "attached" or "leading"`},
		{"private_remarks = \"hide\"\n", `private_remarks is "hide": use "keep" or "drop"`},
	} {
		_, err := Parse([]byte(tc.file), "superschematic.toml")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want it to hold %q", tc.file, err, tc.want)
		}
	}
}
