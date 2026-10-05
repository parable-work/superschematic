package loader

import (
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

const stockAPIConfig = `{"name": "stock-api", "kind": "API", "outputs": {}}`

// TestLoadServiceReadsServiceCallersFromTheDataForms: a set's and an
// operation's service clause are typed IR fields in the data forms, written
// with the API service names the TypeScript form imports as handles.
func TestLoadServiceReadsServiceCallersFromTheDataForms(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": stockAPIConfig,
		"src/stock.schema.yaml": `kind: OperationSet
name: StockOperations
serviceCallers:
  mode: require
  from: [orders-api]
operations:
  - name: reindex
    typeRef: { name: string }
    httpMethod: POST
    restPath: stock/reindex
  - name: release
    typeRef: { name: string }
    httpMethod: POST
    restPath: stock/release
    auth: true
    serviceCallers: { mode: allow }
`,
	})
	schema, err := LoadService(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	set := schema.OperationSets[0]
	want := &ir.ServiceCallers{Mode: ir.ServiceCallersRequire, From: []string{"orders-api"}}
	if !reflect.DeepEqual(set.ServiceCallers, want) {
		t.Errorf("set ServiceCallers = %+v, want %+v", set.ServiceCallers, want)
	}
	if got := ir.EffectiveServiceCallers(set, set.Operations[1]); got == nil || got.Mode != ir.ServiceCallersAllow || len(got.From) != 0 {
		t.Errorf("release effective ServiceCallers = %+v, want allow from every edge", got)
	}
}

// TestLoadServiceRefusesServiceCallersInTheDataForms: the verify pass holds
// a schema authored as IR to the rules the TypeScript reader applies at the
// decorator, and the schema-file JSON Schema closes the mode.
func TestLoadServiceRefusesServiceCallersInTheDataForms(t *testing.T) {
	for name, tc := range map[string]struct {
		set  string
		want string
	}{
		"allow without a user clause": {
			set: `{"kind": "OperationSet", "name": "StockOperations", "serviceCallers": {"mode": "allow"},
  "operations": [{"name": "list", "typeRef": {"name": "string"}, "httpMethod": "GET", "restPath": "stock"}]}`,
			want: "StockOperations.list: the set's @allowService needs a user clause",
		},
		"a webhook": {
			set: `{"kind": "OperationSet", "name": "HookOperations",
  "operations": [{"name": "receive", "typeRef": {"name": "string"}, "httpMethod": "POST", "restPath": "hooks", "webhook": true, "serviceCallers": {"mode": "require"}}]}`,
			want: "HookOperations.receive: @requireService contradicts @webhook",
		},
		"a mode outside the enum": {
			set: `{"kind": "OperationSet", "name": "StockOperations", "serviceCallers": {"mode": "sometimes"},
  "operations": [{"name": "list", "typeRef": {"name": "string"}, "httpMethod": "GET", "restPath": "stock"}]}`,
			want: "/serviceCallers/mode",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeService(t, map[string]string{
				"schema.config.json":    stockAPIConfig,
				"src/stock.schema.json": tc.set,
			})
			_, err := LoadService(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("LoadService error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}
