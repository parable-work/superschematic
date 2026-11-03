package behaviorops

import (
	"slices"
	"testing"
)

// A use's config permissions are every string under a key named
// permission or ending in Permission, at any depth, sorted and each once:
// a Workflow's transitions' and its overridePermission (D104), which moves a
// caller past a transition's by and so is checked by transition too.
func TestConfigPermissionsNameOverridePermission(t *testing.T) {
	config := []byte(`{
	  "states": ["open", "answered", "closed"],
	  "transitions": [
	    {"from": "open", "to": "answered", "by": ["askedOf"]},
	    {"from": "answered", "to": "closed", "by": ["askedOf"], "permission": "questions.close"},
	    {"from": "closed", "to": "open", "permission": "questions.close"}
	  ],
	  "overridePermission": "questions.moderate",
	  "repeat": "unchanged"
	}`)
	if got, want := ConfigPermissions(config), []string{"questions.close", "questions.moderate"}; !slices.Equal(got, want) {
		t.Errorf("ConfigPermissions = %v, want %v", got, want)
	}
	for _, none := range []string{``, `{}`, `{"states": ["open"], "transitions": [], "overridePermission": ""}`, `{"permissions": "not.one"}`} {
		if got := ConfigPermissions([]byte(none)); len(got) != 0 {
			t.Errorf("ConfigPermissions(%s) = %v, want none", none, got)
		}
	}
}
