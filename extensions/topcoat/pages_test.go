package topcoat_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/topcoat"
	"github.com/parable-work/superschematic/internal/testpaths"
	"github.com/parable-work/superschematic/registry"
)

// pagesService is the extension's own fixture for generated pages: a
// resource of every shape its operations describe, a nested one, one only
// some callers list, and an operation that fits none.
const pagesService = "fixture-pages-api"

// pagesOutputs are rustOutputs with the pages on, at the default prefix.
func pagesOutputs() map[string]any {
	outputs := rustOutputs()
	outputs["topcoat"] = map[string]any{"enabled": true, "pages": map[string]any{"enabled": true}}
	return outputs
}

// livePagesOutputs are pagesOutputs with the lists live: a list with a
// filter renders through a shard.
func livePagesOutputs() map[string]any {
	outputs := rustOutputs()
	outputs["topcoat"] = map[string]any{"enabled": true, "pages": map[string]any{"enabled": true, "live": true}}
	return outputs
}

// TestPagesServeATopcoatApp builds fixture-pages-api's crates with the
// pages on and runs cargo clippy, then cargo test, with pagesAppTest: an
// app discovers the pages; the index and the nav list what the caller may
// list, the projects' links after their type's icon and the others
// without one, and the trail names an item by its title; a list filters,
// links its rows to their items, names their owners by the app's choices
// and offers its new page to a writer; an item's page shows its detail,
// its edit and delete links, its action's form and its tasks' section;
// the new, edit, delete and action pages post, redirect (303) and refuse
// with the Refusal's status, 422 at the control, the new page's owner a
// select of the choices, the edit form filled from the item and a
// conflict (409) the form's message; a nested resource's pages live under
// its parent's item, and one under a collection without pages has no
// place in the nav; a refused caller keeps the route's status: 401 with a
// sign-in link that comes back, 403 and 404, a route's value that does
// not decode 404; an untitled document's row links its noun and id; the
// drafts' list at documents/drafts is its own in the nav and the trail,
// never a document; a document's comments are a collection under it,
// which no PUT or DELETE of theirs, nor the DELETE of its cover, turns
// into a form; and a document starts from an optional input that its
// edit page requires. Enter in each form a page renders presses its
// default button (enterHelper).
func TestPagesServeATopcoatApp(t *testing.T) {
	cargoTestCrate(t, pagesService, pagesAppTest)
}

// TestTheNavShowsATypesIcon builds fixture-pages-api, whose Project
// declares an icon, and fixture-console-api, whose types declare none
// (D44, amended): the projects' links, the list's and the archived
// list's, whose rows are projects, carry it for the nav and the index,
// which render it before the label; the other links carry none; and a
// crate whose resources declare no icon has no icon in its links.
func TestTheNavShowsATypesIcon(t *testing.T) {
	files, _ := readCrate(t, registry.DefaultNaming(), pagesService, pagesOutputs())
	pages := files["pages.rs"]
	for _, want := range []string{
		`let link = Link { href: "/console/projects", label: "Projects", icon: Some("folder") };`,
		`let link = Link { href: "/console/projects/archived", label: "Archived", icon: Some("folder") };`,
		`let link = Link { href: "/console/members", label: "Members", icon: None };`,
		`<span class="ss-icon" data-icon=(icon) aria-hidden="true"></span>`,
	} {
		if !strings.Contains(pages, want) {
			t.Errorf("fixture-pages-api's pages.rs lacks %s", want)
		}
	}
	if n := strings.Count(pages, `data-icon=(icon)`); n != 2 {
		t.Errorf("fixture-pages-api's pages.rs renders an icon %d times, want the nav's and the index's", n)
	}
	files, _ = readCrate(t, registry.DefaultNaming(), consoleService, livePagesOutputs())
	for _, unwanted := range []string{"data-icon", "icon:"} {
		if strings.Contains(files["pages.rs"], unwanted) {
			t.Errorf("fixture-console-api's pages.rs, whose types declare no icon, has %s", unwanted)
		}
	}
}

// TestPagesServeAnAPIWithoutCallers does the same for fixture-args-api with
// the pages on and argsPagesAppTest: an API whose operations need no
// caller lists its orders on the index and their page, filtered by the
// query, each linked; an order's page has a form per action, each of which
// posts and redirects, or renders the page again with the route's 409 as
// the form's message or with 422 at the control of JSON that does not
// parse; and a review, whose product nothing lists or reads, has no page.
func TestPagesServeAnAPIWithoutCallers(t *testing.T) {
	cargoTestCrateWith(t, argsService, argsPagesAppTest, crateOptions{outputs: pagesOutputs()})
}

