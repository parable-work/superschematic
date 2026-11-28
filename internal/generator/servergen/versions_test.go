package servergen

import (
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// A third-party module the entrypoint's go.mod requires is required at the
// version that ir and the runtime modules which import it require, so a
// generated server builds against the version they are tested with. An
// indirect requirement of theirs is left to minimal version selection:
// google.golang.org/api, say, is the version the GCS client asks for.
func TestThirdPartyVersionsMatchTheRuntime(t *testing.T) {
	runtime := testpaths.RuntimeRequirements(t)
	for module, version := range map[string]string{
		"github.com/go-chi/chi/v5": chiVersion,
		"github.com/jackc/pgx/v5":  pgxVersion,
		"go.uber.org/zap":          zapVersion,
		cloudSQLConnModule:         cloudSQLConnVersion,
		storageModule:              storageVersion,
		googleAPIModule:            googleAPIVersion,
		redisModule:                redisVersion,
	} {
		for mod, req := range runtime[module] {
			if !req.Indirect && req.Version != version {
				t.Errorf("servergen requires %s %s; %s requires %s", module, version, mod, req.Version)
			}
		}
	}
}
