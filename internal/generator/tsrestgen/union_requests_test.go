package tsrestgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestGeneratedRouterAnswersUnionRequests type-checks the generated package
// for union-lists-api (sdktest.LoadUnionListsService) and sends its router
// union_requests.json's requests (sdktest.UnionRequestsPath) under bun
// (union_requests_runtime.test.ts), which records what it answers;
// sdktest.CheckUnionResponses holds the answers to the requests'
// expectations, as it holds the Go and Rust routers'. A union body
// argument is an object parse<Union>Json checks and parse<Union>FromJSON
// reads, and an input's union fields are validated as their members by
// the input's validator.
func TestGeneratedRouterAnswersUnionRequests(t *testing.T) {
	schema, err := sdktest.LoadUnionListsService()
	if err != nil {
		t.Fatal(err)
	}
	requests, err := sdktest.LoadUnionRequests()
	if err != nil {
		t.Fatal(err)
	}
	requestsPath, err := sdktest.UnionRequestsPath()
	if err != nil {
		t.Fatal(err)
	}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: sdktest.UnionListsService,
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tree := materializeAPI(t, apiFixture{name: sdktest.UnionListsService, schema: schema, endpoints: endpoints})
	tree.typeCheck(t)
	responses := filepath.Join(t.TempDir(), "responses.json")
	tree.env = append(tree.env, "UNION_REQUESTS="+requestsPath, "UNION_RESPONSES="+responses)
	tree.runTest(t, "union_requests_runtime.test.ts")
	problems, err := sdktest.CheckUnionResponses(requests, responses)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) > 0 {
		t.Fatalf("the TypeScript router answered union_requests.json otherwise:\n%s", strings.Join(problems, "\n"))
	}
}
