package rustrestgen

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestRateLimitOperationIsTheGoServers: the operation part of a shared
// rate limit's key (D75) is `<namespace>.<name>`, which the Rust runtime
// formats from each operation's OperationInfo. For every operation of
// fixture-api and of fixture-multiword-api, whose sets' two-word names
// give kebab namespaces (`pool-search`), that is the spelling the Go
// server passes to WithStore, apigen's RateLimitOperation.
func TestRateLimitOperationIsTheGoServers(t *testing.T) {
	for _, name := range []string{"fixture-api", "fixture-multiword-api"} {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		api, err := apiOutputOf(schema, apiSource{}, Options{SchemaName: name})
		if err != nil {
			t.Fatalf("apigen %s: %v", name, err)
		}
		if len(api.Endpoints) == 0 {
			t.Fatalf("%s has no endpoints", name)
		}
		for _, ep := range api.Endpoints {
			info := endpointInfo(ep, false)
			if got, want := info.Namespace+"."+info.Name, ep.RateLimitOperation(); got != want {
				t.Errorf("%s: the Rust server's operation is %q, the Go server's %q", name, got, want)
			}
		}
	}
}