// readCrate builds service with outputs into a new root and reads the
// files of its Topcoat crate, by name under src, with its build log.
func readCrate(t *testing.T, names registry.Naming, service string, outputs map[string]any) (map[string]string, string) {
	t.Helper()
	root := testpaths.TempDir(t)
	var log strings.Builder
	if _, err := buildWithNaming(t, names, service, root, outputs, &log); err != nil {
		t.Fatalf("build %s: %v", service, err)
	}
	files := map[string]string{}
	entries, err := os.ReadDir(filepath.Join(topcoat.Dir(root, service), "src"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(topcoat.Dir(root, service), "src", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name()] = string(data)
	}
	return files, log.String()
}

// TestPagesAreOffByDefault builds fixture-pages-api without pages in its
// config: its crate has no pages module, though its records, forms and
// views are written, and its tables still take hrefs.
func TestPagesAreOffByDefault(t *testing.T) {
	files, _ := readCrate(t, registry.DefaultNaming(), pagesService, rustOutputs())
	if _, ok := files["pages.rs"]; ok {
		t.Error("pages.rs written without outputs.topcoat.pages")
	}
	if strings.Contains(files["lib.rs"], "mod pages") {
		t.Error("lib.rs declares pages without outputs.topcoat.pages")
	}
	if !strings.Contains(files["views.rs"], "    rows: Vec<ProjectRecord>,\n    #[default] choices: Choices,\n    #[default] hrefs: Vec<String>,\n") {
		t.Error("views.rs's project_table takes no hrefs")
	}

	off := rustOutputs()
	off["topcoat"] = map[string]any{"enabled": true, "pages": map[string]any{"enabled": false, "prefix": "/admin"}}
	if files, _ := readCrate(t, registry.DefaultNaming(), pagesService, off); files["pages.rs"] != "" {
		t.Error("pages.rs written with pages.enabled false")
	}
}

// TestThePagesServeUnderTheirPrefix builds fixture-pages-api with the
// pages under /admin/shop: every page, the layout and the index are
// there, and the build log says which operation has no page and why.
func TestThePagesServeUnderTheirPrefix(t *testing.T) {
	outputs := rustOutputs()
	outputs["topcoat"] = map[string]any{"enabled": true, "pages": map[string]any{"enabled": true, "prefix": "/admin/shop"}}
	files, log := readCrate(t, registry.DefaultNaming(), pagesService, outputs)
	pages := files["pages.rs"]
	for _, want := range []string{
		`pub const PREFIX: &str = "/admin/shop";`,
		`#[layout("/admin/shop")]`,
		`#[page("/admin/shop")]`,
		`#[page("/admin/shop/projects")]`,
		`#[page("/admin/shop/projects/{project_id}")]`,
		`#[page(POST "/admin/shop/projects/{project_id}/archive")]`,
		`#[page("/admin/shop/projects/{project_id}/tasks/{task_id}")]`,
	} {
		if !strings.Contains(pages, want) {
			t.Errorf("pages.rs lacks %s", want)
		}
	}
	if strings.Contains(pages, "/console") {
		t.Error("pages.rs serves a page under /console")
	}
	if !strings.Contains(files["lib.rs"], "pub mod pages;") {
		t.Error("lib.rs does not declare pages")
	}
	if want := "no page for viewer.me: a GET of auth/me, a path that ends in a word, is a list only when it returns a list of records"; !strings.Contains(log, want) {
		t.Errorf("the build log lacks %q:\n%s", want, log)
	}
}

// TestABadPagesConfigIsRefused refuses a prefix the pages cannot be served
// under, pages that are not an object, and pages without the forms or the
// display components they are built on.
func TestABadPagesConfigIsRefused(t *testing.T) {
	for _, prefix := range []string{"console", "/console/", "/", "/api", "/api/console", "/_superschematic", "/con{id}"} {
		outputs := rustOutputs()
		outputs["topcoat"] = map[string]any{"enabled": true, "pages": map[string]any{"enabled": true, "prefix": prefix}}
		if _, err := buildService(t, pagesService, testpaths.TempDir(t), outputs); err == nil || !strings.Contains(err.Error(), "prefix") {
			t.Errorf("prefix %q: %v, want it refused", prefix, err)
		}
	}
	for _, topcoatConfig := range []map[string]any{
		{"enabled": true, "pages": true},
		{"enabled": true, "pages": map[string]any{"enabled": true, "layout": "/x"}},
		{"enabled": true, "forms": false, "pages": map[string]any{"enabled": true}},
		{"enabled": true, "views": false, "pages": map[string]any{"enabled": true}},
		{"enabled": true, "records": false, "pages": map[string]any{"enabled": true}},
	} {
		outputs := rustOutputs()
		outputs["topcoat"] = topcoatConfig
		if _, err := buildService(t, pagesService, testpaths.TempDir(t), outputs); err == nil {
			t.Errorf("outputs.topcoat %v: built, want it refused", topcoatConfig)
		}
	}
}

// TestTheNamingFileTurnsPagesOn builds fixture-pages-api, whose config has
// no outputs.topcoat, under a superschematic.toml whose [extension.topcoat]
// lists it with a pages_prefix: its crate has the pages under it, as
// outputs.topcoat.pages would give it. A table of prefixes turns on the
// pages of the listed services it names, each under its own, and leaves
// the others' off. A bad prefix fails assembly, and so do one prefix for
// several services, a service the table does not list, and two prefixes
// one of which is, or is under, the other: an app that links two crates
// would register a page twice.
func TestTheNamingFileTurnsPagesOn(t *testing.T) {
	names, err := registry.ParseNaming([]byte("[extension.topcoat]\nservices = [\"fixture-pages-api\"]\npages_prefix = \"/admin\"\n"), "superschematic.toml")
	if err != nil {
		t.Fatalf("ParseNaming: %v", err)
	}
	outputs := rustOutputs()
	delete(outputs, "topcoat")
	files, _ := readCrate(t, names, pagesService, outputs)
	if !strings.Contains(files["pages.rs"], `pub const PREFIX: &str = "/admin";`) {
		t.Error("a pages_prefix in the table wrote no pages under it")
	}
	listed, err := registry.ParseNaming([]byte("[extension.topcoat]\nservices = [\"fixture-pages-api\"]\n"), "superschematic.toml")
	if err != nil {
		t.Fatalf("ParseNaming: %v", err)
	}
	if files, _ := readCrate(t, listed, pagesService, outputs); files["pages.rs"] != "" {
		t.Error("a table without pages_prefix wrote pages")
	}

	each, err := registry.ParseNaming([]byte(`[extension.topcoat]
services = ["fixture-pages-api", "fixture-args-api"]
pages_prefix = { "fixture-pages-api" = "/admin/projects" }
`), "superschematic.toml")
	if err != nil {
		t.Fatalf("ParseNaming: %v", err)
	}
	if files, _ := readCrate(t, each, pagesService, outputs); !strings.Contains(files["pages.rs"], `pub const PREFIX: &str = "/admin/projects";`) {
		t.Error("a table of prefixes wrote no pages under the service's")
	}
	if files, _ := readCrate(t, each, argsService, outputs); files["lib.rs"] == "" || files["pages.rs"] != "" {
		t.Errorf("a listed service the prefixes do not name: lib.rs %d bytes, pages.rs %d, want a crate without pages", len(files["lib.rs"]), len(files["pages.rs"]))
	}

	for table, want := range map[string]string{
		"pages_prefix = \"/api\"":                                                            "under /api",
		"pages_prefix = true":                                                                "must be a path or a table",
		"services = [\"a\", \"b\"]\npages_prefix = \"/console\"":                             `give each service its own: pages_prefix = { "a" = "/console", ... }`,
		"services = [\"a\"]\npages_prefix = { b = \"/console\" }":                            `names "b", which services does not list`,
		"services = [\"a\"]\npages_prefix = { a = 1 }":                                       "pages_prefix.a must be a path",
		"services = [\"a\"]\npages_prefix = { a = \"/console/\" }":                           "pages_prefix.a",
		"services = [\"a\", \"b\"]\npages_prefix = { a = \"/console\", b = \"/console\" }":   "overlap",
		"services = [\"a\", \"b\"]\npages_prefix = { a = \"/console\", b = \"/console/b\" }": "overlap",
	} {
		names, err := registry.ParseNaming([]byte("[extension.topcoat]\n"+table+"\n"), "superschematic.toml")
		if err != nil {
			t.Fatalf("ParseNaming: %v", err)
		}
		if _, err := registry.Assemble(names, topcoat.Extension{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("[extension.topcoat] %s: %v, want it refused with %q", table, err, want)
		}
	}
	// Two services, each under its own prefix, a sibling of the other's.
	names, err = registry.ParseNaming([]byte("[extension.topcoat]\nservices = [\"a\", \"b\"]\npages_prefix = { a = \"/console/a\", b = \"/console/b\" }\n"), "superschematic.toml")
	if err != nil {
		t.Fatalf("ParseNaming: %v", err)
	}
	if _, err := registry.Assemble(names, topcoat.Extension{}); err != nil {
		t.Errorf("two services' prefixes side by side: %v", err)
	}
}

// TestLiveListsAreOptIn builds fixture-pages-api with the pages on: without
// live, a list's filter is a component and the crate has no shard and
// loads nothing of Topcoat's runtime; with it, each list with a filter is a
// shard at its own path under the prefix, the projects' and a project's
// tasks', and a list without a filter, the documents', stays a component.
// [extension.topcoat]'s pages_live turns them on too, and needs a
// pages_prefix.
func TestLiveListsAreOptIn(t *testing.T) {
	files, _ := readCrate(t, registry.DefaultNaming(), pagesService, pagesOutputs())
	for _, unwanted := range []string{"#[shard", "topcoat::runtime", "_live", "signal("} {
		if strings.Contains(files["pages.rs"], unwanted) {
			t.Errorf("pages.rs without live lists has %s", unwanted)
		}
	}
	if !strings.Contains(files["pages.rs"], "#[component]\nasync fn projects_list(cx: &Cx, filter: ProjectListProjectsArgsForm, next: String)") {
		t.Error("pages.rs without live lists renders no projects_list component")
	}
	live, _ := readCrate(t, registry.DefaultNaming(), pagesService, livePagesOutputs())
	for _, want := range []string{
		"use topcoat::runtime::{Event, shard, signal};",
		"#[shard(\"/console/_live/projects\")]\nasync fn projects_list(cx: &Cx, query: String, next: String)",
		"#[shard(\"/console/_live/projects_tasks\")]\nasync fn projects_tasks_list(cx: &Cx, project_id: String, query: String, next: String)",
		"pub async fn projects_list_page(cx: &Cx) -> topcoat::Result<impl View> {",
		"Ok(_) => projects_list(query: query_of(cx), next: next),",
		"#[component]\nasync fn documents_list(cx: &Cx, next: String)",
	} {
		if !strings.Contains(live["pages.rs"], want) {
			t.Errorf("pages.rs with live lists lacks %q", want)
		}
	}

	names, err := registry.ParseNaming([]byte("[extension.topcoat]\nservices = [\"fixture-pages-api\"]\npages_prefix = \"/admin\"\npages_live = true\n"), "superschematic.toml")
	if err != nil {
		t.Fatalf("ParseNaming: %v", err)
	}
	outputs := rustOutputs()
	delete(outputs, "topcoat")
	if files, _ := readCrate(t, names, pagesService, outputs); !strings.Contains(files["pages.rs"], `#[shard("/admin/_live/projects")]`) {
		t.Error("pages_live in the table wrote no live list")
	}
	for table, want := range map[string]string{
		"services = [\"a\"]\npages_live = true":                                 "has no pages_prefix",
		"services = [\"a\"]\npages_prefix = \"/console\"\npages_live = \"yes\"": "pages_live must be true or false",
	} {
		names, err := registry.ParseNaming([]byte("[extension.topcoat]\n"+table+"\n"), "superschematic.toml")
		if err != nil {
			t.Fatalf("ParseNaming: %v", err)
		}
		if _, err := registry.Assemble(names, topcoat.Extension{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("[extension.topcoat] %s: %v, want it refused with %q", table, err, want)
		}
	}
}

// TestPagesThatWouldMeetAreLeftOut builds fixture-pages-api, whose
// projects/new list would be served at the projects' new page and whose
// projects/{id}/edit and projects/{id}/delete lists at a project's edit
// and delete pages: each is left out, with its reason in the build log,
// and its collection has no place in the trail, so the app's router never
// meets a page twice. The pages' route controls name no page of
// fixture-pages-api, whose routes have none.
func TestPagesThatWouldMeetAreLeftOut(t *testing.T) {
	files, log := readCrate(t, registry.DefaultNaming(), pagesService, livePagesOutputs())
	for _, want := range []string{
		"no page for project.listNewProjects: its collection, projects/new, has no pages, since the list of projects/new would be served at GET projects/new, the new page of projects",
		"no page for project.listProjectEdits: its collection, projects/{}/edit, has no pages, since the list of projects/{}/edit would be served at GET projects/{}/edit, the edit page of an item of projects",
		"no page for project.listProjectDeletions: its collection, projects/{}/delete, has no pages, since the list of projects/{}/delete would be served at GET projects/{}/delete, the delete page of an item of projects",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the build log lacks %q:\n%s", want, log)
		}
	}
	pages := files["pages.rs"]
	for _, unwanted := range []string{`words: &["projects", "new"]`, `words: &["edit"]`, `words: &["delete"]`, "projects_new_list", "projects_edit_list", "route_controls"} {
		if strings.Contains(pages, unwanted) {
			t.Errorf("pages.rs has %s", unwanted)
		}
	}
	if strings.Count(pages, `#[page("/console/projects/new")]`) != 1 || strings.Count(pages, `#[page("/console/projects/{project_id}/edit")]`) != 1 {
		t.Error("pages.rs registers the projects' new page or a project's edit page other than once")
	}
}

// TestATableLinksItsRows builds fixture-views-api, whose crate has no
// pages: a table's hrefs link each row's title cell, or its first cell for
// a type whose columns hold no title, and a row without an href, or past
// their end, links nowhere. A linked cell without text links the type's
// noun, and the row's id when it holds one, in its place.
func TestATableLinksItsRows(t *testing.T) {
	files, _ := readCrate(t, registry.DefaultNaming(), viewsService, rustOutputs())
	views := files["views.rs"]
	linked := func(field, rustField string) string {
		value := "(text_of(&choices, \"" + field + "\", &row." + rustField + "))"
		return "\n                            if let Some(href) = &href {\n                                <a href=(href)>\n" +
			"                                    if let Some(untitled) = untitled {\n                                        (untitled)\n" +
			"                                    } else {\n                                        " + value + "\n                                    }\n" +
			"                                </a>\n                            } else {\n                                " + value + "\n"
	}
	for _, want := range []string{
		"pub async fn order_view_table(\n    rows: Vec<OrderViewRecord>,\n    #[default] choices: Choices,\n    #[default] hrefs: Vec<String>,\n)",
		"let href = hrefs.get(index).filter(|href| !href.is_empty()).cloned();",
		"<th scope=\"row\" data-field=\"reference\">" + linked("reference", "reference"),
		"<td data-field=\"text\">" + linked("text", "text"),
		// The title's absence, and blank text, name the row by its noun and
		// id, or by its noun alone.
		`let untitled = (href.is_some() && row.reference.trim().is_empty()).then(|| format!("Order {}", row.id));`,
		`let untitled = (href.is_some() && row.text.trim().is_empty()).then_some("Comment");`,
	} {
		if !strings.Contains(views, want) {
			t.Errorf("views.rs lacks %q", want)
		}
	}
	if _, ok := files["pages.rs"]; ok {
		t.Error("fixture-views-api's crate has pages")
	}
}

// pagesAppTest is tests/app.rs of fixture-pages-api's Topcoat crate.
const pagesAppTest = `use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

use async_trait::async_trait;
use http::request::Parts;
use schemas_fixture_pages_api_topcoat::api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use schemas_fixture_pages_api_topcoat::api::{
    types, DocumentClearCommentsArgs, DocumentCreateDocumentArgs, DocumentGetDocumentArgs, DocumentImplementation,
    DocumentListCommentsArgs, DocumentRemoveCoverArgs, DocumentReplaceCommentsArgs, DocumentReplaceDocumentArgs, Implementations,
    MemberImplementation, ProjectArchiveProjectArgs, ProjectCreateProjectArgs, ProjectDeleteProjectArgs, ProjectGetProjectArgs,
    ProjectImplementation, ProjectListProjectDeletionsArgs, ProjectListProjectEditsArgs, ProjectListProjectsArgs,
    ProjectUpdateProjectArgs, ShelfGetShelfArgs, ShelfImplementation, TaskAddTaskArgs, TaskGetTaskArgs, TaskImplementation,
    TaskListLabelledTasksArgs, TaskListTasksArgs, ViewerImplementation,
};
use schemas_fixture_pages_api_topcoat::choices::Choices;
use schemas_fixture_pages_api_topcoat::pages::Settings;
use schemas_fixture_pages_api_topcoat::{PageAuthenticator, RouterBuilderFixturePagesApiExt};
use topcoat::context::Cx;
use topcoat::router::request::parts;
use topcoat::router::{header, to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};

#[path = "support/enter.rs"]
mod enter;

const APOLLO: &str = "8d1f6c9e-0000-4000-8000-000000000001";
const GEMINI: &str = "8d1f6c9e-0000-4000-8000-000000000002";
const ROCKET: &str = "8d1f6c9e-0000-4000-8000-000000000101";
const NOWHERE: &str = "8d1f6c9e-0000-4000-8000-000000000999";

fn uuid(text: &str) -> types::IdentityUUID {
    serde_json::from_value(serde_json::json!(text)).unwrap()
}

// An id as the API's JSON writes it, which a row's link and a new item's
// page hold.
fn wire(text: &str) -> String {
    serde_json::to_value(uuid(text)).unwrap().as_str().unwrap().to_owned()
}

// The projects, their tasks, the documents and the members, in memory, as
// one app's requests change them, and the documents each read asked for.
struct Store {
    projects: Mutex<Vec<types::Project>>,
    tasks: Mutex<Vec<(types::IdentityUUID, types::Task)>>,
    documents: Mutex<Vec<types::Document>>,
    read: Mutex<Vec<String>>,
    next: Mutex<u32>,
}

fn document(id: &str, title: Option<&str>) -> types::Document {
    types::Document { id: id.to_owned(), title: title.map(str::to_owned), body: None }
}

impl Store {
    fn new() -> Arc<Self> {
        Arc::new(Store {
            projects: Mutex::new(vec![
                types::Project {
                    id: uuid(APOLLO),
                    name: "Apollo".to_owned(),
                    owner_id: "ada".to_owned(),
                    status: types::ProjectStatus::Active,
                    budget: 1200.5,
                    description: Some("To the moon".to_owned()),
                    due_on: Some("2026-12-01".to_owned()),
                    archived: false,
                    tags: Some(vec!["moon".to_owned(), "rocket".to_owned()]),
                    milestones: Some(vec![types::Milestone { title: "Launch".to_owned(), due_on: Some("2026-07-16".to_owned()) }]),
                },
                types::Project {
                    id: uuid(GEMINI),
                    name: "Gemini".to_owned(),
                    owner_id: "grace".to_owned(),
                    status: types::ProjectStatus::Planned,
                    budget: 300.0,
                    description: None,
                    due_on: None,
                    archived: false,
                    tags: None,
                    milestones: None,
                },
            ]),
            tasks: Mutex::new(vec![(
                uuid(APOLLO),
                types::Task { id: uuid(ROCKET), title: "Build the rocket".to_owned(), done: false },
            )]),
            documents: Mutex::new(vec![document("intro", None), document("guide", Some("Guide"))]),
            read: Mutex::new(Vec::new()),
            next: Mutex::new(500),
        })
    }

    fn new_id(&self) -> types::IdentityUUID {
        let mut next = self.next.lock().unwrap();
        *next += 1;
        uuid(&format!("8d1f6c9e-0000-4000-8000-000000000{next}"))
    }

    fn project(&self, id: &types::IdentityUUID) -> Result<types::Project, ApiError> {
        let projects = self.projects.lock().unwrap();
        projects.iter().find(|project| project.id == *id).cloned().ok_or_else(|| ApiError::not_found("No such project"))
    }
}

struct Shop(Arc<Store>);

#[async_trait]
impl ProjectImplementation for Shop {
    async fn list_projects(&self, _ctx: RequestContext, args: ProjectListProjectsArgs) -> Result<Vec<types::Project>, ApiError> {
        let projects = self.0.projects.lock().unwrap();
        let statuses = args.statuses.unwrap_or_default();
        Ok(projects.iter().filter(|project| statuses.is_empty() || statuses.contains(&project.status)).cloned().collect())
    }
    async fn get_project(&self, _ctx: RequestContext, args: ProjectGetProjectArgs) -> Result<types::Project, ApiError> {
        self.0.project(&args.id)
    }
    async fn create_project(&self, _ctx: RequestContext, args: ProjectCreateProjectArgs) -> Result<types::Project, ApiError> {
        let project = types::Project {
            id: self.0.new_id(),
            name: args.input.name,
            owner_id: args.input.owner_id,
            status: args.input.status,
            budget: args.input.budget,
            description: args.input.description,
            due_on: None,
            archived: false,
            tags: args.input.tags,
            milestones: None,
        };
        self.0.projects.lock().unwrap().push(project.clone());
        Ok(project)
    }
    async fn update_project(&self, _ctx: RequestContext, args: ProjectUpdateProjectArgs) -> Result<types::Project, ApiError> {
        let mut projects = self.0.projects.lock().unwrap();
        let project = projects.iter_mut().find(|project| project.id == args.id).ok_or_else(|| ApiError::not_found("No such project"))?;
        let notify = if args.input.notify_owner == Some(true) { "notified" } else { "quiet" };
        project.name = args.input.name;
        project.owner_id = args.input.owner_id;
        project.status = args.input.status;
        project.budget = args.input.budget;
        project.description = Some(format!("{} ({notify})", args.input.description.unwrap_or_default()));
        project.due_on = args.input.due_on;
        project.tags = args.input.tags;
        project.milestones = args.input.milestones.map(|milestones| {
            milestones.into_iter().map(|milestone| types::Milestone { title: milestone.title, due_on: milestone.due_on }).collect()
        });
        Ok(project.clone())
    }
    async fn delete_project(&self, _ctx: RequestContext, args: ProjectDeleteProjectArgs) -> Result<types::Project, ApiError> {
        let project = self.0.project(&args.id)?;
        self.0.projects.lock().unwrap().retain(|project| project.id != args.id);
        Ok(project)
    }
    async fn list_new_projects(&self, _ctx: RequestContext) -> Result<Vec<types::Project>, ApiError> {
        Err(ApiError::not_implemented("list_new_projects"))
    }
    async fn list_archived_projects(&self, _ctx: RequestContext) -> Result<Vec<types::Project>, ApiError> {
        let projects = self.0.projects.lock().unwrap();
        Ok(projects.iter().filter(|project| project.archived).cloned().collect())
    }
    async fn list_project_edits(&self, _ctx: RequestContext, _args: ProjectListProjectEditsArgs) -> Result<Vec<types::Comment>, ApiError> {
        Err(ApiError::not_implemented("list_project_edits"))
    }
    async fn list_project_deletions(
        &self,
        _ctx: RequestContext,
        _args: ProjectListProjectDeletionsArgs,
    ) -> Result<Vec<types::Comment>, ApiError> {
        Err(ApiError::not_implemented("list_project_deletions"))
    }
    async fn archive_project(&self, _ctx: RequestContext, args: ProjectArchiveProjectArgs) -> Result<types::Project, ApiError> {
        let mut projects = self.0.projects.lock().unwrap();
        let project = projects.iter_mut().find(|project| project.id == args.id).ok_or_else(|| ApiError::not_found("No such project"))?;
        if project.archived {
            return Err(ApiError::conflict("The project is archived already"));
        }
        project.archived = true;
        project.description = Some(args.reason);
        Ok(project.clone())
    }
}

#[async_trait]
impl TaskImplementation for Shop {
    async fn list_tasks(&self, _ctx: RequestContext, args: TaskListTasksArgs) -> Result<Vec<types::Task>, ApiError> {
        self.0.project(&args.project_id)?;
        let tasks = self.0.tasks.lock().unwrap();
        Ok(tasks
            .iter()
            .filter(|(project, task)| *project == args.project_id && args.done.is_none_or(|done| task.done == done))
            .map(|(_, task)| task.clone())
            .collect())
    }
    async fn get_task(&self, _ctx: RequestContext, args: TaskGetTaskArgs) -> Result<types::Task, ApiError> {
        let tasks = self.0.tasks.lock().unwrap();
        tasks
            .iter()
            .find(|(project, task)| *project == args.project_id && task.id == args.id)
            .map(|(_, task)| task.clone())
            .ok_or_else(|| ApiError::not_found("No such task"))
    }
    async fn list_labelled_tasks(&self, _ctx: RequestContext, args: TaskListLabelledTasksArgs) -> Result<Vec<types::Task>, ApiError> {
        let tasks = self.0.tasks.lock().unwrap();
        Ok(tasks.iter().filter(|(_, task)| task.title.to_lowercase().contains(&args.label)).map(|(_, task)| task.clone()).collect())
    }
    async fn add_task(&self, _ctx: RequestContext, args: TaskAddTaskArgs) -> Result<types::Task, ApiError> {
        self.0.project(&args.project_id)?;
        let task = types::Task { id: self.0.new_id(), title: args.input.title, done: false };
        self.0.tasks.lock().unwrap().push((args.project_id, task.clone()));
        Ok(task)
    }
}

#[async_trait]
impl DocumentImplementation for Shop {
    async fn list_documents(&self, _ctx: RequestContext) -> Result<Vec<types::Document>, ApiError> {
        Ok(self.0.documents.lock().unwrap().clone())
    }
    async fn list_drafts(&self, _ctx: RequestContext) -> Result<Vec<types::Document>, ApiError> {
        Ok(vec![document("plan", Some("The plan"))])
    }
    async fn get_document(&self, _ctx: RequestContext, args: DocumentGetDocumentArgs) -> Result<types::Document, ApiError> {
        self.0.read.lock().unwrap().push(args.id.clone());
        let documents = self.0.documents.lock().unwrap();
        documents.iter().find(|document| document.id == args.id).cloned().ok_or_else(|| ApiError::not_found("No such document"))
    }
    async fn list_comments(&self, _ctx: RequestContext, args: DocumentListCommentsArgs) -> Result<Vec<types::Comment>, ApiError> {
        Ok(vec![types::Comment { id: "first".to_owned(), text: format!("On {}", args.document_id) }])
    }
    async fn create_document(&self, _ctx: RequestContext, args: DocumentCreateDocumentArgs) -> Result<types::Document, ApiError> {
        let (title, body) = args.input.map_or((None, None), |input| (input.title, input.body));
        let mut next = self.0.next.lock().unwrap();
        *next += 1;
        let created = types::Document { id: format!("doc-{next}"), title, body };
        self.0.documents.lock().unwrap().push(created.clone());
        Ok(created)
    }
    async fn replace_document(&self, _ctx: RequestContext, args: DocumentReplaceDocumentArgs) -> Result<types::Document, ApiError> {
        let mut documents = self.0.documents.lock().unwrap();
        let document = documents.iter_mut().find(|document| document.id == args.id).ok_or_else(|| ApiError::not_found("No such document"))?;
        document.title = args.input.title;
        document.body = args.input.body;
        Ok(document.clone())
    }
    async fn replace_comments(&self, _ctx: RequestContext, _args: DocumentReplaceCommentsArgs) -> Result<Vec<types::Comment>, ApiError> {
        Err(ApiError::not_implemented("replace_comments"))
    }
    async fn clear_comments(&self, _ctx: RequestContext, _args: DocumentClearCommentsArgs) -> Result<Vec<types::Comment>, ApiError> {
        Err(ApiError::not_implemented("clear_comments"))
    }
    async fn remove_cover(&self, _ctx: RequestContext, _args: DocumentRemoveCoverArgs) -> Result<types::Document, ApiError> {
        Err(ApiError::not_implemented("remove_cover"))
    }
}

#[async_trait]
impl ShelfImplementation for Shop {
    async fn list_shelves(&self, _ctx: RequestContext) -> Result<Vec<types::Shelf>, ApiError> {
        Ok(vec![types::Shelf { id: "top".to_owned(), label: "Top shelf".to_owned() }])
    }
    async fn get_shelf(&self, _ctx: RequestContext, args: ShelfGetShelfArgs) -> Result<types::Shelf, ApiError> {
        Ok(types::Shelf { id: args.id, label: "Top shelf".to_owned() })
    }
}

#[async_trait]
impl MemberImplementation for Shop {
    async fn list_members(&self, _ctx: RequestContext) -> Result<Vec<types::Member>, ApiError> {
        Ok(vec![types::Member { id: uuid(NOWHERE), email: "ada@example.com".to_owned() }])
    }
}

#[async_trait]
impl ViewerImplementation for Shop {
    async fn me(&self, ctx: RequestContext) -> Result<types::Viewer, ApiError> {
        Ok(types::Viewer { subject: ctx.principal.map(|principal| principal.subject).unwrap_or_default() })
    }
}

// The JSON API's callers: none, since these tests drive the pages.
struct NoRequests;

#[async_trait]
impl Authenticator for NoRequests {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(None)
    }
}

// A page's caller, by the x-caller header: a reader of projects, a writer,
// an auditor, a reader of members, a reader of shelves, an editor of
// documents, one with no permission, or none. It counts the times it is
// asked.
#[derive(Default)]
struct Callers {
    asked: Arc<AtomicUsize>,
}

#[async_trait]
impl PageAuthenticator for Callers {
    async fn principal(&self, cx: &Cx) -> Result<Option<Principal>, ApiError> {
        self.asked.fetch_add(1, Ordering::SeqCst);
        let caller = parts(cx).headers.get("x-caller").and_then(|value| value.to_str().ok()).unwrap_or_default();
        let permissions: &[&str] = match caller {
            "reader" => &["projects.read"],
            "writer" => &["projects"],
            "auditor" => &["projects.read", "projects.audit"],
            "members" => &["members.read"],
            "shelver" => &["shelves.read"],
            "editor" => &["documents"],
            "nobody" => &[],
            _ => return Ok(None),
        };
        Ok(Some(Principal::new(caller, permissions.iter().copied())))
    }
}

fn implementations(store: &Arc<Store>) -> Implementations {
    Implementations {
        document: Arc::new(Shop(Arc::clone(store))),
        member: Arc::new(Shop(Arc::clone(store))),
        project: Arc::new(Shop(Arc::clone(store))),
        shelf: Arc::new(Shop(Arc::clone(store))),
        task: Arc::new(Shop(Arc::clone(store))),
        viewer: Arc::new(Shop(Arc::clone(store))),
        authenticator: Arc::new(NoRequests),
    }
}

// The app: the crate's pages by discovery, under the app's settings.
fn app(store: &Arc<Store>) -> Router {
    Router::builder()
        .discover()
        .app_context(Settings::new().title("Projects console").sign_in("/sign-in").choices(owners()))
        .fixture_pages_api(implementations(store), Callers::default())
        .build()
}

// The members who may own a project, as the app names them.
fn owners() -> Choices {
    Choices::new().with("ownerId", [("ada", "Ada Lovelace"), ("grace", "Grace Hopper")])
}

// The same app without settings: a refusal has no sign-in link.
fn bare_app(store: &Arc<Store>) -> Router {
    Router::builder().discover().fixture_pages_api(implementations(store), Callers::default()).build()
}

struct Answer {
    status: StatusCode,
    html: String,
    location: Option<String>,
}

async fn send(router: &Router, method: &str, uri: &str, caller: &str, body: &str) -> Answer {
    let mut request = http::Request::builder().method(method).uri(uri);
    if !caller.is_empty() {
        request = request.header("x-caller", caller);
    }
    if method == "POST" {
        request = request.header(header::CONTENT_TYPE, "application/x-www-form-urlencoded");
    }
    let response = router.handle(request.body(Body::from(body.to_owned())).unwrap()).await;
    let (status, headers) = (response.status(), response.headers().clone());
    let location = headers.get(header::LOCATION).map(|value| value.to_str().unwrap().to_owned());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let html = String::from_utf8(bytes.to_vec()).unwrap();
    enter::check_page(uri, &headers, &html);
    Answer { status, html, location }
}

async fn get(router: &Router, uri: &str, caller: &str) -> Answer {
    send(router, "GET", uri, caller, "").await
}

async fn post(router: &Router, uri: &str, caller: &str, body: &str) -> Answer {
    send(router, "POST", uri, caller, body).await
}

#[tokio::test]
async fn the_index_and_the_nav_show_what_the_caller_may_list() {
    let app = app(&Store::new());
    let index = get(&app, "/console", "reader").await;
    assert_eq!(index.status, StatusCode::OK, "{}", index.html);
    assert!(index.html.contains(r#"<header class="ss-header"><a href="/console">Projects console</a></header>"#), "{}", index.html);
    // The projects' link starts with their type's icon, which the app's
    // stylesheet draws.
    assert!(index.html.contains(r#"<ul class="ss-index"><li><a href="/console/projects"><span class="ss-icon" data-icon="folder" aria-hidden="true"></span>Projects</a></li></ul>"#), "{}", index.html);
    assert!(!index.html.contains("/console/members"), "a reader of projects sees members: {}", index.html);
    assert!(index.html.contains(r#"<li><span aria-current="page">Projects console</span></li>"#), "{}", index.html);

    let index = get(&app, "/console", "members").await;
    // A link whose type declares no icon has no span.
    assert!(index.html.contains(r#"<li><a href="/console/members">Members</a></li>"#), "{}", index.html);
    assert!(!index.html.contains("ss-icon"), "{}", index.html);
    assert!(!index.html.contains("/console/projects"), "a reader of members sees projects: {}", index.html);

    // Signed out, the index lists nothing and offers to sign in.
    let index = get(&app, "/console", "").await;
    assert_eq!(index.status, StatusCode::OK);
    assert!(index.html.contains("There is nothing here you may list."), "{}", index.html);
    assert!(index.html.contains(r#"<a class="ss-sign-in" href="/sign-in?next=%2Fconsole">Sign in</a>"#), "{}", index.html);

    // The nav marks the current resource: its list as the page, an item's
    // page as under it.
    let list = get(&app, "/console/projects", "reader").await;
    assert!(list.html.contains(r#"<nav class="ss-nav" aria-label="Resources"><ul><li><a href="/console/projects" aria-current="page"><span class="ss-icon" data-icon="folder" aria-hidden="true"></span>Projects</a></li></ul></nav>"#), "{}", list.html);
    let item = get(&app, &format!("/console/projects/{APOLLO}"), "reader").await;
    assert!(item.html.contains(r#"<li><a href="/console/projects" aria-current="true"><span class="ss-icon" data-icon="folder" aria-hidden="true"></span>Projects</a></li>"#), "{}", item.html);
    // The trail shows no icon.
    assert!(item.html.contains(r#"<li><a href="/console/projects">Projects</a></li>"#), "{}", item.html);
}

#[tokio::test]
async fn a_list_filters_links_its_rows_and_offers_a_new_page_to_a_writer() {
    let app = app(&Store::new());
    let list = get(&app, "/console/projects", "reader").await;
    assert_eq!(list.status, StatusCode::OK, "{}", list.html);
    assert!(list.html.contains(r#"<h1>Projects</h1>"#), "{}", list.html);
    let (apollo, gemini) = (wire(APOLLO), wire(GEMINI));
    assert!(list.html.contains(&format!(r#"<th scope="row" data-field="name"><a href="/console/projects/{apollo}">Apollo</a></th>"#)), "{}", list.html);
    assert!(list.html.contains(&format!(r#"<a href="/console/projects/{gemini}">Gemini</a>"#)), "{}", list.html);
    assert!(!list.html.contains("Start a project"), "a reader may not create: {}", list.html);
    assert!(list.html.contains(r#"<td data-field="ownerId">Ada Lovelace</td>"#), "{}", list.html);
    assert!(list.html.contains(r#"<form method="get" class="ss-filter" data-ss-live="projects""#), "{}", list.html);
    assert!(list.html.contains(r#"<li><a href="/console">Projects console</a></li><li><span aria-current="page">Projects</span></li>"#), "{}", list.html);

    let list = get(&app, "/console/projects", "writer").await;
    assert!(list.html.contains(r#"<a class="ss-new" href="/console/projects/new">Start a project</a>"#), "{}", list.html);

    let filtered = get(&app, "/console/projects?statuses=planned", "reader").await;
    assert_eq!(filtered.status, StatusCode::OK, "{}", filtered.html);
    assert!(filtered.html.contains("Gemini") && !filtered.html.contains("Apollo"), "{}", filtered.html);
    assert!(filtered.html.contains(r#"value="planned" checked="""#), "{}", filtered.html);

    let refused = get(&app, "/console/projects?statuses=lost", "reader").await;
    assert_eq!(refused.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", refused.html);
    assert!(!refused.html.contains("<table"), "{}", refused.html);

    let empty = get(&app, "/console/projects?statuses=done", "reader").await;
    assert!(empty.html.contains(r#"<p class="ss-empty">There are no projects.</p>"#), "{}", empty.html);
}

#[tokio::test]
async fn an_item_page_shows_its_detail_its_links_its_actions_and_its_sections() {
    let app = app(&Store::new());
    let item = get(&app, &format!("/console/projects/{APOLLO}"), "writer").await;
    assert_eq!(item.status, StatusCode::OK, "{}", item.html);
    for want in [
        "<h1>Apollo</h1>".to_owned(),
        r#"<div class="ss-detail" data-type="Project" role="group" aria-label="Apollo">"#.to_owned(),
        format!(r#"<a class="ss-edit" href="/console/projects/{APOLLO}/edit">Edit</a>"#),
        format!(r#"<a class="ss-delete" href="/console/projects/{APOLLO}/delete">Delete</a>"#),
        format!(r#"<form method="post" action="/console/projects/{APOLLO}/archive" class="ss-action" data-action="archive">"#),
        format!(r#"<input type="hidden" name="id" value="{APOLLO}">"#),
        r#"name="reason""#.to_owned(),
        r#"<button type="submit">Archive project</button>"#.to_owned(),
        format!(r#"<section class="ss-section" data-resource="tasks"><h2><a href="/console/projects/{APOLLO}/tasks">Tasks</a></h2>"#),
        format!(r#"<a class="ss-new" href="/console/projects/{APOLLO}/tasks/new">New task</a>"#),
        format!(r#"<a href="/console/projects/{APOLLO}/tasks/{}">Build the rocket</a>"#, wire(ROCKET)),
        // The trail names the item by its title.
        r#"<li><a href="/console/projects">Projects</a></li><li><span aria-current="page">Apollo</span></li>"#.to_owned(),
        // The app's choices name the owner.
        r#"<div data-field="ownerId"><dt>Owner</dt><dd>Ada Lovelace</dd></div>"#.to_owned(),
    ] {
        assert!(item.html.contains(&want), "the item's page lacks {want}: {}", item.html);
    }

    // A reader sees the item and its tasks, and nothing that changes them.
    let item = get(&app, &format!("/console/projects/{APOLLO}"), "reader").await;
    assert_eq!(item.status, StatusCode::OK, "{}", item.html);
    assert!(item.html.contains("Build the rocket"), "{}", item.html);
    for unwanted in ["ss-actions", "ss-edit", "ss-delete", "data-action", "ss-new"] {
        assert!(!item.html.contains(unwanted), "a reader's item page has {unwanted}: {}", item.html);
    }
}

#[tokio::test]
async fn a_new_page_posts_redirects_and_refuses_with_422() {
    let store = Store::new();
    let app = app(&store);
    let new = get(&app, "/console/projects/new", "writer").await;
    assert_eq!(new.status, StatusCode::OK, "{}", new.html);
    assert!(new.html.contains("<h1>Start a project</h1>"), "{}", new.html);
    assert!(new.html.contains(r#"name="name" type="text" required="" minlength="2""#), "{}", new.html);
    assert!(new.html.contains(r#"<select id="project-input-owner-id" name="ownerId" required=""><option value="" selected=""></option><option value="ada">Ada Lovelace</option><option value="grace">Grace Hopper</option></select>"#), "{}", new.html);
    assert!(new.html.contains(r#"<li><a href="/console/projects">Projects</a></li><li><span aria-current="page">Start a project</span></li>"#), "{}", new.html);

    let added = post(&app, "/console/projects/new", "writer", "name=Mercury&ownerId=ada&status=planned&budget=10&_action=add:tags").await;
    assert_eq!(added.status, StatusCode::OK, "{}", added.html);
    assert!(added.html.contains(r#"name="tags[0]""#), "{}", added.html);
    assert_eq!(store.projects.lock().unwrap().len(), 2, "a row button created a project");

    let short = post(&app, "/console/projects/new", "writer", "name=A&ownerId=ada&status=planned&budget=10").await;
    assert_eq!(short.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", short.html);
    assert!(short.html.contains(r#"value="A""#), "{}", short.html);
    assert!(short.html.contains(r#"<p class="field-error">"#), "{}", short.html);

    let created = post(&app, "/console/projects/new", "writer", "name=Mercury&ownerId=grace&status=planned&budget=10&description=First").await;
    assert_eq!(created.status, StatusCode::SEE_OTHER, "{}", created.html);
    let location = created.location.unwrap();
    let id = location.strip_prefix("/console/projects/").unwrap_or_default();
    assert!(!id.is_empty() && !id.contains('/'), "{location}");
    let shown = get(&app, &location, "writer").await;
    assert!(shown.html.contains("<h1>Mercury</h1>") && shown.html.contains("<dd>Grace Hopper</dd>"), "{}", shown.html);

    let refused = get(&app, "/console/projects/new", "reader").await;
    assert_eq!(refused.status, StatusCode::FORBIDDEN, "{}", refused.html);
    let refused = post(&app, "/console/projects/new", "reader", "name=Venus&ownerId=ada&status=planned&budget=10").await;
    assert_eq!(refused.status, StatusCode::FORBIDDEN, "{}", refused.html);
    assert_eq!(store.projects.lock().unwrap().len(), 3);
}

#[tokio::test]
async fn an_edit_page_is_filled_from_the_item_and_posts_redirects() {
    let store = Store::new();
    let app = app(&store);
    let edit = get(&app, &format!("/console/projects/{APOLLO}/edit"), "writer").await;
    assert_eq!(edit.status, StatusCode::OK, "{}", edit.html);
    for want in [
        "<h1>Edit the project</h1>",
        r#"value="Apollo""#,
        r#"<option value="active" selected="">Active</option>"#,
        r#"<option value="ada" selected="">Ada Lovelace</option>"#,
        r#"value="1200.5""#,
        // The description, up to 500 characters, is long text.
        "name=\"description\" rows=\"6\" maxlength=\"500\">\nTo the moon</textarea>",
        r#"name="dueOn" type="date" value="2026-12-01""#,
        // The rows the project holds, a list of values and a list of
        // objects of another type with the same fields.
        r#"name="tags[0]" type="text" required="" value="moon""#,
        r#"name="tags[1]" type="text" required="" value="rocket""#,
        r#"name="milestones[0].title" type="text" required="" minlength="1" value="Launch""#,
        r#"name="milestones[0].dueOn" type="date" value="2026-07-16""#,
        // notifyOwner, which a project does not hold, starts as a new
        // form starts it: its default, true.
        r#"name="notifyOwner" type="checkbox" checked="""#,
        "<span aria-current=\"page\">Edit</span>",
    ] {
        assert!(edit.html.contains(want), "the edit page lacks {want}: {}", edit.html);
    }

    // A row button renders the form again, a row more, and saves nothing.
    let added = post(
        &app,
        &format!("/console/projects/{APOLLO}/edit"),
        "writer",
        &format!("id={APOLLO}&name=Apollo+X&ownerId=ada&status=active&budget=1&tags[0]=moon&_action=add:tags"),
    )
    .await;
    assert_eq!(added.status, StatusCode::OK, "{}", added.html);
    assert!(added.html.contains(r#"name="tags[1]""#) && added.html.contains(r#"value="Apollo X""#), "{}", added.html);
    assert_eq!(store.project(&uuid(APOLLO)).unwrap().name, "Apollo");

    let refused = post(&app, &format!("/console/projects/{APOLLO}/edit"), "writer", &format!("id={APOLLO}&name=Apollo&ownerId=ada&status=active&budget=-1")).await;
    assert_eq!(refused.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", refused.html);
    assert!(refused.html.contains(r#"value="-1""#), "{}", refused.html);

    // The route's id is the project changed, whatever the hidden input says.
    let saved = post(
        &app,
        &format!("/console/projects/{APOLLO}/edit"),
        "writer",
        &format!("id={GEMINI}&name=Apollo+II&ownerId=grace&status=done&budget=1300&description=Again&notifyOwner=on&tags[0]=sun&milestones[0].title=Return"),
    )
    .await;
    assert_eq!(saved.status, StatusCode::SEE_OTHER, "{}", saved.html);
    assert_eq!(saved.location.as_deref(), Some(format!("/console/projects/{APOLLO}").as_str()));
    let project = store.project(&uuid(APOLLO)).unwrap();
    assert_eq!(project.name, "Apollo II");
    assert_eq!(project.owner_id, "grace");
    assert_eq!(project.description.as_deref(), Some("Again (notified)"));
    assert_eq!(project.tags, Some(vec!["sun".to_owned()]));
    assert_eq!(project.milestones, Some(vec![types::Milestone { title: "Return".to_owned(), due_on: None }]));
    assert_eq!(store.project(&uuid(GEMINI)).unwrap().name, "Gemini");

    let refused = get(&app, &format!("/console/projects/{APOLLO}/edit"), "reader").await;
    assert_eq!(refused.status, StatusCode::FORBIDDEN, "{}", refused.html);
}

#[tokio::test]
async fn a_delete_asks_first_then_goes_to_the_list() {
    let store = Store::new();
    let app = app(&store);
    let confirm = get(&app, &format!("/console/projects/{GEMINI}/delete"), "writer").await;
    assert_eq!(confirm.status, StatusCode::OK, "{}", confirm.html);
    for want in [
        "<h1>Delete the project</h1>".to_owned(),
        r#"<p class="ss-confirm">Delete this project? It cannot be undone.</p>"#.to_owned(),
        r#"data-type="Project""#.to_owned(),
        r#"<form method="post" class="ss-form" data-action="delete">"#.to_owned(),
        r#"<button type="submit">Delete the project</button>"#.to_owned(),
        format!(r#"<a class="ss-back" href="/console/projects/{GEMINI}">Keep the project</a>"#),
    ] {
        assert!(confirm.html.contains(&want), "the confirmation lacks {want}: {}", confirm.html);
    }
    assert_eq!(store.projects.lock().unwrap().len(), 2, "a GET deleted the project");

    let deleted = post(&app, &format!("/console/projects/{GEMINI}/delete"), "writer", &format!("id={GEMINI}")).await;
    assert_eq!(deleted.status, StatusCode::SEE_OTHER, "{}", deleted.html);
    assert_eq!(deleted.location.as_deref(), Some("/console/projects"));
    let list = get(&app, "/console/projects", "writer").await;
    assert!(!list.html.contains("Gemini"), "{}", list.html);
    let gone = get(&app, &format!("/console/projects/{GEMINI}"), "writer").await;
    assert_eq!(gone.status, StatusCode::NOT_FOUND, "{}", gone.html);
    assert!(gone.html.contains("No such project"), "{}", gone.html);

    let again = post(&app, &format!("/console/projects/{GEMINI}/delete"), "writer", "").await;
    assert_eq!(again.status, StatusCode::NOT_FOUND, "{}", again.html);
}

#[tokio::test]
async fn an_action_posts_and_comes_back_and_a_conflict_is_the_forms_message() {
    let store = Store::new();
    let app = app(&store);
    let uri = format!("/console/projects/{APOLLO}/archive");
    let short = post(&app, &uri, "writer", &format!("id={APOLLO}&reason=no")).await;
    assert_eq!(short.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", short.html);
    assert!(short.html.contains("<h1>Apollo</h1>"), "a refused action renders the item's page again: {}", short.html);
    assert!(short.html.contains(r#"value="no""#), "{}", short.html);

    let archived = post(&app, &uri, "writer", &format!("id={APOLLO}&reason=Landed")).await;
    assert_eq!(archived.status, StatusCode::SEE_OTHER, "{}", archived.html);
    assert_eq!(archived.location.as_deref(), Some(format!("/console/projects/{APOLLO}").as_str()));
    assert!(store.project(&uuid(APOLLO)).unwrap().archived);
    let item = get(&app, &format!("/console/projects/{APOLLO}"), "writer").await;
    assert!(item.html.contains(r#"<div data-field="archived"><dt>Archived</dt><dd>Yes</dd></div>"#), "{}", item.html);

    let again = post(&app, &uri, "writer", &format!("id={APOLLO}&reason=Landed")).await;
    assert_eq!(again.status, StatusCode::CONFLICT, "{}", again.html);
    assert!(again.html.contains(r#"<p class="form-error">The project is archived already</p>"#), "{}", again.html);

    let refused = post(&app, &uri, "reader", &format!("id={APOLLO}&reason=Landed")).await;
    assert_eq!(refused.status, StatusCode::FORBIDDEN, "{}", refused.html);
}

#[tokio::test]
async fn a_nested_resource_has_pages_under_its_parents_item() {
    let store = Store::new();
    let app = app(&store);
    let tasks = get(&app, &format!("/console/projects/{APOLLO}/tasks"), "writer").await;
    assert_eq!(tasks.status, StatusCode::OK, "{}", tasks.html);
    assert!(tasks.html.contains("<h1>Tasks</h1>"), "{}", tasks.html);
    assert!(tasks.html.contains(&format!(r#"<a href="/console/projects/{APOLLO}/tasks/{}">Build the rocket</a>"#, wire(ROCKET))), "{}", tasks.html);
    assert!(tasks.html.contains(&format!(
        r#"<li><a href="/console/projects">Projects</a></li><li><a href="/console/projects/{APOLLO}">Apollo</a></li><li><span aria-current="page">Tasks</span></li>"#
    )), "{}", tasks.html);

    let new = get(&app, &format!("/console/projects/{APOLLO}/tasks/new"), "writer").await;
    assert_eq!(new.status, StatusCode::OK, "{}", new.html);
    assert!(new.html.contains("<h1>New task</h1>") && new.html.contains(r#"<button type="submit">Create</button>"#), "{}", new.html);

    let blank = post(&app, &format!("/console/projects/{APOLLO}/tasks/new"), "writer", &format!("projectId={APOLLO}&title=")).await;
    assert_eq!(blank.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", blank.html);

    let added = post(&app, &format!("/console/projects/{APOLLO}/tasks/new"), "writer", &format!("projectId={GEMINI}&title=Fuel")).await;
    assert_eq!(added.status, StatusCode::SEE_OTHER, "{}", added.html);
    let location = added.location.unwrap();
    assert!(location.starts_with(&format!("/console/projects/{APOLLO}/tasks/")), "{location}");
    let task = get(&app, &location, "writer").await;
    assert_eq!(task.status, StatusCode::OK, "{}", task.html);
    assert!(task.html.contains("<h1>Fuel</h1>"), "{}", task.html);
    // The route's project, not the hidden input's, holds the task.
    let gemini = get(&app, &format!("/console/projects/{GEMINI}/tasks"), "writer").await;
    assert!(!gemini.html.contains("Fuel"), "{}", gemini.html);

    let missing = get(&app, &format!("/console/projects/{NOWHERE}/tasks"), "writer").await;
    assert_eq!(missing.status, StatusCode::NOT_FOUND, "{}", missing.html);

    // A list under a label, which nothing lists or reads: its page lives
    // under the label's path, its rows unlinked, as no detail reads them,
    // and neither the nav nor the index links it, since it needs a label.
    let labelled = get(&app, "/console/labels/rocket/tasks", "reader").await;
    assert_eq!(labelled.status, StatusCode::OK, "{}", labelled.html);
    assert!(labelled.html.contains(r#"<th scope="row" data-field="title">Build the rocket</th>"#), "{}", labelled.html);
    assert!(labelled.html.contains(r#"<li><span>Labels</span></li><li><span>rocket</span></li><li><span aria-current="page">Tasks</span></li>"#), "{}", labelled.html);
    let index = get(&app, "/console", "reader").await;
    assert!(!index.html.contains("/console/labels"), "{}", index.html);
}

#[tokio::test]
async fn documents_link_an_untitled_row_and_read_the_most_specific_path() {
    let store = Store::new();
    let app = app(&store);
    // A document without a title links its row by its noun and id.
    let list = get(&app, "/console/documents", "editor").await;
    assert_eq!(list.status, StatusCode::OK, "{}", list.html);
    assert!(list.html.contains(r#"<th scope="row" data-field="title"><a href="/console/documents/intro">Document intro</a></th>"#), "{}", list.html);
    assert!(list.html.contains(r#"<th scope="row" data-field="title"><a href="/console/documents/guide">Guide</a></th>"#), "{}", list.html);
    assert!(list.html.contains(r#"<li><a href="/console/documents" aria-current="page">Documents</a></li><li><a href="/console/documents/drafts">Drafts</a></li>"#), "{}", list.html);

    // documents/drafts is the drafts' list, which the nav and the trail
    // mark, not a document named drafts.
    let drafts = get(&app, "/console/documents/drafts", "editor").await;
    assert_eq!(drafts.status, StatusCode::OK, "{}", drafts.html);
    assert!(drafts.html.contains("<h1>Drafts</h1>") && drafts.html.contains("The plan"), "{}", drafts.html);
    assert!(drafts.html.contains(r#"<li><a href="/console">Projects console</a></li><li><span aria-current="page">Drafts</span></li>"#), "{}", drafts.html);
    assert!(drafts.html.contains(r#"<li><a href="/console/documents">Documents</a></li><li><a href="/console/documents/drafts" aria-current="page">Drafts</a></li>"#), "{}", drafts.html);
    assert!(!store.read.lock().unwrap().iter().any(|id| id == "drafts"), "a document named drafts was read");

    // A document, read with its query argument left out: its edit link and
    // its comments, and no form for the PUT or the DELETE of its comments,
    // nor for the DELETE of its cover.
    let item = get(&app, "/console/documents/intro", "editor").await;
    assert_eq!(item.status, StatusCode::OK, "{}", item.html);
    assert!(item.html.contains("<h1>Document</h1>"), "{}", item.html);
    assert!(item.html.contains(r#"<a class="ss-edit" href="/console/documents/intro/edit">Edit</a>"#), "{}", item.html);
    assert!(item.html.contains(r#"<section class="ss-section" data-resource="comments"><h2><a href="/console/documents/intro/comments">Comments</a></h2>"#), "{}", item.html);
    assert!(!item.html.contains("data-action") && !item.html.contains("ss-delete"), "{}", item.html);
    assert!(item.html.contains(r#"<li><a href="/console/documents">Documents</a></li><li><span aria-current="page">Document intro</span></li>"#), "{}", item.html);

    // The comments are a collection under the document, never its action.
    let comments = get(&app, "/console/documents/intro/comments", "editor").await;
    assert_eq!(comments.status, StatusCode::OK, "{}", comments.html);
    assert!(comments.html.contains("On intro"), "{}", comments.html);
    assert!(comments.html.contains(r#"<li><a href="/console/documents/intro">Document intro</a></li><li><span aria-current="page">Comments</span></li>"#), "{}", comments.html);
    for uri in ["/console/documents/intro/comments", "/console/documents/intro/cover"] {
        let posted = post(&app, uri, "editor", "documentId=intro").await;
        assert!(posted.status.is_client_error(), "POST {uri}: {} {}", posted.status, posted.html);
    }
}

#[tokio::test]
async fn a_document_starts_from_an_optional_input_and_is_replaced_through_a_required_one() {
    let store = Store::new();
    let app = app(&store);
    let new = get(&app, "/console/documents/new", "editor").await;
    assert_eq!(new.status, StatusCode::OK, "{}", new.html);
    assert!(new.html.contains(r#"name="title""#) && new.html.contains(r#"name="body""#), "{}", new.html);
    let created = post(&app, "/console/documents/new", "editor", "title=&body=").await;
    assert_eq!(created.status, StatusCode::SEE_OTHER, "{}", created.html);
    let location = created.location.unwrap();
    let id = location.strip_prefix("/console/documents/").unwrap_or_default().to_owned();
    assert!(id.starts_with("doc-"), "{location}");
    let list = get(&app, "/console/documents", "editor").await;
    assert!(list.html.contains(&format!(r#"<a href="{location}">Document {id}</a>"#)), "{}", list.html);

    let edit = get(&app, "/console/documents/guide/edit", "editor").await;
    assert_eq!(edit.status, StatusCode::OK, "{}", edit.html);
    assert!(edit.html.contains(r#"value="Guide""#), "{}", edit.html);
    let saved = post(&app, "/console/documents/guide/edit", "editor", "id=guide&title=Handbook&body=All+of+it").await;
    assert_eq!(saved.status, StatusCode::SEE_OTHER, "{}", saved.html);
    assert_eq!(saved.location.as_deref(), Some("/console/documents/guide"));
    let shown = get(&app, "/console/documents/guide", "editor").await;
    assert!(shown.html.contains("<h1>Handbook</h1>"), "{}", shown.html);
}

#[tokio::test]
async fn a_refused_caller_keeps_the_routes_status() {
    let store = Store::new();
    let app = app(&store);
    let signed_out = get(&app, "/console/projects?statuses=planned", "").await;
    assert_eq!(signed_out.status, StatusCode::UNAUTHORIZED, "{}", signed_out.html);
    assert!(signed_out.html.contains("<h1>Sign in to see this page</h1>"), "{}", signed_out.html);
    assert!(signed_out.html.contains(r#"<a class="ss-sign-in" href="/sign-in?next=%2Fconsole%2Fprojects%3Fstatuses%3Dplanned">Sign in</a>"#), "{}", signed_out.html);

    let item = get(&app, &format!("/console/projects/{APOLLO}"), "").await;
    assert_eq!(item.status, StatusCode::UNAUTHORIZED, "{}", item.html);
    // A caller who may not read the item sees it by its noun and id.
    assert!(item.html.contains(&format!(r#"<span aria-current="page">Project {APOLLO}</span>"#)), "{}", item.html);

    // A refused post signs in to come back to its page: the new page, or
    // the item's for an action, which has none of its own.
    let created = post(&app, "/console/projects/new", "", "name=Venus&ownerId=ada&status=planned&budget=10").await;
    assert_eq!(created.status, StatusCode::UNAUTHORIZED, "{}", created.html);
    assert!(created.html.contains(r#"href="/sign-in?next=%2Fconsole%2Fprojects%2Fnew""#), "{}", created.html);
    let archived = post(&app, &format!("/console/projects/{APOLLO}/archive"), "", "reason=Landed").await;
    assert_eq!(archived.status, StatusCode::UNAUTHORIZED, "{}", archived.html);
    assert!(archived.html.contains(&format!(r#"href="/sign-in?next=%2Fconsole%2Fprojects%2F{APOLLO}""#)), "{}", archived.html);
    // Nothing to list, no nav.
    assert!(!archived.html.contains("ss-nav"), "{}", archived.html);
    assert!(!store.project(&uuid(APOLLO)).unwrap().archived);

    let bare = get(&bare_app(&store), "/console/projects", "").await;
    assert_eq!(bare.status, StatusCode::UNAUTHORIZED, "{}", bare.html);
    assert!(!bare.html.contains("ss-sign-in"), "{}", bare.html);
    assert!(bare.html.contains(r#"<header class="ss-header"><a href="/console">Fixture pages api</a></header>"#), "{}", bare.html);

    let forbidden = get(&app, "/console/projects", "nobody").await;
    assert_eq!(forbidden.status, StatusCode::FORBIDDEN, "{}", forbidden.html);
    assert!(forbidden.html.contains("<h1>Not allowed</h1>"), "{}", forbidden.html);
    let forbidden = get(&app, "/console/members", "reader").await;
    assert_eq!(forbidden.status, StatusCode::FORBIDDEN, "{}", forbidden.html);

    let missing = get(&app, &format!("/console/projects/{NOWHERE}"), "reader").await;
    assert_eq!(missing.status, StatusCode::NOT_FOUND, "{}", missing.html);
    assert!(missing.html.contains("<h1>Not found</h1>"), "{}", missing.html);

    // A route's value its argument does not decode is no page.
    let unparsed = get(&app, "/console/projects/not-a-uuid", "reader").await;
    assert_eq!(unparsed.status, StatusCode::NOT_FOUND, "{}", unparsed.html);
    assert!(unparsed.html.contains("There is no such page"), "{}", unparsed.html);
    let unparsed = post(&app, "/console/projects/not-a-uuid/archive", "writer", "reason=Landed").await;
    assert_eq!(unparsed.status, StatusCode::NOT_FOUND, "{}", unparsed.html);

    let members = get(&app, "/console/members", "members").await;
    assert_eq!(members.status, StatusCode::OK, "{}", members.html);
    assert!(members.html.contains("ada@example.com"), "{}", members.html);
}

// A shard's request, as Topcoat's runtime sends one: its arguments and the
// values of the signals its content holds, in JSON, under the shard's
// identity when the page gave one.
async fn shard(router: &Router, uri: &str, caller: &str, body: serde_json::Value, identity: Option<&str>) -> Answer {
    let mut request = http::Request::builder().method("POST").uri(uri).header(header::CONTENT_TYPE, "application/json");
    if !caller.is_empty() {
        request = request.header("x-caller", caller);
    }
    if let Some(identity) = identity {
        request = request.header("x-topcoat-identity", identity);
    }
    let response = router.handle(request.body(Body::from(body.to_string())).unwrap()).await;
    let (status, headers) = (response.status(), response.headers().clone());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let html = String::from_utf8(bytes.to_vec()).unwrap();
    enter::check_page(uri, &headers, &html);
    Answer { status, html, location: None }
}

// The text of html between the first start and the end after it.
fn between<'a>(html: &'a str, start: &str, end: &str) -> &'a str {
    let rest = &html[html.find(start).unwrap_or_else(|| panic!("no {start} in {html}")) + start.len()..];
    &rest[..rest.find(end).unwrap()]
}

#[tokio::test]
async fn a_live_list_renders_its_filter_through_a_shard() {
    let app = app(&Store::new());
    // Without Topcoat's runtime the page works as before: its filter is a
    // GET form and its results follow it, between the shard's markers.
    let list = get(&app, "/console/projects?statuses=planned", "reader").await;
    assert_eq!(list.status, StatusCode::OK, "{}", list.html);
    for want in [
        r#"<!--::topcoat::shard::start("/console/_live/projects", "#,
        r#"<form method="get" class="ss-filter" data-ss-live="projects" data-topcoat-on:submit=""#,
        r#"<input type="hidden" data-ss-query="" data-topcoat-on:input=""#,
        r#"value="planned" checked="""#,
    ] {
        assert!(list.html.contains(want), "the list lacks {want}: {}", list.html);
    }
    assert!(list.html.contains("Gemini") && !list.html.contains("Apollo"), "{}", list.html);

    // The shard's request renders the list for the query it sends, without
    // the layout around it.
    let live = shard(&app, "/console/_live/projects", "reader", serde_json::json!({"args": ["statuses=active", "/console/projects"]}), None).await;
    assert_eq!(live.status, StatusCode::OK, "{}", live.html);
    assert!(live.html.contains("Apollo") && !live.html.contains("Gemini"), "{}", live.html);
    assert!(live.html.contains(r#"value="active" checked="""#), "{}", live.html);
    assert!(!live.html.contains("ss-nav"), "{}", live.html);

    // As the runtime renders it again, under the shard's identity, with the
    // query the filter's submit set in its signal, which wins over the
    // query the page first gave it.
    let identity = between(&list.html, r#"::topcoat::shard::start("/console/_live/projects", ""#, "\"");
    let signal = between(&list.html, "::topcoat::signal({&quot;t&quot;:&quot;signal&quot;,&quot;id&quot;:&quot;", "&quot;");
    let body = serde_json::json!({"args": ["statuses=planned", "/console/projects"], "signals": {signal: "statuses=active"}});
    let rerun = shard(&app, "/console/_live/projects", "reader", body, Some(identity)).await;
    assert_eq!(rerun.status, StatusCode::OK, "{}", rerun.html);
    assert!(rerun.html.contains("Apollo") && !rerun.html.contains("Gemini"), "{}", rerun.html);

    // A filter the rules refuse answers 422, its message at its control,
    // as the page does; a name no control has, or an id, is ignored.
    let refused = shard(&app, "/console/_live/projects", "reader", serde_json::json!({"args": ["statuses=lost", "/console/projects"]}), None).await;
    assert_eq!(refused.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", refused.html);
    assert!(!refused.html.contains("<table"), "{}", refused.html);
    let forged = format!("statuses=planned&id={APOLLO}&owner=ada");
    let ignored = shard(&app, "/console/_live/projects", "reader", serde_json::json!({"args": [forged, "/console/projects"]}), None).await;
    assert_eq!(ignored.status, StatusCode::OK, "{}", ignored.html);
    assert!(ignored.html.contains("Gemini") && !ignored.html.contains("Apollo"), "{}", ignored.html);

    // A shard's request meets no page's guard: the list's call admits its
    // caller, and refuses one the list refuses with the route's status and
    // none of the projects.
    for (caller, status) in [("", StatusCode::UNAUTHORIZED), ("nobody", StatusCode::FORBIDDEN), ("members", StatusCode::FORBIDDEN)] {
        let refused = shard(&app, "/console/_live/projects", caller, serde_json::json!({"args": ["", "/console/projects"]}), None).await;
        assert_eq!(refused.status, status, "{caller}: {}", refused.html);
        assert!(!refused.html.contains("Apollo") && !refused.html.contains("<table"), "{caller}: {}", refused.html);
    }
    let signed_out = shard(&app, "/console/_live/projects", "", serde_json::json!({"args": ["", "/console/projects"]}), None).await;
    assert!(signed_out.html.contains(r#"<a class="ss-sign-in" href="/sign-in?next=%2Fconsole%2Fprojects">Sign in</a>"#), "{}", signed_out.html);

    // A nested list's shard takes its project from its arguments, as its
    // page does from its route: the query's projectId is ignored, and one
    // that is no project's id is no page.
    let open = format!("done=false&projectId={GEMINI}");
    let tasks = shard(&app, "/console/_live/projects_tasks", "reader", serde_json::json!({"args": [APOLLO, open, "/x"]}), None).await;
    assert_eq!(tasks.status, StatusCode::OK, "{}", tasks.html);
    assert!(tasks.html.contains("Build the rocket"), "{}", tasks.html);
    let done = shard(&app, "/console/_live/projects_tasks", "reader", serde_json::json!({"args": [APOLLO, "done=true", "/x"]}), None).await;
    assert!(done.html.contains(r#"<p class="ss-empty">There are no tasks.</p>"#), "{}", done.html);
    let unparsed = shard(&app, "/console/_live/projects_tasks", "reader", serde_json::json!({"args": ["not-a-uuid", "", "/x"]}), None).await;
    assert_eq!(unparsed.status, StatusCode::NOT_FOUND, "{}", unparsed.html);
    let signed_out = shard(&app, "/console/_live/projects_tasks", "", serde_json::json!({"args": [APOLLO, "", "/x"]}), None).await;
    assert_eq!(signed_out.status, StatusCode::UNAUTHORIZED, "{}", signed_out.html);
    assert!(!signed_out.html.contains("Build the rocket"), "{}", signed_out.html);
}

#[tokio::test]
async fn a_render_authenticates_its_caller_once() {
    let store = Store::new();
    let callers = Callers::default();
    let asked = Arc::clone(&callers.asked);
    let app = Router::builder().discover().fixture_pages_api(implementations(&store), callers).build();
    // The index asks each list's guard for the nav and for itself, a list
    // its guard, its filter's call and the new page's guard, and a
    // project's page its detail, the guards of its links and its action
    // and its tasks' section: each authenticates its caller once.
    for (path, caller) in [
        "/console".to_owned(),
        "/console/projects".to_owned(),
        format!("/console/projects/{APOLLO}"),
        format!("/console/projects/{APOLLO}/edit"),
        format!("/console/projects/{APOLLO}/tasks"),
    ]
    .iter()
    .zip(["reader", "writer", "writer", "writer", "reader"])
    {
        asked.store(0, Ordering::SeqCst);
        let page = get(&app, path, caller).await;
        assert_eq!(page.status, StatusCode::OK, "{path}: {}", page.html);
        assert_eq!(asked.load(Ordering::SeqCst), 1, "{path} asked for its caller more than once");
    }
}

#[tokio::test]
async fn a_resources_own_choices_win_on_its_pages() {
    let store = Store::new();
    let settings = Settings::new()
        .choices(owners().with("title", [("Build the rocket", "The rocket")]))
        .choices_for("projects", Choices::new().with("ownerId", [("ada", "Countess Ada")]))
        .choices_for("projects/{project_id}/tasks", Choices::new().with("title", [("Build the rocket", "Rocket building")]));
    let app = Router::builder().discover().app_context(settings).fixture_pages_api(implementations(&store), Callers::default()).build();
    // The projects' own choice of an owner is the only one their pages
    // take for the field, so a value it does not name shows as it is.
    let list = get(&app, "/console/projects", "reader").await;
    assert!(list.html.contains(r#"<td data-field="ownerId">Countess Ada</td>"#), "{}", list.html);
    assert!(list.html.contains(r#"<td data-field="ownerId">grace</td>"#), "{}", list.html);
    let new = get(&app, "/console/projects/new", "writer").await;
    assert!(new.html.contains(r#"<option value="ada">Countess Ada</option>"#) && !new.html.contains("Grace Hopper"), "{}", new.html);
    let item = get(&app, &format!("/console/projects/{APOLLO}"), "reader").await;
    assert!(item.html.contains(r#"<div data-field="ownerId"><dt>Owner</dt><dd>Countess Ada</dd></div>"#), "{}", item.html);
    // The tasks' section on a project's page is the tasks', whose own
    // choices win over the app's.
    assert!(item.html.contains(">Rocket building</a>"), "{}", item.html);
    // A label's tasks, which have no choices of their own, take the app's.
    let labelled = get(&app, "/console/labels/rocket/tasks", "reader").await;
    assert!(labelled.html.contains(">The rocket<") && !labelled.html.contains("Rocket building"), "{}", labelled.html);
}

#[test]
#[should_panic(expected = "no resource of these pages is at \"project\"")]
fn a_choice_for_no_resource_is_refused() {
    let _ = Settings::new().choices_for("project", Choices::new());
}

#[tokio::test]
async fn names_come_from_the_type_and_the_path_never_a_guessed_singular() {
    let app = app(&Store::new());
    // Shelf declares no @display: the shelves are titled by their path's
    // word and a shelf by its type's name, never "Shelve".
    let shelves = get(&app, "/console/shelves", "shelver").await;
    assert_eq!(shelves.status, StatusCode::OK, "{}", shelves.html);
    assert!(shelves.html.contains("<h1>Shelves</h1>"), "{}", shelves.html);
    assert!(shelves.html.contains(r#"<li><a href="/console/shelves" aria-current="page">Shelves</a></li>"#), "{}", shelves.html);
    let shelf = get(&app, "/console/shelves/top", "shelver").await;
    assert_eq!(shelf.status, StatusCode::OK, "{}", shelf.html);
    assert!(shelf.html.contains("<h1>Shelf</h1>"), "{}", shelf.html);
    assert!(shelf.html.contains(r#"<li><span aria-current="page">Shelf top</span></li>"#), "{}", shelf.html);
    assert!(!shelf.html.contains("Shelve ") && !shelves.html.contains(">Shelve<"), "{}", shelf.html);

    // The archived projects' path extends the projects': they are titled
    // by their own word, not by Project's plural, and only an auditor
    // lists them.
    let archived = get(&app, "/console/projects/archived", "auditor").await;
    assert_eq!(archived.status, StatusCode::OK, "{}", archived.html);
    assert!(archived.html.contains("<h1>Archived</h1>"), "{}", archived.html);
    // Their rows are projects, so their link has the projects' icon too.
    assert!(archived.html.contains(r#"<li><a href="/console/projects/archived" aria-current="page"><span class="ss-icon" data-icon="folder" aria-hidden="true"></span>Archived</a></li>"#), "{}", archived.html);
    assert!(archived.html.contains(r#"<li><a href="/console/projects"><span class="ss-icon" data-icon="folder" aria-hidden="true"></span>Projects</a></li>"#), "{}", archived.html);
    let reader = get(&app, "/console/projects", "reader").await;
    assert!(!reader.html.contains("/console/projects/archived"), "{}", reader.html);
}

#[tokio::test]
async fn a_collection_whose_pages_would_meet_anothers_has_none() {
    // The app builds: projects/new, projects/{id}/edit and
    // projects/{id}/delete are the projects' new, edit and delete pages,
    // which Topcoat's router would refuse to register twice.
    let app = app(&Store::new());
    let new = get(&app, "/console/projects/new", "writer").await;
    assert!(new.html.contains("<h1>Start a project</h1>"), "{}", new.html);
    let edit = get(&app, &format!("/console/projects/{APOLLO}/edit"), "writer").await;
    assert!(edit.html.contains("<h1>Edit the project</h1>"), "{}", edit.html);
    let delete = get(&app, &format!("/console/projects/{APOLLO}/delete"), "writer").await;
    assert!(delete.html.contains("<h1>Delete the project</h1>"), "{}", delete.html);
    let item = get(&app, &format!("/console/projects/{APOLLO}"), "writer").await;
    assert!(!item.html.contains(r#"data-resource="edit""#) && !item.html.contains(r#"data-resource="delete""#), "{}", item.html);
}
`

// argsPagesAppTest is tests/app.rs of fixture-args-api's Topcoat crate with
// the pages on.
const argsPagesAppTest = `use std::sync::Arc;

use async_trait::async_trait;
use schemas_fixture_args_api_topcoat::api::runtime::{ApiError, RequestContext};
use schemas_fixture_args_api_topcoat::api::{
    types, Implementations, OrderCancelOrderArgs, OrderGetOrderArgs, OrderHoldOrderArgs, OrderImplementation,
    OrderListOrdersArgs, OrderNoteOrderArgs, OrderTagOrderArgs, ProductReviewsImplementation, ProductReviewsWriteReviewArgs,
};
use schemas_fixture_args_api_topcoat::RouterBuilderFixtureArgsApiExt;
use topcoat::router::{header, to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};

#[path = "support/enter.rs"]
mod enter;

const ORDER: &str = "8d1f6c9e-0000-4000-8000-000000000001";
const PRODUCT: &str = "8d1f6c9e-0000-4000-8000-000000000002";

fn uuid(text: &str) -> types::IdentityUUID {
    serde_json::from_value(serde_json::json!(text)).unwrap()
}

fn order(status: types::OrderStatus, note: &str) -> types::OrderView {
    types::OrderView { id: uuid(ORDER), status, note: Some(note.to_owned()) }
}

// Each operation answers one order, its note what it was asked.
struct Shop;

#[async_trait]
impl OrderImplementation for Shop {
    async fn list_orders(&self, _ctx: RequestContext, args: OrderListOrdersArgs) -> Result<Vec<types::OrderView>, ApiError> {
        Ok(args.statuses.unwrap_or_default().into_iter().map(|status| order(status, "listed")).collect())
    }
    async fn get_order(&self, _ctx: RequestContext, args: OrderGetOrderArgs) -> Result<types::OrderView, ApiError> {
        if args.id != uuid(ORDER) {
            return Err(ApiError::not_found("No such order"));
        }
        Ok(order(types::OrderStatus::Placed, "got"))
    }
    async fn cancel_order(&self, _ctx: RequestContext, args: OrderCancelOrderArgs) -> Result<types::OrderView, ApiError> {
        if args.reason.as_deref() == Some("too late") {
            return Err(ApiError::conflict("The order has shipped"));
        }
        Ok(order(types::OrderStatus::Cancelled, "cancelled"))
    }
    async fn hold_order(&self, _ctx: RequestContext, _args: OrderHoldOrderArgs) -> Result<types::OrderView, ApiError> {
        Ok(order(types::OrderStatus::Onhold, "held"))
    }
    async fn note_order(&self, _ctx: RequestContext, _args: OrderNoteOrderArgs) -> Result<types::OrderView, ApiError> {
        Ok(order(types::OrderStatus::Placed, "noted"))
    }
    async fn tag_order(&self, _ctx: RequestContext, _args: OrderTagOrderArgs) -> Result<types::OrderView, ApiError> {
        Ok(order(types::OrderStatus::Placed, "tagged"))
    }
}

#[async_trait]
impl ProductReviewsImplementation for Shop {
    async fn write_review(&self, _ctx: RequestContext, args: ProductReviewsWriteReviewArgs) -> Result<types::ReviewView, ApiError> {
        Ok(types::ReviewView { id: args.product_id, rating: args.input.rating, title: args.input.title })
    }
}

// An API whose operations need no caller: the app's setup takes no page
// authenticator, and every guard admits.
fn app() -> Router {
    Router::builder().discover().fixture_args_api(Implementations { order: Arc::new(Shop), product_reviews: Arc::new(Shop) }).build()
}

async fn send(method: &str, uri: &str, body: &str) -> (StatusCode, String, Option<String>) {
    let request = http::Request::builder()
        .method(method)
        .uri(uri)
        .header(header::CONTENT_TYPE, "application/x-www-form-urlencoded")
        .body(Body::from(body.to_owned()))
        .unwrap();
    let response = app().handle(request).await;
    let (status, headers) = (response.status(), response.headers().clone());
    let location = headers.get(header::LOCATION).map(|value| value.to_str().unwrap().to_owned());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let html = String::from_utf8(bytes.to_vec()).unwrap();
    enter::check_page(uri, &headers, &html);
    (status, html, location)
}

#[tokio::test]
async fn pages_serve_an_api_without_callers() {
    let (status, html, _) = send("GET", "/console", "").await;
    assert_eq!(status, StatusCode::OK, "{html}");
    assert!(html.contains(r#"<ul class="ss-index"><li><a href="/console/orders">Orders</a></li></ul>"#), "{html}");
    assert!(!html.contains("ss-sign-in"), "{html}");

    let (status, html, _) = send("GET", "/console/orders?statuses=placed&statuses=shipped", "").await;
    assert_eq!(status, StatusCode::OK, "{html}");
    assert_eq!(html.matches(r#"<a href="/console/orders/"#).count(), 2, "{html}");

    let (status, html, _) = send("GET", &format!("/console/orders/{ORDER}"), "").await;
    assert_eq!(status, StatusCode::OK, "{html}");
    for action in ["cancel", "hold", "notes", "tags"] {
        assert!(html.contains(&format!(r#"action="/console/orders/{ORDER}/{action}" class="ss-action" data-action="{action}""#)), "{action}: {html}");
    }
    assert!(html.contains(r#"<button type="submit">Cancel order</button>"#), "{html}");
}

#[tokio::test]
async fn an_action_without_callers_posts_and_refuses_by_its_status() {
    let (status, html, location) = send("POST", &format!("/console/orders/{ORDER}/hold"), "notify=on").await;
    assert_eq!(status, StatusCode::SEE_OTHER, "{html}");
    assert_eq!(location.as_deref(), Some(format!("/console/orders/{ORDER}").as_str()));

    let (status, html, _) = send("POST", &format!("/console/orders/{ORDER}/cancel"), "code=too_slow&reason=too+late").await;
    assert_eq!(status, StatusCode::CONFLICT, "{html}");
    assert!(html.contains(r#"<p class="form-error">The order has shipped</p>"#), "{html}");

    let (status, html, _) = send("POST", &format!("/console/orders/{ORDER}/tags"), "labels=%7Bnot+json").await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(html.contains(r#"<textarea"#) && html.contains("{not json"), "{html}");

    // A review, whose product nothing lists or reads, has no page.
    let (status, _, _) = send("GET", &format!("/console/products/{PRODUCT}/reviews/new"), "").await;
    assert_eq!(status, StatusCode::NOT_FOUND);
}
`

// TestPagesKeepTheirRoutesControls builds fixture-controls-api with the
// pages on, their lists live, and runs controlsPagesAppTest: the orders'
// list page, and its shard, which share the list's limiter, answer the
// third request in a minute with the route's 429 and Retry-After as the
// pages' refusal, an order's page, whose detail has no control, is not
// limited, and the hold action's post answers its route's rate limit,
// body limit and timeout with 429, 413 and 504. The shipments' page, whose
// list has no control, names each warehouse through listWarehouses, and
// meets its rate limit, which the warehouses' own page shares, and its
// timeout.
func TestPagesKeepTheirRoutesControls(t *testing.T) {
	cargoTestCrateWith(t, controlsService, controlsPagesAppTest, crateOptions{
		outputs:         livePagesOutputs(),
		devDependencies: `axum = "0.8.9"` + "\n",
	})
}

// controlsPagesAppTest is tests/app.rs of fixture-controls-api's Topcoat
// crate with the pages on.
const controlsPagesAppTest = `use std::collections::HashMap;
use std::sync::Arc;
use std::time::{Duration, Instant};

use async_trait::async_trait;
use axum::extract::Request;
use axum::middleware::Next;
use axum::response::{IntoResponse, Response};
use http::request::Parts;
use schemas_fixture_controls_api_topcoat::api::runtime::{
    ApiError, Authenticator, Principal, RequestContext, ServiceAuthenticator, ServiceCaller,
};
use schemas_fixture_controls_api_topcoat::api::{
    types, HookImplementation, HookReceivePaymentArgs, Implementations, OrderGetOrderArgs, OrderHoldOrderArgs,
    OrderImplementation, OrderListOrdersArgs, StockImplementation, StockReleaseReservationArgs, WebhookVerifier,
};
use schemas_fixture_controls_api_topcoat::{PageAuthenticator, RouterBuilderFixtureControlsApiExt};
use topcoat::context::Cx;
use topcoat::router::{header, to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};

#[path = "support/enter.rs"]
mod enter;

struct NoRequests;

#[async_trait]
impl Authenticator for NoRequests {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(None)
    }
}

struct NoServices;

#[async_trait]
impl ServiceAuthenticator for NoServices {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<ServiceCaller>, ApiError> {
        Ok(None)
    }
}

struct Unsigned;

#[async_trait]
impl WebhookVerifier for Unsigned {
    async fn verify(&self, _request: Request, _next: Next) -> Response {
        (StatusCode::UNAUTHORIZED, "unsigned").into_response()
    }
}

struct Caller;

#[async_trait]
impl PageAuthenticator for Caller {
    async fn principal(&self, _cx: &Cx) -> Result<Option<Principal>, ApiError> {
        Ok(Some(Principal::new("ada", ["stock"])))
    }
}

// A shop whose holds and warehouses take longer than their routes allow
// when slow.
struct Shop {
    slow: bool,
}

#[async_trait]
impl HookImplementation for Shop {
    async fn receive_payment(&self, _ctx: RequestContext, args: HookReceivePaymentArgs) -> Result<types::Receipt, ApiError> {
        Ok(types::Receipt { event_id: args.input.event_id })
    }
}

#[async_trait]
impl StockImplementation for Shop {
    async fn reindex_stock(&self, _ctx: RequestContext) -> Result<types::StockRun, ApiError> {
        Ok(types::StockRun { done: true })
    }
    async fn release_reservation(&self, _ctx: RequestContext, args: StockReleaseReservationArgs) -> Result<types::Reservation, ApiError> {
        Ok(types::Reservation { id: args.id, held: false })
    }
    async fn list_warehouses(&self, _ctx: RequestContext) -> Result<Vec<types::Warehouse>, ApiError> {
        if self.slow {
            tokio::time::sleep(Duration::from_secs(5)).await;
        }
        Ok(vec![types::Warehouse { id: "n".to_string(), name: "North".to_string() }])
    }
    async fn list_shipments(&self, _ctx: RequestContext) -> Result<Vec<types::Shipment>, ApiError> {
        Ok(vec![types::Shipment { id: "1".to_string(), warehouse_id: "n".to_string() }])
    }
}

#[async_trait]
impl OrderImplementation for Shop {
    async fn list_orders(&self, _ctx: RequestContext, _args: OrderListOrdersArgs) -> Result<Vec<types::OrderView>, ApiError> {
        Ok(vec![types::OrderView { id: "1".to_string() }])
    }
    async fn get_order(&self, _ctx: RequestContext, args: OrderGetOrderArgs) -> Result<types::OrderView, ApiError> {
        Ok(types::OrderView { id: args.id })
    }
    async fn hold_order(&self, _ctx: RequestContext, args: OrderHoldOrderArgs) -> Result<types::OrderView, ApiError> {
        if self.slow {
            tokio::time::sleep(Duration::from_secs(5)).await;
        }
        Ok(types::OrderView { id: args.id })
    }
    async fn place_order(&self, _ctx: RequestContext) -> Result<types::OrderView, ApiError> {
        Ok(types::OrderView { id: "1".to_string() })
    }
}

fn app(slow: bool) -> Router {
    let shop = Arc::new(Shop { slow });
    let verifier: Arc<dyn WebhookVerifier> = Arc::new(Unsigned);
    let implementations = Implementations {
        hook: shop.clone(),
        order: shop.clone(),
        stock: shop,
        authenticator: Arc::new(NoRequests),
        service_authenticator: Arc::new(NoServices),
        webhook_verifiers: HashMap::from([("stripe".to_string(), verifier)]),
    };
    Router::builder().discover().fixture_controls_api(implementations, Caller).build()
}

struct Answer {
    status: StatusCode,
    headers: http::HeaderMap,
    html: String,
}

async fn send(router: &Router, method: &str, uri: &str, content_type: &str, body: String, length: Option<usize>) -> Answer {
    let mut request = http::Request::builder().method(method).uri(uri).header(header::CONTENT_TYPE, content_type);
    if let Some(length) = length {
        request = request.header(header::CONTENT_LENGTH, length);
    }
    let response = router.handle(request.body(Body::from(body)).unwrap()).await;
    let status = response.status();
    let headers = response.headers().clone();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let html = String::from_utf8(bytes.to_vec()).unwrap();
    enter::check_page(uri, &headers, &html);
    Answer { status, headers, html }
}

async fn get(router: &Router, uri: &str) -> Answer {
    send(router, "GET", uri, "text/plain", String::new(), None).await
}

async fn hold(router: &Router, body: String, length: Option<usize>) -> Answer {
    send(router, "POST", "/console/orders/1/hold", "application/x-www-form-urlencoded", body, length).await
}

// The pages' refusal of a route control: its status, heading and message,
// without the layout.
fn refusal(answer: &Answer, status: u16, heading: &str, message: &str) {
    assert_eq!(answer.status.as_u16(), status, "{}", answer.html);
    let want = format!(r#"<section class="ss-refusal" data-status="{status}"><h1>{heading}</h1><p>{message}</p></section>"#);
    assert_eq!(answer.html, want);
    assert_eq!(answer.headers.get(header::CONTENT_TYPE).unwrap(), "text/html; charset=utf-8");
}

#[tokio::test]
async fn a_list_and_its_shard_meet_the_lists_rate_limit() {
    let app = app(false);
    // The list's route allows two a minute: its page's third is refused,
    // with the route's Retry-After.
    for _ in 0..2 {
        let list = get(&app, "/console/orders?status=placed").await;
        assert_eq!(list.status, StatusCode::OK, "{}", list.html);
    }
    let limited = get(&app, "/console/orders").await;
    refusal(&limited, 429, "Too many requests", "Too Many Requests");
    assert!(limited.headers.contains_key(header::RETRY_AFTER), "{:?}", limited.headers);
    // The list's shard calls the list too, and shares its pages' budget.
    let body = serde_json::json!({"args": ["", "/console/orders"]}).to_string();
    let shard = send(&app, "POST", "/console/_live/orders", "application/json", body, None).await;
    refusal(&shard, 429, "Too many requests", "Too Many Requests");
    // An order's page calls its detail, whose route has no control.
    for _ in 0..3 {
        let order = get(&app, "/console/orders/1").await;
        assert_eq!(order.status, StatusCode::OK, "{}", order.html);
    }

    // On another app the shard's request is admitted, and its two count.
    let app = self::app(false);
    let body = serde_json::json!({"args": ["status=placed", "/console/orders"]}).to_string();
    for _ in 0..2 {
        let shard = send(&app, "POST", "/console/_live/orders", "application/json", body.clone(), None).await;
        assert_eq!(shard.status, StatusCode::OK, "{}", shard.html);
    }
    refusal(&get(&app, "/console/orders").await, 429, "Too many requests", "Too Many Requests");
}

#[tokio::test]
async fn an_actions_post_meets_its_routes_controls() {
    let app = app(false);
    for _ in 0..2 {
        let held = hold(&app, "reason=Stock".to_owned(), None).await;
        assert_eq!(held.status, StatusCode::SEE_OTHER, "{}", held.html);
    }
    let limited = hold(&app, "reason=Stock".to_owned(), None).await;
    refusal(&limited, 429, "Too many requests", "Too Many Requests");
    assert!(limited.headers.contains_key(header::RETRY_AFTER), "{:?}", limited.headers);

    // A body past the route's megabyte, sent or declared, is refused before
    // the page reads its form.
    let app = self::app(false);
    let large = format!("reason={}", "a".repeat(1024 * 1024));
    refusal(&hold(&app, large, None).await, 413, "Too large", "Request body exceeds the accepted size");
    refusal(&hold(&app, "reason=Stock".to_owned(), Some(2 * 1024 * 1024)).await, 413, "Too large", "Request body exceeds the accepted size");

    // A hold past the route's second is refused with 504.
    let slow = self::app(true);
    let started = Instant::now();
    refusal(&hold(&slow, "reason=Stock".to_owned(), None).await, 504, "Timed out", "Gateway Timeout");
    assert!(started.elapsed() < Duration::from_secs(4), "{:?}", started.elapsed());
}

#[tokio::test]
async fn a_page_meets_the_controls_of_the_operations_its_references_call() {
    // The shipments' list has no control, but its rows name their
    // warehouses through listWarehouses, two a minute: the page's third in
    // a minute is refused, and the warehouses' own page shares the budget.
    let app = app(false);
    for _ in 0..2 {
        let shipments = get(&app, "/console/shipments").await;
        assert_eq!(shipments.status, StatusCode::OK, "{}", shipments.html);
        assert!(shipments.html.contains(r#"<td data-field="warehouseId">North</td>"#), "{}", shipments.html);
    }
    let limited = get(&app, "/console/shipments").await;
    refusal(&limited, 429, "Too many requests", "Too Many Requests");
    assert!(limited.headers.contains_key(header::RETRY_AFTER), "{:?}", limited.headers);
    refusal(&get(&app, "/console/warehouses").await, 429, "Too many requests", "Too Many Requests");

    // A listing past its route's second times the page out.
    let slow = self::app(true);
    let started = Instant::now();
    refusal(&get(&slow, "/console/shipments").await, 504, "Timed out", "Gateway Timeout");
    assert!(started.elapsed() < Duration::from_secs(4), "{:?}", started.elapsed());
}
`
