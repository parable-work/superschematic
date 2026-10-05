package tsreader

import (
	"strings"
	"testing"
)

// TestPublicRouteInAnAuthenticatedSetIsOpen: @publicRoute on a method of an
// Authenticated set opens that one route. The walker does not fold the
// set's Authenticated into it, so every generator sees a route that needs
// no caller (apigen's RequiresAuth is false), as the TypeScript server
// already served it.
func TestPublicRouteInAnAuthenticatedSetIsOpen(t *testing.T) {
	dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": `import { Authenticated, HttpMethod, publicRoute, rest } from "@superschematic/api";
export class ThingQueries extends Authenticated {
  @rest(HttpMethod.GET, "things")
  @publicRoute
  listThings(): string {
    throw new Error("schema declaration only");
  }
  @rest(HttpMethod.GET, "things/mine")
  myThings(): string {
    throw new Error("schema declaration only");
  }
}
`})
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ops := map[string][2]bool{}
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			ops[op.Name] = [2]bool{op.Public, op.Auth}
		}
	}
	if got := ops["listThings"]; got != [2]bool{true, false} {
		t.Errorf("listThings Public, Auth = %v, want true, false", got)
	}
	if got := ops["myThings"]; got != [2]bool{false, true} {
		t.Errorf("myThings Public, Auth = %v, want false, true", got)
	}
}

// TestPublicRouteWithACallerDecoratorIsRefused: @publicRoute and @auth,
// @requirePermission or @requireOwnership on the same method contradict
// each other, and the build says which.
func TestPublicRouteWithACallerDecoratorIsRefused(t *testing.T) {
	dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": `import { HttpMethod, auth, publicRoute, requireOwnership, requirePermission, rest } from "@superschematic/api";
export class ThingQueries {
  @rest(HttpMethod.GET, "a")
  @publicRoute
  @auth
  a(): string {
    throw new Error("schema declaration only");
  }
  @rest(HttpMethod.GET, "b")
  @publicRoute
  @requirePermission(["things.read"])
  b(): string {
    throw new Error("schema declaration only");
  }
  @rest(HttpMethod.GET, "c")
  @requireOwnership
  @publicRoute
  c(): string {
    throw new Error("schema declaration only");
  }
}
`})
	_, _, err := LoadService(dir)
	if err == nil {
		t.Fatal("expected the contradictions to be refused")
	}
	for _, want := range []string{
		"a.schema.ts:3:3: @publicRoute contradicts @auth on a: a public route needs no caller",
		"a.schema.ts:9:3: @publicRoute contradicts @requirePermission on b: a public route needs no caller",
		"a.schema.ts:15:3: @publicRoute contradicts @requireOwnership on c: a public route needs no caller",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%s", want, err)
		}
	}
}
