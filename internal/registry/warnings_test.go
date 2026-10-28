package registry

import (
	"bytes"
	"reflect"
	"testing"
)

// TestWarnfRecordsAWarningOnce (D100): a generator's warning lands on the
// run's Result and in its log, "warning: " first, as the load prints the
// verification pass's; the same warning met again is neither.
func TestWarnfRecordsAWarningOnce(t *testing.T) {
	var log bytes.Buffer
	ctx := GenerateContext{Options: Options{Log: &log}, Result: &Result{Outputs: map[string]string{}}}
	ctx.Warnf("site %s: its package lacks %s", "shop-web", "@acme/shop-api-sdk")
	ctx.Warnf("site %s: its package lacks %s", "shop-web", "@acme/shop-api-sdk")
	ctx.Warnf("stack %s: the implementation of %s lacks %s", "shop", "shop-api", "@acme/shop-db-types")
	want := []string{"site shop-web: its package lacks @acme/shop-api-sdk", "stack shop: the implementation of shop-api lacks @acme/shop-db-types"}
	if !reflect.DeepEqual(ctx.Result.Warnings, want) {
		t.Errorf("Warnings = %q, want %q", ctx.Result.Warnings, want)
	}
	if got := log.String(); got != "warning: "+want[0]+"\nwarning: "+want[1]+"\n" {
		t.Errorf("log = %q", got)
	}
}
