package cloudflare_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// workerdCallsFixture is the HTTP runtime's workerd fixture that runs the
// calls fixture's two Workers (runtime/http/typescript/test/workerd).
var workerdCallsFixture = filepath.Join("..", "..", "runtime", "http", "typescript", "test", "workerd", "fixtures", "calls", "config.capnp")

// capnpBinding is a binding of a Worker in a workerd config: its kind,
// `text`, `service`, `wrapped` or `hyperdrive`, and a text's or a service's
// value.
type capnpBinding struct{ kind, value string }

var (
	capnpWorker      = regexp.MustCompile(`(?m)^const (\w+) :Workerd\.Worker = \(`)
	capnpBindingLine = regexp.MustCompile(`\(name = "([A-Z0-9_]+)", (text|service|wrapped|hyperdrive) = ("(?:[^"\\]|\\.)*")?`)
)

// capnpUnquote is the contents of a Cap'n Proto text literal, its escapes
// undone: the inverse of capnpText.
func capnpUnquote(s string) string {
	s = strings.TrimSuffix(strings.TrimPrefix(s, `"`), `"`)
	return strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\n`, "\n").Replace(s)
}

// workerdBindings reads each Worker's bindings from a workerd config.
func workerdBindings(t *testing.T, path string) map[string]map[string]capnpBinding {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	out := map[string]map[string]capnpBinding{}
	starts := capnpWorker.FindAllStringSubmatchIndex(text, -1)
	for i, start := range starts {
		end := len(text)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		worker := text[start[2]:start[3]]
		out[worker] = map[string]capnpBinding{}
		for _, m := range capnpBindingLine.FindAllStringSubmatch(text[start[0]:end], -1) {
			out[worker][m[1]] = capnpBinding{kind: m[2], value: capnpUnquote(m[3])}
		}
	}
	return out
}

// TestWorkerdCallsFixtureHoldsTheGoldenBindings holds the workerd fixture
// that runs the calls fixture's two Workers to the bindings the platform
// gives them in Staging: each binding of each version by its name and
// kind, a plain or a secret text as a text, the pricing Worker's service
// binding as a service of that Worker, the bucket's r2_bucket binding as a
// stand-in, and each literal text with its value. A value the graph holds
// as an output, a key or a token, is the fixture's stand-in.
func TestWorkerdCallsFixtureHoldsTheGoldenBindings(t *testing.T) {
	env := resolveCalls(t, assemble(t), "Staging")
	fixture := workerdBindings(t, workerdCallsFixture)
	for worker, deployable := range map[string]string{"storefront": "shop-storefront", "pricing": "shop-pricing"} {
		got := fixture[worker]
		if got == nil {
			t.Errorf("the workerd fixture has no Worker %s", worker)
			continue
		}
		version := node(t, env, deployable+".version")
		want := map[string]bool{}
		for _, b := range version.Properties["bindings"].([]any) {
			b := b.(map[string]any)
			name := b["name"].(string)
			want[name] = true
			have, ok := got[name]
			if !ok {
				t.Errorf("the workerd fixture's %s has no binding %s", worker, name)
				continue
			}
			switch b["type"] {
			case "plain_text", "secret_text":
				if have.kind != "text" {
					t.Errorf("%s's %s is %s, want text", worker, name, have.kind)
				}
				if s, ok := b["text"].(string); ok && have.value != s {
					t.Errorf("%s's %s is %q, want %q", worker, name, have.value, s)
				}
			case "service":
				out, ok := b["service"].(ir.Output)
				callee := strings.TrimSuffix(out.Resource, ".worker")
				if !ok || have.kind != "service" || have.value != env.Deployable(callee).ResourceName {
					t.Errorf("%s's %s is a %s of %q, want a service of %v", worker, name, have.kind, have.value, b["service"])
				}
			case "r2_bucket":
				if have.kind != "wrapped" {
					t.Errorf("%s's %s is %s, want the R2 stand-in", worker, name, have.kind)
				}
			default:
				t.Errorf("%s's %s is of type %v, which the fixture does not stand in for", worker, name, b["type"])
			}
		}
		for name := range got {
			if !want[name] {
				t.Errorf("the workerd fixture's %s has binding %s, which %s's version does not", worker, name, deployable)
			}
		}
	}
}
