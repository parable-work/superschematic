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

// noFormService is a service whose pages call no operation a form
// submits: a list without arguments and an item by its id, a path
// argument, so its crate has pages and neither an input form nor an
// argument form.
const noFormService = "fixture-pages-without-forms-api"

// writeNoFormService writes noFormService into a test's own directory.
func writeNoFormService(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), noFormService)
	files := map[string]string{
		"schema.config.yaml": "name: " + noFormService + "\nkind: API\noutputs: {}\n",
		"src/notes.schema.yaml": `name: ` + noFormService + `
kind: API
types:
  Note:
    name: Note
    role: APIView
    display:
      noun: Note
      titleField: title
    fields:
      - name: id
        typeRef: { name: string }
        required: true
      - name: title
        typeRef: { name: string }
        required: true
operationSets:
  - name: Notes
    operations:
      - name: listNotes
        typeRef: { name: Note, isArray: true }
        required: true
        httpMethod: GET
        restPath: notes
      - name: getNote
        typeRef: { name: Note }
        required: true
        httpMethod: GET
        restPath: "notes/{id}"
        arguments:
          - name: id
            typeRef: { name: string }
            required: true
`,
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestPagesWithoutAFormBuild builds the crate of a service whose pages
// call no operation a form submits: pages.rs reads Refusal and FormErrors
// from forms.rs, which the crate writes with no form, and which holds
// nothing a form alone would call, so the crate passes cargo clippy with
// warnings denied and its pages serve.
func TestPagesWithoutAFormBuild(t *testing.T) {
	dir := writeNoFormService(t)
	root := testpaths.TempDir(t)
	if _, err := buildDir(t, registry.DefaultNaming(), dir, root, pagesOutputs(), nil); err != nil {
		t.Fatalf("build: %v", err)
	}
	src := filepath.Join(topcoat.Dir(root, noFormService), "src")
	pages, err := os.ReadFile(filepath.Join(src, "pages.rs"))
	if err != nil {
		t.Fatalf("pages.rs: %v", err)
	}
	if !strings.Contains(string(pages), "use crate::forms::") {
		t.Fatalf("pages.rs reads nothing of forms.rs:\n%s", pages)
	}
	forms, err := os.ReadFile(filepath.Join(src, "forms.rs"))
	if err != nil {
		t.Fatalf("forms.rs: %v", err)
	}
	for _, want := range []string{"pub struct FormErrors", "pub struct Refusal"} {
		if !strings.Contains(string(forms), want) {
			t.Errorf("forms.rs lacks %s", want)
		}
	}
	for _, unwanted := range []string{"fn unshown(", "fn segments(", "fn choice_select("} {
		if strings.Contains(string(forms), unwanted) {
			t.Errorf("forms.rs of a crate without a form has %s", unwanted)
		}
	}

	cargoTestCrateWith(t, noFormService, noFormAppTest, crateOptions{dir: dir, outputs: pagesOutputs()})
}

// noFormAppTest serves the notes' list and item pages through the
// crate's router.
const noFormAppTest = `use async_trait::async_trait;
use schemas_fixture_pages_without_forms_api_topcoat::api::runtime::{ApiError, RequestContext};
use schemas_fixture_pages_without_forms_api_topcoat::api::{types, Implementations, NotesGetNoteArgs, NotesImplementation};
use schemas_fixture_pages_without_forms_api_topcoat::RouterBuilderFixturePagesWithoutFormsApiExt;
use topcoat::router::{to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};

#[path = "support/enter.rs"]
mod enter;

struct Notes;

fn note() -> types::Note {
    types::Note { id: "n1".to_owned(), title: "First".to_owned() }
}

#[async_trait]
impl NotesImplementation for Notes {
    async fn list_notes(&self, _ctx: RequestContext) -> Result<Vec<types::Note>, ApiError> {
        Ok(vec![note()])
    }
    async fn get_note(&self, _ctx: RequestContext, args: NotesGetNoteArgs) -> Result<types::Note, ApiError> {
        if args.id == "n1" { Ok(note()) } else { Err(ApiError::not_found("no such note")) }
    }
}

async fn get(uri: &str) -> (StatusCode, String) {
    let implementations = Implementations { notes: std::sync::Arc::new(Notes) };
    let router = Router::builder().discover().fixture_pages_without_forms_api(implementations).build();
    let response = router.handle(http::Request::builder().uri(uri).body(Body::empty()).unwrap()).await;
    let status = response.status();
    let headers = response.headers().clone();
    let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let html = String::from_utf8(body.to_vec()).unwrap();
    enter::check_page(uri, &headers, &html);
    (status, html)
}

#[tokio::test]
async fn the_pages_serve_without_a_form() {
    let (status, html) = get("/console/notes").await;
    assert_eq!(status, StatusCode::OK, "{html}");
    assert!(html.contains("First"), "{html}");
    let (status, html) = get("/console/notes/n1").await;
    assert_eq!(status, StatusCode::OK, "{html}");
    assert!(html.contains("First"), "{html}");
    let (status, html) = get("/console/notes/n2").await;
    assert_eq!(status, StatusCode::NOT_FOUND, "{html}");
}
`
