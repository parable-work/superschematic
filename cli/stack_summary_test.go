package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
)

// TestPrintLocalSummaryNamesTheSQLiteFile: a sqlite-first database's line
// in stack dev's summary is its file's sqlite: URL under the state
// directory (D64), beside a Postgres database's connection string.
func TestPrintLocalSummaryNamesTheSQLiteFile(t *testing.T) {
	env := &ir.ResolvedEnvironment{Stack: "notes-stack", Environment: "Dev", Resources: &ir.ResourceGraph{Resources: []*ir.Resource{
		{ID: "notes-db.database.notes-db", Type: local.TypeSQLiteDatabase, Properties: map[string]any{"name": "notes_db", "service": "notes-db", "file": "databases/notes_db.sqlite"}},
		{ID: "shop-db.database.shop-db", Type: local.TypeDatabase, Properties: map[string]any{"service": "shop-db", "url": "postgres://postgres@127.0.0.1:30001/shop_db?sslmode=disable"}},
	}}}
	var out bytes.Buffer
	printLocalSummary(&out, env, "/state", true)
	for _, want := range []string{
		"  database notes-db                 sqlite:/state/databases/notes_db.sqlite\n",
		"  database shop-db                  postgres://postgres@127.0.0.1:30001/shop_db?sslmode=disable\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the summary lacks %q:\n%s", want, out.String())
		}
	}
}

// TestPrintLocalSummaryNamesTheDerivers: a deriver's line says which
// schemas' derived fields it fills, and how many answers it runs at a
// time, or that they wait when the environment turns it off (D74).
func TestPrintLocalSummaryNamesTheDerivers(t *testing.T) {
	env := &ir.ResolvedEnvironment{Stack: "shop-stack", Environment: "Dev", Resources: &ir.ResourceGraph{}, Deployables: []*ir.ResolvedDeployable{
		{Name: "shop-cart-triage-tickets", Kind: ir.DeployableDeriver, Deriver: &ir.ResolvedDeriver{Schemas: []string{"Ticket"}, Instances: 1, Concurrency: 4}},
		{Name: "shop-cart-fill-all", Kind: ir.DeployableDeriver, Deriver: &ir.ResolvedDeriver{Schemas: []string{"Article", "Ticket"}}},
	}}
	var out bytes.Buffer
	printLocalSummary(&out, env, "/state", true)
	for _, want := range []string{
		"  deriver  shop-cart-triage-tickets fills the derived fields of Ticket, 4 at a time\n",
		"  deriver  shop-cart-fill-all       off: the derived fields of Article, Ticket wait\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the summary lacks %q:\n%s", want, out.String())
		}
	}
}

// TestPrintLocalSummaryNamesADevSite: a site with a dev server says it
// runs it, and where the APIs are on its origin (D82), unless
// --build-sites builds it, as a site without one is.
func TestPrintLocalSummaryNamesADevSite(t *testing.T) {
	env := &ir.ResolvedEnvironment{Stack: "shop-stack", Environment: "Dev", Resources: &ir.ResourceGraph{}, Deployables: []*ir.ResolvedDeployable{
		{Name: "shop-web", Kind: ir.DeployableSite, PublicAddress: "http://127.0.0.1:20001", Site: &ir.ResolvedSite{Dev: &ir.SiteDevConfig{Command: "dev"}}},
		{Name: "shop-admin", Kind: ir.DeployableSite, PublicAddress: "http://127.0.0.1:20002", Site: &ir.ResolvedSite{}},
	}}
	for _, tc := range []struct {
		devSites bool
		want     string
	}{
		{true, "  site     shop-web                 http://127.0.0.1:20001, its dev server (bun run dev) and its APIs under /api/<service>\n"},
		{false, "  site     shop-web                 http://127.0.0.1:20001\n"},
	} {
		var out bytes.Buffer
		printLocalSummary(&out, env, "/state", tc.devSites)
		for _, want := range []string{tc.want, "  site     shop-admin               http://127.0.0.1:20002\n"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("devSites %v: the summary lacks %q:\n%s", tc.devSites, want, out.String())
			}
		}
	}
}

// TestPrintLocalSummaryNamesAnOrigin: a site with an origin names the APIs
// of it under their paths, behind its dev server or its build (D91).
func TestPrintLocalSummaryNamesAnOrigin(t *testing.T) {
	origin := []*ir.SiteOriginAPI{{Service: "shop-api", Server: "shop-api", Path: "/api/shop-api"}, {Service: "shop-cart", Server: "shop-cart", Path: "/api/shop-cart"}}
	env := &ir.ResolvedEnvironment{Stack: "shop-stack", Environment: "Dev", Resources: &ir.ResourceGraph{}, Deployables: []*ir.ResolvedDeployable{
		{Name: "shop-web", Kind: ir.DeployableSite, PublicAddress: "http://127.0.0.1:20001", Site: &ir.ResolvedSite{Dev: &ir.SiteDevConfig{Command: "dev"}, Origin: origin}},
	}}
	for _, tc := range []struct {
		devSites bool
		want     string
	}{
		{true, "  site     shop-web                 http://127.0.0.1:20001, its dev server (bun run dev) and shop-api, shop-cart under /api/<service>\n"},
		{false, "  site     shop-web                 http://127.0.0.1:20001, its build and shop-api, shop-cart under /api/<service>\n"},
	} {
		var out bytes.Buffer
		printLocalSummary(&out, env, "/state", tc.devSites)
		if !strings.Contains(out.String(), tc.want) {
			t.Errorf("devSites %v: the summary lacks %q:\n%s", tc.devSites, tc.want, out.String())
		}
	}
}

// TestPrintLocalSummaryNamesTheApps: an app's line gives its address and
// the app class and API it is, or says it is off when the environment
// turns it off (D78).
func TestPrintLocalSummaryNamesTheApps(t *testing.T) {
	env := &ir.ResolvedEnvironment{Stack: "shop-stack", Environment: "Dev", Resources: &ir.ResourceGraph{}, Deployables: []*ir.ResolvedDeployable{
		{Name: "shop-stock-backoffice", Kind: ir.DeployableApp, Address: "http://127.0.0.1:21211", App: &ir.ResolvedApp{API: "shop-stock", Name: "Backoffice", Enabled: true}},
		{Name: "shop-orders-console", Kind: ir.DeployableApp, App: &ir.ResolvedApp{API: "shop-orders", Name: "Console"}},
	}}
	var out bytes.Buffer
	printLocalSummary(&out, env, "/state", false)
	for _, want := range []string{
		"  app      shop-stock-backoffice    http://127.0.0.1:21211, Backoffice of shop-stock\n",
		"  app      shop-orders-console      off\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the summary lacks %q:\n%s", want, out.String())
		}
	}
}
