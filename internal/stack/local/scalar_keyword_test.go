package local

import (
	"encoding/json"
	"strings"
	"testing"

	scalars "github.com/parable-work/superscalar/go"
)

// A port in the local target's schemas names Network.Port under
// x-superschematic-scalar, and a schema that names a scalar holds a value
// to the type and the bounds the scalar declares.
func TestASchemaThatNamesAScalarHoldsItsBounds(t *testing.T) {
	documents := map[string]string{"targetValues": targetValues, "serverSettings": serverSettings}
	for name, schema := range resourceTypes {
		documents[name] = schema
	}
	named := 0
	var walk func(document, property string, node any)
	walk = func(document, property string, node any) {
		switch value := node.(type) {
		case []any:
			for _, item := range value {
				walk(document, property, item)
			}
		case map[string]any:
			scalar, _ := value["x-superschematic-scalar"].(string)
			if strings.HasSuffix(strings.ToLower(property), "port") && value["type"] == "integer" && scalar != "Network.Port" {
				t.Errorf("%s: %s is a port and names %q, not Network.Port", document, property, scalar)
			}
			if scalar != "" {
				named++
				meta, ok := scalars.ScalarMetadataByCanonical[scalar]
				if !ok {
					t.Fatalf("%s: %s names %s, which superscalar does not define", document, property, scalar)
				}
				if value["type"] != meta.JSONSchemaType {
					t.Errorf("%s: %s is %v, and %s is %s", document, property, value["type"], scalar, meta.JSONSchemaType)
				}
				for keyword, bound := range map[string]*int64{"minimum": meta.Minimum, "maximum": meta.Maximum} {
					got, has := value[keyword].(float64)
					if (bound == nil) != !has || (bound != nil && got != float64(*bound)) {
						t.Errorf("%s: %s has %s %v, and %s declares %v", document, property, keyword, value[keyword], scalar, bound)
					}
				}
			}
			for key, child := range value {
				walk(document, key, child)
			}
		}
	}
	for name, text := range documents {
		var schema any
		if err := json.Unmarshal([]byte(text), &schema); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		walk(name, "", schema)
	}
	if named == 0 {
		t.Fatal("no schema names a scalar")
	}
}
