package rustgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestUserModelCrateCompiles generates fixture-user-model-db's Rust types
// crate, whose Session, UserCredential and UserRoleGrant the loader adds
// beside its User and Role tables (D50), and builds it with a test that
// decodes a session and a role grant and encodes a credential.
func TestUserModelCrateCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the compiled crate in short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the compiled crate")
	}
	const service = "fixture-user-model-db"
	schema, err := loader.LoadService(filepath.Join(fixturesDir, service))
	if err != nil {
		t.Fatalf("load %s: %v", service, err)
	}
	output, err := Generate(schema, Options{SchemaName: service, Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	outDir := testpaths.TempDir(t)
	if err := SetLocalPaths(output, testpaths.Local(t), outDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	testsDir := filepath.Join(outDir, "tests")
	if err := os.Mkdir(testsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	crate := strings.ReplaceAll(output.CrateName, "-", "_")
	source := strings.ReplaceAll(userModelCrateTest, "CRATE", crate)
	if err := os.WriteFile(filepath.Join(testsDir, "user_model.rs"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(cargoPath, "test", "--test", "user_model")
	cmd.Dir = outDir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+filepath.Join(outDir, "target"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cargo test failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "test user_model_tables_round_trip ... ok") {
		t.Fatalf("the generated crate did not run user_model_tables_round_trip:\n%s", out)
	}
}

// userModelCrateTest is the generated crate's tests/user_model.rs; CRATE is
// the crate's name as Rust spells it.
const userModelCrateTest = `use CRATE::*;

#[test]
fn user_model_tables_round_trip() {
    let hash = "ab".repeat(32);
    let session: Session = serde_json::from_value(serde_json::json!({
        "tokenHash": hash,
        "createdAt": "2026-01-02T03:04:05Z",
        "expiresAt": "2026-01-16T03:04:05Z",
        "user": {"email": "alice@example.com", "displayName": "Alice", "createdAt": "2026-01-02T03:04:05Z"}
    }))
    .expect("decode a session");
    assert_eq!(session.token_hash, hash);
    assert!(session.revoked_at.is_none() && session.last_seen_at.is_none());
    assert_eq!(session.user.expect("the session's user").email, "alice@example.com");

    let grant: UserRoleGrant = serde_json::from_value(serde_json::json!({
        "grantedAt": "2026-01-02T03:04:05Z",
        "role": {"name": "admin", "permissions": ["identity.users.read"]}
    }))
    .expect("decode a grant");
    assert_eq!(grant.role.expect("the grant's role").permissions, vec!["identity.users.read".to_string()]);

    let credential: UserCredential = serde_json::from_value(serde_json::json!({
        "passwordHash": "$argon2id$v=19$m=19456,t=2,p=1$salt$hash",
        "passwordChangedAt": "2026-01-02T03:04:05Z"
    }))
    .expect("decode a credential");
    let encoded = serde_json::to_value(&credential).expect("encode a credential");
    assert_eq!(encoded["passwordHash"], "$argon2id$v=19$m=19456,t=2,p=1$salt$hash");
    assert!(encoded.get("disabledAt").is_none());
}
`
