package cli

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tarEntry is one entry of a test archive: a file with body, a directory
// when dir is set, or a link to link.
type tarEntry struct {
	name, body, link string
	dir              bool
}

func tarStream(t *testing.T, entries ...tarEntry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		header := &tar.Header{Name: e.name, Mode: 0o644}
		switch {
		case e.dir:
			header.Typeflag, header.Mode = tar.TypeDir, 0o755
		case e.link != "":
			header.Typeflag, header.Linkname = tar.TypeSymlink, e.link
		default:
			header.Typeflag, header.Size = tar.TypeReg, int64(len(e.body))
		}
		require.NoError(t, tw.WriteHeader(header))
		if header.Typeflag == tar.TypeReg {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	return &buf
}

func TestUntarExtractsLinksInsideTheArchive(t *testing.T) {
	dir := t.TempDir()
	err := untar(tarStream(t,
		tarEntry{name: "services/", dir: true},
		// A link may come before its target, as git archive orders by path.
		tarEntry{name: "services/current", link: "shop-db"},
		tarEntry{name: "services/shop-db/schema.json", body: "{}"},
	), dir)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(dir, "services", "current", "schema.json"))
	require.NoError(t, err)
	assert.Equal(t, "{}", string(body))
}

func TestUntarRefusesPathsOutsideTheArchive(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []tarEntry
		want    string
	}{
		{"a name with ..", []tarEntry{{name: "../escape.json", body: "{}"}}, `"../escape.json" is outside the archive`},
		{"an absolute name", []tarEntry{{name: "/escape.json", body: "{}"}}, `"/escape.json" is outside the archive`},
		{"an absolute link", []tarEntry{{name: "etc", link: "/etc"}}, `"etc" links to "/etc", outside the archive`},
		{"a link up and out", []tarEntry{{name: "a/up", link: "../.."}}, `"a/up" links to "../..", outside the archive`},
		{
			"a chain of links that leaves",
			[]tarEntry{{name: "first", link: "second"}, {name: "second", link: ".."}},
			`"second" links to "..", outside the archive`,
		},
		{
			"a file written through a link that leaves",
			// The link itself is refused first, so nothing is written
			// outside.
			[]tarEntry{{name: "out", link: "../sibling"}, {name: "out/file.json", body: "{}"}},
			`"out" links to "../sibling", outside the archive`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "root")
			require.NoError(t, os.Mkdir(dir, 0o755))
			err := untar(tarStream(t, tc.entries...), dir)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			entries, err := os.ReadDir(parent)
			require.NoError(t, err)
			assert.Len(t, entries, 1, "nothing is written beside the archive root")
		})
	}
}
