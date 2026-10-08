package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// demoJob is the job the demo API declares in TestStackRun, and its
// deployable.
const (
	demoJob     = "Sweep"
	demoJobName = "demo-api-sweep"
)

// TestStackRun runs a job of the demo tree on demand (D52): on the fake
// target through its job runner, with the image the last deploy rolled
// out, and refuses a run never deployed, a name that is no job, and a
// local environment stack dev does not run.
func TestStackRun(t *testing.T) {
	stackDir := demoTree(t)
	api := filepath.Join(filepath.Dir(stackDir), "demo-api", "src", "api.schema.yaml")
	src, err := os.ReadFile(api)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(api, append(src, []byte("jobs:\n  - name: "+demoJob+"\n    schedule: \"0 * * * *\"\n")...), 0o644))

	ext := &stacktest.Extension{}
	exts := []registry.Extension{ext}
	run := func(args ...string) (string, error) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		root := New(Config{}, exts...)
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetIn(strings.NewReader(""))
		root.SetArgs(append([]string{"stack"}, append(args, "--stack", stackDir)...))
		err := root.Execute()
		return stdout.String() + stderr.String(), err
	}

	_, err = run("run", "Staging", demoJobName)
	require.ErrorContains(t, err, "Staging was never deployed; deploy it, then run job "+demoJobName)

	useTerminal(t, &fakeTerminal{answers: []string{"key-1"}})
	jobImage := demoJobName + "=" + demoJobName + "@sha256:" + strings.Repeat("e", 64)
	_, err = run("deploy", "Staging", "--image", demoImage(1), "--image", jobImage)
	require.NoError(t, err)
	useTerminal(t, nil)

	n := len(ext.Provisioner.Calls())
	out, err := run("run", "Staging", demoJobName)
	require.NoError(t, err)
	assert.Contains(t, out, "run job "+demoJobName+" of Staging (image "+strings.TrimPrefix(jobImage, demoJobName+"=")+")")
	assert.Equal(t, []string{"run job " + demoJobName + ": " + strings.TrimPrefix(jobImage, demoJobName+"=")}, ext.Provisioner.Calls()[n:])

	ext.Jobs.Fail = map[string]error{demoJobName: fmt.Errorf("the run failed; its logs are in the console")}
	_, err = run("run", "Staging", demoJobName)
	require.ErrorContains(t, err, "the run failed")
	ext.Jobs.Fail = nil

	_, err = run("run", "Staging", "demo-api")
	require.ErrorContains(t, err, "environment Staging has no job demo-api (its jobs: "+demoJobName+")")

	// A local environment runs its job against the environment stack dev
	// runs; with none built, there is nothing to run against.
	_, err = run("run", "Dev", demoJobName)
	require.ErrorContains(t, err, "stack run runs a job against the environment stack dev runs: start it with superschematic stack dev --environment Dev")
}
