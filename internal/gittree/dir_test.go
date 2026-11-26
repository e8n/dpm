package gittree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gitTreeOfDir returns the tree that "git add -A" and "git write-tree"
// produce for dir, using a repository outside dir without any line ending
// conversion.
func gitTreeOfDir(t *testing.T, a Algorithm, dir string) string {
	t.Helper()
	repo := newRepo(t, a)
	args := []string{"--git-dir=" + filepath.Join(repo, ".git"), "--work-tree=" + dir}
	git(t, repo, append(args, "add", "-A")...)
	return git(t, repo, append(args, "write-tree")...)
}

func TestDirTreeMatchesGit(t *testing.T) {
	tests := []struct {
		name     string
		require  func(*testing.T)
		files    []file
		emptyDir []string
	}{
		{
			name: "files without normalization",
			files: []file{
				{path: "SKILL.md", data: "# skill\n"},
				{path: "crlf.txt", data: "line 1\r\nline 2\r\n"},
				{path: "mixed.txt", data: "lf\ncrlf\r\n"},
				{path: "binary.bin", data: "\x00\x01\xff\r\n"},
				{path: "empty.txt", data: ""},
				{path: "refs/deep/a.txt", data: "a\n"},
				{path: "a.txt", data: "a.txt\n"},
				{path: "a/x.txt", data: "a/x\n"},
				{path: "a0", data: "a0\n"},
				{path: "日本語.md", data: "こんにちは\n"},
			},
			emptyDir: []string{"empty", "refs/empty/nested"},
		},
		{
			name:     "only empty directories",
			emptyDir: []string{"x", "y/z"},
		},
		{
			name:    "execute bit",
			require: requireExecBit,
			files: []file{
				{path: "run.sh", data: "#!/bin/sh\n", exec: true},
				{path: "scripts/tool", data: "#!/bin/sh\n", exec: true},
				{path: "plain.txt", data: "plain\n"},
			},
		},
		{
			name:    "symbolic links",
			require: requireSymlinks,
			files: []file{
				{path: "target.txt", data: "target\n"},
				{path: "dir/inner.txt", data: "inner\n"},
				{path: "link-to-file", link: "target.txt"},
				{path: "link-to-dir", link: "dir"},
				{path: "dir/up", link: "../target.txt"},
				{path: "dangling", link: "does/not/exist"},
			},
		},
	}
	for _, a := range algorithms {
		for _, tt := range tests {
			t.Run(string(a)+"/"+tt.name, func(t *testing.T) {
				if tt.require != nil {
					tt.require(t)
				}
				dir := realTempDir(t)
				writeFiles(t, dir, tt.files)
				for _, d := range tt.emptyDir {
					if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(d)), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				got, err := DirTree(a, dir, nil)
				if err != nil {
					t.Fatal(err)
				}
				if want := gitTreeOfDir(t, a, dir); got.Hash != want {
					t.Errorf("DirTree = %s, git write-tree = %s", got.Hash, want)
				}
				if got.Algorithm != a {
					t.Errorf("Algorithm = %s, want %s", got.Algorithm, a)
				}
				if len(got.Entries) != len(tt.files) {
					t.Errorf("got %d entries, want %d: %v", len(got.Entries), len(tt.files), got.Entries)
				}
				for i := 1; i < len(got.Entries); i++ {
					if got.Entries[i-1].Path >= got.Entries[i].Path {
						t.Errorf("entries are not sorted: %q before %q", got.Entries[i-1].Path, got.Entries[i].Path)
					}
				}
			})
		}
	}
}

func TestDirTreeSymlinkContent(t *testing.T) {
	requireSymlinks(t)
	dir := realTempDir(t)
	writeFiles(t, dir, []file{
		{path: "sub/target.txt", data: "x"},
		{path: "link", link: "sub/target.txt"},
	})
	got, err := DirTree(SHA1, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Entry{Path: "link", Mode: ModeSymlink, Hash: BlobHash(SHA1, []byte("sub/target.txt"))}
	if got.Entries[0] != want {
		t.Errorf("entry = %+v, want %+v (the link target with / as the separator)", got.Entries[0], want)
	}
}

func TestDirTreeFileModes(t *testing.T) {
	files := []file{
		{path: "a.sh", data: "a\n"},
		{path: "b.sh", data: "b\n", exec: true},
		{path: "c.txt", data: "c\n"},
		{path: "sub/d.sh", data: "d\n"},
	}
	// The source says a.sh and sub/d.sh are executable and b.sh is not;
	// c.txt is missing from the source.
	source := []Entry{
		{Path: "a.sh", Mode: ModeExec},
		{Path: "b.sh", Mode: ModeFile},
		{Path: "sub/d.sh", Mode: ModeExec},
		{Path: "removed", Mode: ModeExec},
	}
	wantModes := map[string]Mode{"a.sh": ModeExec, "b.sh": ModeFile, "c.txt": ModeFile, "sub/d.sh": ModeExec}

	for _, a := range algorithms {
		t.Run(string(a), func(t *testing.T) {
			dir := realTempDir(t)
			writeFiles(t, dir, files)
			got, err := DirTree(a, dir, ModesFrom(source))
			if err != nil {
				t.Fatal(err)
			}
			var want []Entry
			for _, f := range files {
				want = append(want, Entry{Path: f.path, Mode: wantModes[f.path], Hash: BlobHash(a, []byte(f.data))})
			}
			wantHash, err := TreeHash(a, want)
			if err != nil {
				t.Fatal(err)
			}
			if got.Hash != wantHash {
				t.Errorf("DirTree = %s, want %s (entries %v)", got.Hash, wantHash, got.Entries)
			}
			for _, e := range got.Entries {
				if e.Mode != wantModes[e.Path] {
					t.Errorf("%s: mode %s, want %s", e.Path, e.Mode, wantModes[e.Path])
				}
			}
		})
	}
}

func TestDirTreeModesFromKeepsSymlinks(t *testing.T) {
	requireSymlinks(t)
	dir := realTempDir(t)
	writeFiles(t, dir, []file{{path: "link", link: "x"}})
	got, err := DirTree(SHA1, dir, ModesFrom([]Entry{{Path: "link", Mode: ModeExec}}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Entries[0].Mode != ModeSymlink {
		t.Errorf("mode = %s, want %s", got.Entries[0].Mode, ModeSymlink)
	}
}

func TestDirTreeFollowsRootLink(t *testing.T) {
	requireSymlinks(t)
	base := realTempDir(t)
	writeFiles(t, base, []file{{path: "real/a.txt", data: "a\n"}})
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(base, "real"), link); err != nil {
		t.Fatal(err)
	}
	got, err := DirTree(SHA1, link, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := DirTree(SHA1, filepath.Join(base, "real"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hash != want.Hash || len(got.Entries) != 1 {
		t.Errorf("DirTree through a link = %+v, want %+v", got, want)
	}
}

func TestDirTreeErrors(t *testing.T) {
	dir := realTempDir(t)
	writeFiles(t, dir, []file{{path: "file.txt", data: "x"}})
	tests := []struct {
		name string
		path string
		want string
	}{
		{"missing", filepath.Join(dir, "missing"), ""},
		{"file", filepath.Join(dir, "file.txt"), "not a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DirTree(SHA1, tt.path, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("DirTree error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
