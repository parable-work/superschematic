package tsrestgen

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExportsName reads whether an implementation exports createJobs: by
// a declaration, a renamed or plain export list, or a re-export of a whole
// module, which may; not by a local name, a test file or a declaration
// file (D52).
func TestExportsName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{"const", map[string]string{"index.ts": "export const createJobs: JobsConstructor = deps => ({});\n"}, true},
		{"async function", map[string]string{"jobs.ts": "export async function createJobs(deps) {\n  return {};\n}\n"}, true},
		{"export list", map[string]string{"index.ts": "const createJobs = () => ({});\nexport { create, createJobs };\n"}, true},
		{"renamed", map[string]string{"index.ts": "const jobs = () => ({});\nexport { jobs as createJobs };\n"}, true},
		{"re-export", map[string]string{"index.ts": "export * from './jobs';\n"}, true},
		{"local only", map[string]string{"index.ts": "const createJobs = () => ({});\nexport const create = () => ({});\n"}, false},
		{"another export", map[string]string{"index.ts": "export const createJobsLater = () => ({});\n"}, false},
		{"test file", map[string]string{"index.ts": "export const create = () => ({});\n", "index.test.ts": "export const createJobs = 1;\n"}, false},
		{"declaration file", map[string]string{"index.ts": "export const create = () => ({});\n", "jobs.d.ts": "export declare const createJobs: unknown;\n"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, text := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ExportsName(dir, JobsFunc)
			if err != nil || got != tc.want {
				t.Fatalf("ExportsName = %t, %v; want %t", got, err, tc.want)
			}
		})
	}
}
