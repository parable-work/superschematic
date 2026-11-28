package cloudflare_test

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/cloudflare"
	ir "github.com/parable-work/superschematic/ir"
)

// TestWakeRelayUnderWorkerd (#600) runs a drainer's wakes of the other
// drainers under the runtime's pinned workerd: the behavior ledger's
// embedding worker's QUEUE_WAKES and its WAKE_ service bindings, as its
// version holds them, each bound to the script every wake relay runs, the
// bytes the extension embeds, whose WORKFLOW is a stand-in that records
// each instance. testdata/workerd/wake-relay's waker installs the
// runtime's queue waker from those bindings, as drain.ts does, wakes each
// queue, and checks the instance, a wake the run covers sending none, the
// relay passing on the Workflow's refusal of an id it holds, and its
// refusal of a wake with no run id. The waker is bundled from the
// runtime's source with bun.
func TestWakeRelayUnderWorkerd(t *testing.T) {
	bin := workerdBinary(t)
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("SUPERSCHEMATIC_REQUIRE_TS_CHECKS") != "" {
			t.Fatal("bun is not on PATH")
		}
		t.Skip("bun is not on PATH")
	}
	env, errs := resolveOver(t, assemble(t), ledgerStack(), behaviorLedger(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	bindings := versionBindings(t, node(t, env, "finances-api-embed-line-items.version"))
	wakes, _ := bindings[ir.QueueWakesVariable]["text"].(string)
	if wakes == "" {
		t.Fatal("the embedding worker holds no QUEUE_WAKES")
	}
	var wakeBindings []string
	for _, name := range slices.Sorted(maps.Keys(bindings)) {
		if b := bindings[name]; strings.HasPrefix(name, "WAKE_") {
			if b["type"] != "service" {
				t.Fatalf("the embedding worker binds %s as %v, want a service binding of a relay", name, b)
			}
			wakeBindings = append(wakeBindings, `(name = "`+name+`", service = "relay"),`)
		}
	}
	if len(wakeBindings) < 2 {
		t.Fatalf("the embedding worker binds the relays %v, want the other two drainers'", wakeBindings)
	}
	dir := t.TempDir()
	for _, file := range []string{"relay.capnp", "workflow.js"} {
		data, err := os.ReadFile(filepath.Join("testdata", "workerd", "wake-relay", file))
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.NewReplacer("$QUEUE_WAKES", capnpText(wakes), "$WAKE_BINDINGS", strings.Join(wakeBindings, "\n    ")).Replace(string(data)))
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, cloudflare.WakeRelayModule), []byte(cloudflare.WakeRelayScript()), 0o644); err != nil {
		t.Fatal(err)
	}
	waker, err := filepath.Abs(filepath.Join("testdata", "workerd", "wake-relay", "waker.ts"))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command(bun, "build", "--target=browser", "--external=cloudflare:*", "--external=node:*", "--outfile", filepath.Join(dir, "drainer.js"), waker)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("bun build the waker: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "test", "relay.capnp")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workerd test: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "a drainer's wakes through the relay: 4 queues, one instance, a duplicate refused") {
		t.Errorf("the waker did not report its checks:\n%s", out)
	}
}
