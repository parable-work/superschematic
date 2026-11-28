package servergen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// goMCPGoldenRoot holds the entrypoint of a Go server whose API serves its
// MCP endpoint over a database with the runtime's wiring (#660).
const goMCPGoldenRoot = "testdata/golden/wiring/go-mcp"

// TestGoServerBuildsItsMCPEndpointGolden: the server of an API with
// outputs.api.mcp on builds its router with Config.MCP: the confirmation
// key its config holds (D93), the dispatcher's MCPCallGate, which holds an
// agent run's call to its autonomy by the tool's @autonomy (#621), and the
// calendar's and search's tools the API package's MCPTools serves (#616);
// its worker builds none of it, since a worker serves no request.
// Regenerate with:
//
//	go test ./internal/generator/servergen -run TestGoServerBuildsItsMCPEndpointGolden -update
func TestGoServerBuildsItsMCPEndpointGolden(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	withMCP := func(in servergen.Input) servergen.Input {
		in.APIs[0].Output.MCP = &apigen.GoMCP{RequiresAuth: true, Confirming: []string{"close_duty"}, ConfirmationKey: "McpConfirmationKey"}
		return in
	}
	in := withMCP(goWiringInput(repoRoot))
	s, err := servergen.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := servergen.Write(s, in.Dir); err != nil {
		t.Fatal(err)
	}
	compareGoldens(t, goMCPGoldenRoot, map[string]string{
		"server/" + servergen.MainFile: filepath.Join(in.Dir, servergen.MainFile),
	})

	worker := withMCP(goWiringInput(repoRoot))
	worker.Server = "workflow-notify"
	worker.Dir = filepath.Join(filepath.Dir(in.Dir), worker.Server)
	worker.Worker = &servergen.WorkerInput{Name: "Notify"}
	w, err := servergen.Plan(worker)
	if err != nil {
		t.Fatal(err)
	}
	if err := servergen.Write(w, worker.Dir); err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile(filepath.Join(worker.Dir, servergen.MainFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(main), "MCPOptions") {
		t.Errorf("the worker builds an MCP endpoint:\n%s", main)
	}
}
