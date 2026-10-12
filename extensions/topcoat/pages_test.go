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

// TestPagesServeATopcoatApp builds fixture-pages-api's crates with the
// pages on and runs cargo clippy, then cargo test, with pagesAppTest: an
// app discovers the pages; the index and the nav list what the caller may
// list; a list filters, links its rows to their items and offers its new
// page to a writer; an item's page shows its detail, its edit and delete
// links, its action's form and its tasks' section; the new, edit, delete
// and action pages post, redirect (303) and refuse with 422 at the
// control, the edit form filled from the item and a conflict (409) the
// form's message; a nested resource's pages live under its parent's item;
// and a refused caller keeps the route's status: 401 with a sign-in link
// that comes back, 403 and 404, a route's value that does not decode 404.
func TestPagesServeATopcoatApp(t *testing.T) {
	cargoTestCrate(t, pagesService, pagesAppTest)
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
	if !strings.Contains(files["views.rs"], "pub async fn project_table(rows: Vec<ProjectRecord>, #[default] hrefs: Vec<String>)") {
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
// outputs.topcoat.pages would give it. A bad prefix fails assembly.
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
	for _, table := range []string{"pages_prefix = \"/api\"", "pages_prefix = true"} {
		names, err := registry.ParseNaming([]byte("[extension.topcoat]\n"+table+"\n"), "superschematic.toml")
		if err != nil {
			t.Fatalf("ParseNaming: %v", err)
		}
		if _, err := registry.Assemble(names, topcoat.Extension{}); err == nil {
			t.Errorf("[extension.topcoat] %s: assembled, want it refused", table)
		}
	}
}

// TestATableLinksItsRows builds fixture-views-api, whose crate has no
// pages: a table's hrefs link each row's title cell, or its first cell for
// a type whose columns hold no title, and a row without an href, or past
// their end, links nowhere.
func TestATableLinksItsRows(t *testing.T) {
	files, _ := readCrate(t, registry.DefaultNaming(), viewsService, rustOutputs())
	views := files["views.rs"]
	for _, want := range []string{
		"pub async fn order_view_table(rows: Vec<OrderViewRecord>, #[default] hrefs: Vec<String>)",
		"let href = hrefs.get(index).filter(|href| !href.is_empty()).cloned();",
		"<th scope=\"row\" data-field=\"reference\">\n                            if let Some(href) = &href {\n                                <a href=(href)>(row.reference)</a>",
		"<td data-field=\"text\">\n                            if let Some(href) = &href {\n                                <a href=(href)>(row.text)</a>",
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
const pagesAppTest = `use std::sync::{Arc, Mutex};

use async_trait::async_trait;
use http::request::Parts;
use schemas_fixture_pages_api_topcoat::api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use schemas_fixture_pages_api_topcoat::api::{
    types, Implementations, MemberImplementation, ProjectArchiveProjectArgs, ProjectCreateProjectArgs,
    ProjectDeleteProjectArgs, ProjectGetProjectArgs, ProjectImplementation, ProjectListProjectsArgs,
    ProjectUpdateProjectArgs, TaskAddTaskArgs, TaskGetTaskArgs, TaskImplementation, TaskListTasksArgs,
    ViewerImplementation,
};
use schemas_fixture_pages_api_topcoat::pages::Settings;
use schemas_fixture_pages_api_topcoat::{PageAuthenticator, RouterBuilderFixturePagesApiExt};
use topcoat::context::Cx;
use topcoat::router::request::parts;
use topcoat::router::{header, to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};

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

// The projects, their tasks and the members, in memory, as one app's
// requests change them.
struct Store {
    projects: Mutex<Vec<types::Project>>,
    tasks: Mutex<Vec<(types::IdentityUUID, types::Task)>>,
    next: Mutex<u32>,
}

impl Store {
    fn new() -> Arc<Self> {
        Arc::new(Store {
            projects: Mutex::new(vec![
                types::Project {
                    id: uuid(APOLLO),
                    name: "Apollo".to_owned(),
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
        Ok(tasks.iter().filter(|(project, _)| *project == args.project_id).map(|(_, task)| task.clone()).collect())
    }
    async fn get_task(&self, _ctx: RequestContext, args: TaskGetTaskArgs) -> Result<types::Task, ApiError> {
        let tasks = self.0.tasks.lock().unwrap();
        tasks
            .iter()
            .find(|(project, task)| *project == args.project_id && task.id == args.id)
            .map(|(_, task)| task.clone())
            .ok_or_else(|| ApiError::not_found("No such task"))
    }
    async fn add_task(&self, _ctx: RequestContext, args: TaskAddTaskArgs) -> Result<types::Task, ApiError> {
        self.0.project(&args.project_id)?;
        let task = types::Task { id: self.0.new_id(), title: args.input.title, done: false };
        self.0.tasks.lock().unwrap().push((args.project_id, task.clone()));
        Ok(task)
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
// a reader of members, one with no permission, or none.
struct Callers;

#[async_trait]
impl PageAuthenticator for Callers {
    async fn principal(&self, cx: &Cx) -> Result<Option<Principal>, ApiError> {
        let caller = parts(cx).headers.get("x-caller").and_then(|value| value.to_str().ok()).unwrap_or_default();
        let permissions: &[&str] = match caller {
            "reader" => &["projects.read"],
            "writer" => &["projects"],
            "members" => &["members.read"],
            "nobody" => &[],
            _ => return Ok(None),
        };
        Ok(Some(Principal::new(caller, permissions.iter().copied())))
    }
}

fn implementations(store: &Arc<Store>) -> Implementations {
    Implementations {
        member: Arc::new(Shop(Arc::clone(store))),
        project: Arc::new(Shop(Arc::clone(store))),
        task: Arc::new(Shop(Arc::clone(store))),
        viewer: Arc::new(Shop(Arc::clone(store))),
        authenticator: Arc::new(NoRequests),
    }
}

// The app: the crate's pages by discovery, under the app's settings.
fn app(store: &Arc<Store>) -> Router {
    Router::builder()
        .discover()
        .app_context(Settings::new().title("Projects console").sign_in("/sign-in"))
        .fixture_pages_api(implementations(store), Callers)
        .build()
}

// The same app without settings: a refusal has no sign-in link.
fn bare_app(store: &Arc<Store>) -> Router {
    Router::builder().discover().fixture_pages_api(implementations(store), Callers).build()
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
    let status = response.status();
    let location = response.headers().get(header::LOCATION).map(|value| value.to_str().unwrap().to_owned());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    Answer { status, html: String::from_utf8(bytes.to_vec()).unwrap(), location }
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
    assert!(index.html.contains(r#"<ul class="ss-index"><li><a href="/console/projects">Projects</a></li></ul>"#), "{}", index.html);
    assert!(!index.html.contains("/console/members"), "a reader of projects sees members: {}", index.html);
    assert!(index.html.contains(r#"<li><span aria-current="page">Projects console</span></li>"#), "{}", index.html);

    let index = get(&app, "/console", "members").await;
    assert!(index.html.contains(r#"<li><a href="/console/members">Members</a></li>"#), "{}", index.html);
    assert!(!index.html.contains("/console/projects"), "a reader of members sees projects: {}", index.html);

    // Signed out, the index lists nothing and offers to sign in.
    let index = get(&app, "/console", "").await;
    assert_eq!(index.status, StatusCode::OK);
    assert!(index.html.contains("There is nothing here you may list."), "{}", index.html);
    assert!(index.html.contains(r#"<a class="ss-sign-in" href="/sign-in?next=%2Fconsole">Sign in</a>"#), "{}", index.html);

    // The nav marks the current resource: its list as the page, an item's
    // page as under it.
    let list = get(&app, "/console/projects", "reader").await;
    assert!(list.html.contains(r#"<nav class="ss-nav" aria-label="Resources"><ul><li><a href="/console/projects" aria-current="page">Projects</a></li></ul></nav>"#), "{}", list.html);
    let item = get(&app, &format!("/console/projects/{APOLLO}"), "reader").await;
    assert!(item.html.contains(r#"<li><a href="/console/projects" aria-current="true">Projects</a></li>"#), "{}", item.html);
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
    assert!(list.html.contains(r#"<form method="get" class="ss-filter">"#), "{}", list.html);
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
        format!(r#"<li><a href="/console/projects">Projects</a></li><li><span aria-current="page">Project {APOLLO}</span></li>"#),
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
    assert!(new.html.contains(r#"<li><a href="/console/projects">Projects</a></li><li><span aria-current="page">Start a project</span></li>"#), "{}", new.html);

    let added = post(&app, "/console/projects/new", "writer", "name=Mercury&status=planned&budget=10&_action=add:tags").await;
    assert_eq!(added.status, StatusCode::OK, "{}", added.html);
    assert!(added.html.contains(r#"name="tags[0]""#), "{}", added.html);
    assert_eq!(store.projects.lock().unwrap().len(), 2, "a row button created a project");

    let short = post(&app, "/console/projects/new", "writer", "name=A&status=planned&budget=10").await;
    assert_eq!(short.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", short.html);
    assert!(short.html.contains(r#"value="A""#), "{}", short.html);
    assert!(short.html.contains(r#"<p class="field-error">"#), "{}", short.html);

    let created = post(&app, "/console/projects/new", "writer", "name=Mercury&status=planned&budget=10&description=First").await;
    assert_eq!(created.status, StatusCode::SEE_OTHER, "{}", created.html);
    let location = created.location.unwrap();
    let id = location.strip_prefix("/console/projects/").unwrap_or_default();
    assert!(!id.is_empty() && !id.contains('/'), "{location}");
    let shown = get(&app, &location, "writer").await;
    assert!(shown.html.contains("<h1>Mercury</h1>"), "{}", shown.html);

    let refused = get(&app, "/console/projects/new", "reader").await;
    assert_eq!(refused.status, StatusCode::FORBIDDEN, "{}", refused.html);
    let refused = post(&app, "/console/projects/new", "reader", "name=Venus&status=planned&budget=10").await;
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
        r#"value="1200.5""#,
        r#"value="To the moon""#,
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
        &format!("id={APOLLO}&name=Apollo+X&status=active&budget=1&tags[0]=moon&_action=add:tags"),
    )
    .await;
    assert_eq!(added.status, StatusCode::OK, "{}", added.html);
    assert!(added.html.contains(r#"name="tags[1]""#) && added.html.contains(r#"value="Apollo X""#), "{}", added.html);
    assert_eq!(store.project(&uuid(APOLLO)).unwrap().name, "Apollo");

    let refused = post(&app, &format!("/console/projects/{APOLLO}/edit"), "writer", &format!("id={APOLLO}&name=Apollo&status=active&budget=-1")).await;
    assert_eq!(refused.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", refused.html);
    assert!(refused.html.contains(r#"value="-1""#), "{}", refused.html);

    // The route's id is the project changed, whatever the hidden input says.
    let saved = post(
        &app,
        &format!("/console/projects/{APOLLO}/edit"),
        "writer",
        &format!("id={GEMINI}&name=Apollo+II&status=done&budget=1300&description=Again&notifyOwner=on&tags[0]=sun&milestones[0].title=Return"),
    )
    .await;
    assert_eq!(saved.status, StatusCode::SEE_OTHER, "{}", saved.html);
    assert_eq!(saved.location.as_deref(), Some(format!("/console/projects/{APOLLO}").as_str()));
    let project = store.project(&uuid(APOLLO)).unwrap();
    assert_eq!(project.name, "Apollo II");
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
        r#"<li><a href="/console/projects">Projects</a></li><li><a href="/console/projects/{APOLLO}">Project {APOLLO}</a></li><li><span aria-current="page">Tasks</span></li>"#
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

    // A refused post signs in to come back to its page: the new page, or
    // the item's for an action, which has none of its own.
    let created = post(&app, "/console/projects/new", "", "name=Venus&status=planned&budget=10").await;
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
    let status = response.status();
    let location = response.headers().get(header::LOCATION).map(|value| value.to_str().unwrap().to_owned());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, String::from_utf8(bytes.to_vec()).unwrap(), location)
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
