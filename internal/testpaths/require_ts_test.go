package testpaths

import (
	"fmt"
	"strings"
	"testing"
)

// outcomeTB records whether a helper failed or skipped instead of ending
// the calling test.
type outcomeTB struct {
	testing.TB
	fatal, skip string
}

func (o *outcomeTB) Helper() {}

func (o *outcomeTB) Fatalf(format string, args ...any) { o.fatal = fmt.Sprintf(format, args...) }

func (o *outcomeTB) Skipf(format string, args ...any) { o.skip = fmt.Sprintf(format, args...) }

// TestRequireOrSkipTS: a TypeScript gate that cannot run skips by default
// and fails when SUPERSCHEMATIC_REQUIRE_TS_CHECKS=1, as CI sets it.
func TestRequireOrSkipTS(t *testing.T) {
	t.Setenv(RequireTSChecksEnv, "")
	skipped := &outcomeTB{TB: t}
	RequireOrSkipTS(skipped, "bun not available")
	if skipped.fatal != "" || !strings.Contains(skipped.skip, "bun not available") {
		t.Errorf("unset: fatal=%q skip=%q, want a skip naming the reason", skipped.fatal, skipped.skip)
	}

	t.Setenv(RequireTSChecksEnv, "1")
	failed := &outcomeTB{TB: t}
	RequireOrSkipTS(failed, "bun install failed")
	if failed.skip != "" || !strings.Contains(failed.fatal, "bun install failed") {
		t.Errorf("set: fatal=%q skip=%q, want a failure naming the reason", failed.fatal, failed.skip)
	}
}
