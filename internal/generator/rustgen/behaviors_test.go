package rustgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/behaviortypes/behaviortypestest"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestGeneratedBehaviors builds the behavior types fixture's crate (D58)
// and checks with serde that TicketBehaviors reads a read's behaviors,
// each behavior under its exact name, and writes them back unchanged.
func TestGeneratedBehaviors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the compiled behaviors check in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the compiled behaviors check")
	}
	reg := behaviortypestest.Registry(t)
	output, err := Generate(behaviortypestest.Schema(t, reg), Options{
		SchemaName: behaviortypestest.Service,
		Registry:   reg,
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	outDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := SetLocalPaths(output, testpaths.Local(t), outDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	testsDir := filepath.Join(outDir, "tests")
	if err := os.Mkdir(testsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	crate := strings.ReplaceAll(output.CrateName, "-", "_")
	test := `use ` + crate + `::{TicketBehaviors, TicketState, NoteBehaviors};

const READ: &str = r#"` + behaviortypestest.TicketBehaviors + `"#;

#[test]
fn reads_and_writes_a_ticket_s_behaviors() {
    let behaviors: TicketBehaviors = serde_json::from_str(READ).expect("decode");
    assert_eq!(behaviors.workflow.status, TicketState::InProgress);
    assert_eq!(behaviors.comments.comment_count, 2);
    assert_eq!(behaviors.revisions.revision, Some(3));
    assert_eq!(behaviors.revisions.pending_proposals, None);
    assert_eq!(behaviors.links.targets.project.id, "launch");
    assert_eq!(behaviors.links.targets.reviewers.as_ref().map(Vec::len), Some(2));
    assert_eq!(behaviors.links.targets.spec.as_ref().and_then(|spec| spec.stale), Some(true));
    assert!(behaviors.timers.deadlines.close.is_none());
    assert_eq!(behaviors.timers.deadlines.respond.as_ref().and_then(|d| d.outcome.as_deref()), Some("moved"));
    assert_eq!(behaviors.approvals.gates.done.as_ref().map(|gate| gate.signed_by.clone()), Some(vec!["ana".to_string()]));
    assert_eq!(behaviors.acme_rating.rating_count, Some(serde_json::json!(4)));
    let written = serde_json::to_value(&behaviors).expect("encode");
    let read: serde_json::Value = serde_json::from_str(READ).expect("parse");
    assert_eq!(written, read);
}

#[test]
fn a_state_the_workflow_lacks_is_refused() {
    let read = READ.replace("in-progress", "closed");
    assert!(serde_json::from_str::<TicketBehaviors>(&read).is_err());
}

#[test]
fn behaviors_without_fields_are_an_empty_object() {
    let behaviors: NoteBehaviors = serde_json::from_str("{}").expect("decode");
    assert_eq!(serde_json::to_string(&behaviors).expect("encode"), "{}");
}
`
	if err := os.WriteFile(filepath.Join(testsDir, "behaviors.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(cargo, "test", "--quiet")
	cmd.Dir = outDir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+filepath.Join(outDir, "target"), "RUSTFLAGS=-D warnings")
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated Rust tests: %v\n%s", err, data)
	}
}
