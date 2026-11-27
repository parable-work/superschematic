package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// entry is one entry of a test archive: a file with body, a directory
// when dir is set, a symbolic link to link, or a hard link to hard.
type entry struct {
	name, body, link, hard string
	dir                    bool
	mode                   int64
	kind                   byte
}

func tarball(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o644}
		if e.mode != 0 {
			hdr.Mode = e.mode
		}
		switch {
		case e.kind != 0:
			hdr.Typeflag = e.kind
		case e.dir:
			hdr.Typeflag, hdr.Mode = tar.TypeDir, 0o755
		case e.link != "":
			hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, e.link
		case e.hard != "":
			hdr.Typeflag, hdr.Linkname = tar.TypeLink, e.hard
		default:
			hdr.Typeflag, hdr.Size = tar.TypeReg, int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// tree lists what lies under dir: a file's body, "/" for a directory,
// "-> target" for a link.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(p, dir+string(filepath.Separator)))
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			got[rel] = "-> " + target
			return err
		case d.IsDir():
			got[rel] = "/"
		default:
			body, err := os.ReadFile(p)
			got[rel] = string(body)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestExtractTar(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []entry
		opts    Options
		// existing is written into dest first.
		existing map[string]string
		want     map[string]string
		// wantErr is a substring of the error; "" wants none.
		wantErr string
	}{
		{
			name: "files and directories",
			entries: []entry{
				{name: "a/", dir: true},
				{name: "a/b.txt", body: "b"},
				{name: "./c/d.txt", body: "d"},
				{name: "./", dir: true},
			},
			want: map[string]string{"a": "/", "a/b.txt": "b", "c": "/", "c/d.txt": "d"},
		},
		{
			name:    "a way up",
			entries: []entry{{name: "../escape", body: "x"}},
			wantErr: `"../escape" is outside the archive`,
		},
		{
			name:    "a way up after a way down",
			entries: []entry{{name: "a/../../escape", body: "x"}},
			wantErr: `"a/../../escape" is outside the archive`,
		},
		{
			name:    "an absolute path",
			entries: []entry{{name: "/etc/passwd", body: "x"}},
			wantErr: `"/etc/passwd" is outside the archive`,
		},
		{
			name:    "a backslash",
			entries: []entry{{name: `..\escape`, body: "x"}},
			wantErr: `"..\\escape" is outside the archive`,
		},
		{
			name:    "a symbolic link out of dest",
			entries: []entry{{name: "a/up", link: "../.."}},
			opts:    Options{Links: true},
			wantErr: `"a/up" links to "../..", outside the archive`,
		},
		{
			name:    "an absolute symbolic link",
			entries: []entry{{name: "etc", link: "/etc"}},
			opts:    Options{Links: true},
			wantErr: `"etc" links to "/etc", outside the archive`,
		},
		{
			name:    "a chain of symbolic links out of dest",
			entries: []entry{{name: "first", link: "second"}, {name: "second", link: ".."}},
			opts:    Options{Links: true},
			wantErr: `"second" links to "..", outside the archive`,
		},
		{
			// Written beneath the link, the target climbs out of dest,
			// though it reads as inside.
			name:    "a symbolic link out of dest through another",
			entries: []entry{{name: "s", link: "."}, {name: "s/s/evil", link: "../../escape"}},
			opts:    Options{Links: true},
			wantErr: `"s/s/evil" links to "../../escape", outside the archive`,
		},
		{
			// x/.. is dest while x is missing, and dest's parent once x
			// links to dest.
			name:    "a symbolic link climbing out of a path not made yet",
			entries: []entry{{name: "a", link: "x/.."}, {name: "x", link: "."}},
			opts:    Options{Links: true},
			wantErr: `"a" links to "x/..", outside the archive`,
		},
		{
			name: "symbolic links inside dest",
			entries: []entry{
				{name: "services/current", link: "shop-db"},
				{name: "services/shop-db/schema.json", body: "{}"},
				{name: "services/up", link: "../top"},
				{name: "top", body: "t"},
			},
			opts: Options{Links: true},
			want: map[string]string{
				"services": "/", "services/current": "-> shop-db", "services/shop-db": "/",
				"services/shop-db/schema.json": "{}", "services/up": "-> ../top", "top": "t",
			},
		},
		{
			name:    "a symbolic link without Links",
			entries: []entry{{name: "l", link: "a"}},
			wantErr: `"l" is neither a directory nor a regular file`,
		},
		{
			name:    "a hard link out of dest",
			entries: []entry{{name: "h", hard: "../escape"}},
			opts:    Options{Links: true},
			wantErr: `"h" links to "../escape", outside the archive`,
		},
		{
			name:    "a hard link out of dest through a symbolic link",
			entries: []entry{{name: "s", link: "."}, {name: "h", hard: "s/../escape"}},
			opts:    Options{Links: true},
			wantErr: `"h" links to "s/../escape", outside the archive`,
		},
		{
			name:    "a hard link inside dest",
			entries: []entry{{name: "top/a", body: "a"}, {name: "top/h", hard: "top/a"}},
			opts:    Options{Links: true, StripComponents: 1},
			want:    map[string]string{"a": "a", "h": "a"},
		},
		{
			name: "strip components",
			entries: []entry{
				{name: "pkg/", dir: true},
				{name: "README", body: "skipped"},
				{name: "pkg/lib/", dir: true},
				{name: "pkg/lib/x.a", body: "x"},
			},
			opts: Options{StripComponents: 1},
			want: map[string]string{"lib": "/", "lib/x.a": "x"},
		},
		{
			name:    "strip components after a way up",
			entries: []entry{{name: "pkg/../../escape", body: "x"}},
			opts:    Options{StripComponents: 1},
			wantErr: `"pkg/../../escape" is outside the archive`,
		},
		{
			name:     "a file that exists",
			entries:  []entry{{name: "a", body: "new"}},
			existing: map[string]string{"a": "old"},
			wantErr:  "file exists",
		},
		{
			name:    "a file twice",
			entries: []entry{{name: "a", body: "1"}, {name: "a", body: "2"}},
			wantErr: "file exists",
		},
		{
			name:    "a file written through a symbolic link that exists",
			entries: []entry{{name: "a/file", body: "x"}},
			opts:    Options{Links: true},
			// dest/a links out of dest, as nothing in an archive could.
			existing: map[string]string{"a": "-> .."},
			wantErr:  "escapes",
		},
		{
			name:    "another kind",
			entries: []entry{{name: "fifo", kind: tar.TypeFifo}},
			wantErr: `"fifo" is neither a directory nor a regular file`,
		},
		{
			name:    "another kind skipped",
			entries: []entry{{name: "fifo", kind: tar.TypeFifo}, {name: "l", link: "a"}, {name: "a", body: "a"}},
			opts:    Options{SkipOther: true},
			want:    map[string]string{"a": "a"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "dest")
			if err := os.Mkdir(dest, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, body := range tc.existing {
				var err error
				if target, ok := strings.CutPrefix(body, "-> "); ok {
					err = os.Symlink(target, filepath.Join(dest, name))
				} else {
					err = os.WriteFile(filepath.Join(dest, name), []byte(body), 0o644)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			err := ExtractTar(bytes.NewReader(tarball(t, tc.entries...)), dest, tc.opts)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if got := tree(t, dest); !maps.Equal(got, tc.want) {
					t.Errorf("dest holds %v, want %v", got, tc.want)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ExtractTar = %v, want an error holding %q", err, tc.wantErr)
			}
			if entries, err := os.ReadDir(parent); err != nil || len(entries) != 1 {
				t.Errorf("beside dest lie %v, %v; want nothing", entries, err)
			}
		})
	}
}

// TestExtractTarErrors: a refusal is an EntryError naming the entry, and
// its Err tells a path out of dest from a kind not extracted.
func TestExtractTarErrors(t *testing.T) {
	err := ExtractTar(bytes.NewReader(tarball(t, entry{name: "../x", body: "x"})), t.TempDir(), Options{})
	var entryErr *EntryError
	if !errors.As(err, &entryErr) || entryErr.Name != "../x" || !errors.Is(err, ErrOutside) {
		t.Errorf("a way up = %#v", err)
	}
	err = ExtractTar(bytes.NewReader(tarball(t, entry{name: "f", kind: tar.TypeFifo})), t.TempDir(), Options{})
	if !errors.Is(err, ErrKind) {
		t.Errorf("a fifo = %v", err)
	}
}

// TestExtractTarGzipAndModes: a gzipped stream is read with Gzip, dest is
// made when missing, and a file is 0o755 when any execute bit is set.
func TestExtractTarGzipAndModes(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(tarball(t, entry{name: "run", body: "#!", mode: 0o4710}, entry{name: "data", body: "d", mode: 0o600})); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "made", "here")
	if err := ExtractTar(&buf, dest, Options{Gzip: true}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]fs.FileMode{"run": 0o755, "data": 0o644} {
		info, err := os.Stat(filepath.Join(dest, name))
		if err != nil {
			t.Fatal(err)
		}
		// The umask may clear bits, never set them.
		if got := info.Mode(); got&^want != 0 || got&0o100 != want&0o100 {
			t.Errorf("%s has mode %v, want %v", name, got, want)
		}
	}
}
