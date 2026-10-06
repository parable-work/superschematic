package bindings_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/cli"
	"github.com/parable-work/superschematic/extensions/pulumi/bindings"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// demoTree writes a schemas root in the YAML data form, an API service and
// a stack that deploys it on stacktest's fake target in Staging and in a
// parameterized Preview, and returns the stack's directory.
func demoTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "schemas", "services")
	files := map[string]string{
		"demo-api/schema.config.yaml": `name: demo-api
kind: API
outputs:
  types:
    go: { enabled: true }
  api: { enabled: true }
`,
		"demo-api/src/api.schema.yaml": `name: demo-api
kind: API
types:
  OrderView:
    name: OrderView
    role: APIView
    fields:
      - name: id
        typeRef: { name: string }
        required: true
operationSets:
  - name: OrderQueries
    operations:
      - name: getOrder
        typeRef: { name: OrderView }
        required: true
        httpMethod: GET
        restPath: orders/:id
        arguments:
          - name: id
            typeRef: { name: string }
            required: true
`,
		"demo-stack/schema.config.yaml": `name: demo-stack
kind: Stack
outputs: {}
`,
		"demo-stack/src/stack.schema.yaml": `kind: Stack
types:
  Demo:
    name: Demo
    role: EmbeddedStruct
    stack:
      deploy:
        - { name: demo-api, kind: API }
  Staging:
    name: Staging
    role: EmbeddedStruct
    environment:
      target: fake
      values: { project: acme-staging, region: us-east1 }
  Preview:
    name: Preview
    role: EmbeddedStruct
    extends: Staging
    rawHeritage: { extends: Staging }
    environment:
      parameters: [pr]
`,
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "demo-stack")
}

// superschematic runs the core's CLI with stacktest's fake target linked.
func superschematic(t *testing.T, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	root := cli.New(cli.Config{}, &stacktest.Extension{})
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("superschematic %s: %v\n%s", strings.Join(args, " "), err, buf.String())
	}
	return buf.String()
}

// TestStackOutputsFileReads runs the flow of docs/stack-model.md, section
// 6.6: a build writes each environment's environment.json, `stack outputs
// --out` writes the run's outputs file beside it, and the generator reads
// both into a binding with the run's outputs.
func TestStackOutputsFileReads(t *testing.T) {
	stackDir := demoTree(t)
	out := t.TempDir()
	superschematic(t, "build", stackDir, "--out", out)

	envPath := stack.EnvironmentPath(out, "demo-stack", "Staging")
	outputsPath := filepath.Join(filepath.Dir(envPath), bindings.OutputsFile)
	superschematic(t, "stack", "outputs", "Staging", "--stack", stackDir, "--program-dir", t.TempDir(), "--out", outputsPath)

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	env, err := stack.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(outputsPath)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := bindings.UnmarshalOutputs(data)
	if err != nil {
		t.Fatalf("the file stack outputs --out wrote: %v\n%s", err, data)
	}
	if outputs.Version != bindings.OutputsVersion || outputs.Stack != "demo-stack" || outputs.Environment != "Staging" || outputs.Parameters != nil {
		t.Errorf("outputs file %+v", outputs)
	}
	// The fake provisioner reads each node's ID as its id output.
	if got := outputs.Resources["demo-api.service"]["id"]; got != "demo-api.service" {
		t.Errorf("demo-api.service's id is %v; outputs %v", got, outputs.Resources)
	}

	files, err := bindings.Generate("demostack", []bindings.Environment{{Resolved: env, Outputs: outputs}})
	if err != nil {
		t.Fatal(err)
	}
	if values := string(files[bindings.ValuesFile]); !strings.Contains(values, `"demo-api.service"`) || !strings.Contains(values, "var Staging = Environment{") {
		t.Errorf("the binding holds no value of Staging's outputs:\n%s", values)
	}

	// A member of a parameterized environment is a run of its own: its
	// outputs file names it and its parameter, and is no environment's
	// value.
	superschematic(t, "stack", "outputs", "Preview", "--param", "pr=7", "--stack", stackDir, "--program-dir", t.TempDir(), "--out", outputsPath)
	data, err = os.ReadFile(outputsPath)
	if err != nil {
		t.Fatal(err)
	}
	member, err := bindings.UnmarshalOutputs(data)
	if err != nil {
		t.Fatal(err)
	}
	if member.Environment != "Preview" || member.Parameters["pr"] != "7" {
		t.Errorf("the member's outputs file %+v", member)
	}
}
