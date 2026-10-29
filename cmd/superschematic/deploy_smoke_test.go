package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite examples/deploy-smoke's environment goldens")

// deploySmoke is the example the live deploy smoke deploys (D97).
const deploySmoke = "../../examples/deploy-smoke"

// TestDeploySmokeEnvironments builds examples/deploy-smoke's stack with
// the installed binary, over a copy of its schemas, and holds its Gcp and
// Cloudflare environment.json to testdata/<environment>.environment.json
// beside the example: what scripts/smoke.sh deploys, with the committed
// placeholder accounts. Beyond the goldens it reads what the smoke proves
// live: on gcp, smoke-api behind the site's load balancer alone, under
// /api/smoke-api with its prefix taken off, and the job's name shortened
// to its account id's 30 characters, its whole name in a label (D91,
// D97); on Cloudflare, smoke-db on Hyperdrive, the worker draining at
// each minute's Cron firing, and the site's Worker taking the API's paths
// first and reaching it through a service binding (D91, D92).
func TestDeploySmokeEnvironments(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	example := filepath.Join(repoRoot, "examples", "deploy-smoke")
	project := t.TempDir()
	schemasRoot := filepath.Join(project, "schemas")
	copyTree(t, filepath.Join(example, "schemas", "services"), filepath.Join(schemasRoot, "services"))
	// The naming file's paths are relative to the project and the
	// tsconfig's to the schemas root: each points from the copy back at
	// this checkout.
	rel, err := filepath.Rel(project, repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	rel = filepath.ToSlash(rel)
	for name, replace := range map[string]*strings.Replacer{
		"superschematic.toml": strings.NewReplacer(`"../../`, `"`+rel+"/", `"../.."`, `"`+rel+`"`),
		"tsconfig.base.json":  strings.NewReplacer(`"../../../`, `"../`+rel+"/"),
	} {
		data, err := os.ReadFile(filepath.Join(example, "schemas", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(schemasRoot, name), []byte(replace.Replace(string(data))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(project)
	outputRoot := t.TempDir()
	run(t, "build", filepath.Join(schemasRoot, "services", "smoke-stack"), "--out", outputRoot)

	envs := map[string]map[string]any{}
	for _, name := range []string{"Gcp", "Cloudflare"} {
		got, err := os.ReadFile(filepath.Join(outputRoot, "stack", "smoke-stack", name, "environment.json"))
		if err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join(example, "testdata", name+".environment.json")
		if *update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run go test -update to write it)", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s's environment.json differs from %s; run go test -update and review the diff", name, golden)
		}
		var env map[string]any
		if err := json.Unmarshal(got, &env); err != nil {
			t.Fatal(err)
		}
		envs[name] = env
	}

	gcp := envs["Gcp"]
	service := props(t, gcp, "smoke-api.service")
	if service["ingress"] != "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER" || service["invokerIamDisabled"] != true {
		t.Errorf("smoke-api's service takes %v with its invoker check off %v, want the load balancer's traffic alone (D91)", service["ingress"], service["invokerIamDisabled"])
	}
	urlMap, _ := json.Marshal(props(t, gcp, "smoke-web.url-map"))
	for _, want := range []string{`"fullPathMatch":"/api/smoke-api"`, `"prefixMatch":"/api/smoke-api/"`, `"pathPrefixRewrite":"/"`, `"headerValue":"/api/smoke-api"`} {
		if !bytes.Contains(urlMap, []byte(want)) {
			t.Errorf("the site's URL map holds no %s (D91)", want)
		}
	}
	job := props(t, gcp, "smoke-api-sync-all-linked-accounts.job")
	account := props(t, gcp, "smoke-api-sync-all-linked-accounts.account")
	if name, _ := job["name"].(string); len(name) > 30 || !strings.HasPrefix(name, "smoke-api-sync-all-") || account["accountId"] != name {
		t.Errorf("the job is %v and its account %v, want one name of a prefix and a hash in 30 characters (D97)", job["name"], account["accountId"])
	}
	if labels, _ := json.Marshal(job["labels"]); string(labels) != `{"superschematic-deployable":"smoke-api-sync-all-linked-accounts"}` {
		t.Errorf("the job's labels are %s, want its deployable's whole name (D97)", labels)
	}

	cf := envs["Cloudflare"]
	platforms := map[string]string{}
	for _, d := range cf["deployables"].([]any) {
		d := d.(map[string]any)
		platforms[d["name"].(string)] = d["platform"].(string)
	}
	for name, want := range map[string]string{
		"smoke-db":                   "cloudflare.hyperdrive",
		"smoke-api":                  "cloudflare.workers",
		"smoke-api-drain-line-items": "cloudflare.drainer",
		"smoke-web":                  "cloudflare.sites",
	} {
		if platforms[name] != want {
			t.Errorf("%s is on %s, want %s (D92)", name, platforms[name], want)
		}
	}
	if cron, _ := json.Marshal(props(t, cf, "smoke-api-drain-line-items.schedule")["schedules"]); string(cron) != `[{"cron":"* * * * *"}]` {
		t.Errorf("the drainer fires on %s, want every minute (D92)", cron)
	}
	site := props(t, cf, "smoke-web.version")
	first, _ := json.Marshal(site["assets"])
	bindings, _ := json.Marshal(site["bindings"])
	if !bytes.Contains(first, []byte(`"/api/smoke-api","/api/smoke-api/*"`)) || !bytes.Contains(bindings, []byte(`"SITE_API_SMOKE_API"`)) {
		t.Errorf("the site's Worker takes %s first and binds %s, want smoke-api's paths and SITE_API_SMOKE_API (D91)", first, bindings)
	}
	for _, d := range []string{"smoke-api", "smoke-api-drain-line-items"} {
		version, _ := json.Marshal(props(t, cf, d+".version")["bindings"])
		if !bytes.Contains(version, []byte(`"type":"hyperdrive"`)) {
			t.Errorf("%s's version binds no Hyperdrive config: %s (D92)", d, version)
		}
	}
}

// props are the properties of the resource id of an environment.json.
func props(t *testing.T, env map[string]any, id string) map[string]any {
	t.Helper()
	resources := env["resources"].(map[string]any)["resources"].([]any)
	i := slices.IndexFunc(resources, func(r any) bool { return r.(map[string]any)["id"] == id })
	if i < 0 {
		t.Fatalf("%s %s has no resource %s", env["stack"], env["environment"], id)
	}
	return resources[i].(map[string]any)["properties"].(map[string]any)
}
