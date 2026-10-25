package local_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/parable-work/superschematic/internal/stack/local"
)

// TestValkeyMatchesCIPin keeps the image the local target runs a memory
// store on at VALKEY_VERSION in tools.env, the Valkey CI's memory store
// tests run against (D75), so the runtimes are tested on the server stack
// dev runs.
func TestValkeyMatchesCIPin(t *testing.T) {
	tools, err := os.ReadFile(filepath.Join("..", "..", "..", "tools.env"))
	if err != nil {
		t.Fatal(err)
	}
	pin := regexp.MustCompile(`(?m)^VALKEY_VERSION=(\S+)$`).FindSubmatch(tools)
	if pin == nil {
		t.Fatal("tools.env pins no VALKEY_VERSION")
	}
	if want := "valkey/valkey:" + string(pin[1]) + "-alpine"; local.DefaultMemoryStoreImage != want {
		t.Errorf("the local target runs %s; tools.env pins CI's Valkey at %s", local.DefaultMemoryStoreImage, want)
	}
}
