package ir

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestResourceReferencesRoundTrip: references in a resource's properties
// encode as tagged objects and decode back to Output, Parameter and Concat.
func TestResourceReferencesRoundTrip(t *testing.T) {
	res := &Resource{
		ID:   "shop-api.service",
		Type: "gcp:cloudrunv2/service:Service",
		Properties: map[string]any{
			"name":    Concat{"shop-api-", Parameter("pr")},
			"account": Output{Resource: "shop-api.account", Name: "email"},
			"env": []any{
				map[string]any{"name": "LOG_LEVEL", "value": "warn"},
				map[string]any{"name": "PR", "value": Parameter("pr")},
			},
			"minInstances": float64(1),
		},
		DependsOn: []string{"shop-api.account"},
		Phase:     PhaseRollout,
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"name":{"$concat":["shop-api-",{"$parameter":"pr"}]}`,
		`"account":{"$output":{"resource":"shop-api.account","name":"email"}}`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%s lacks %s", data, want)
		}
	}
	var back Resource
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&back, res) {
		t.Errorf("round trip:\n got %#v\nwant %#v", back.Properties, res.Properties)
	}
	outputs, params := ValueRefs(back.Properties)
	if len(outputs) != 1 || outputs[0].Resource != "shop-api.account" || strings.Join(params, ",") != "pr" {
		t.Errorf("ValueRefs = %v, %v", outputs, params)
	}
}

// TestDecodeValueRejectsMalformedReferences: a reference object that is
// not well formed is an error rather than a literal.
func TestDecodeValueRejectsMalformedReferences(t *testing.T) {
	for _, doc := range []string{
		`{"$output": "shop-api.account"}`,
		`{"$output": {"resource": "a"}}`,
		`{"$parameter": 7}`,
		`{"$concat": "a"}`,
		`{"$concat": ["a", {"nested": true}]}`,
		`{"x": [{"$parameter": ""}]}`,
	} {
		var v any
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeValue(v); err == nil {
			t.Errorf("DecodeValue(%s) succeeded", doc)
		}
	}
	var v any
	_ = json.Unmarshal([]byte(`{"$other": 1, "plain": {"a": [1, "b"]}}`), &v)
	got, err := DecodeValue(v)
	if err != nil || !reflect.DeepEqual(got, v) {
		t.Errorf("a literal object changed: %v, %v", got, err)
	}
}

// TestResourcePropertiesMustBeAnObject: properties that decode to a
// reference are an error, not a panic.
func TestResourcePropertiesMustBeAnObject(t *testing.T) {
	var res Resource
	err := json.Unmarshal([]byte(`{"id": "x", "type": "t", "properties": {"$parameter": "pr"}}`), &res)
	if err == nil || !strings.Contains(err.Error(), "properties must be an object") {
		t.Fatalf("Unmarshal = %v", err)
	}
}

// TestResolvedEnvironmentDecodesReferences: a deployable's name and
// address, a binding's value and a DNS record decode their references.
func TestResolvedEnvironmentDecodesReferences(t *testing.T) {
	doc := `{
	  "version": 1, "stack": "Shop", "environment": "Preview", "target": "gcp",
	  "deployables": [{"name": "shop-api", "kind": "server", "platform": "p", "services": [],
	    "resourceName": {"$concat": ["shop-api-", {"$parameter": "pr"}]},
	    "address": {"$output": {"resource": "shop-api.service", "name": "uri"}},
	    "bindings": [{"field": "SHOP_DB_DATABASE", "source": "derived", "edge": "sql:shop-api->shop-db",
	      "value": {"instance": {"$output": {"resource": "db", "name": "connectionName"}}}}]}],
	  "dns": {"platform": "manual", "records": [{"name": {"$concat": ["a.", {"$parameter": "pr"}]}, "type": "CNAME",
	    "value": {"$output": {"resource": "lb", "name": "ip"}}}]},
	  "resources": {"resources": []}, "deployOrder": []
	}`
	var env ResolvedEnvironment
	if err := json.Unmarshal([]byte(doc), &env); err != nil {
		t.Fatal(err)
	}
	d := env.Deployable("shop-api")
	if _, ok := d.ResourceName.(Concat); !ok {
		t.Errorf("resourceName = %T", d.ResourceName)
	}
	if _, ok := d.Address.(Output); !ok {
		t.Errorf("address = %T", d.Address)
	}
	if v, ok := d.Bindings[0].Value.(map[string]any); !ok || v["instance"] != (Output{Resource: "db", Name: "connectionName"}) {
		t.Errorf("binding value = %#v", d.Bindings[0].Value)
	}
	if _, ok := env.DNS.Records[0].Value.(Output); !ok {
		t.Errorf("record value = %T", env.DNS.Records[0].Value)
	}
}
