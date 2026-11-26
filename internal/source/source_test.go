package source

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/e8n/dpm/internal/gittree"
)

// readFile returns the contents of the file p (a slash path) under dir.
func readFile(t *testing.T, dir, p string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWriteTo(t *testing.T) {
	r := newRemote(t, gittree.SHA1)
	s := fetchOne(t, depSpec{name: "a", git: r.url, subdir: "skills/a"})
	dst := filepath.Join(realTempDir(t), "staging", "a")
	if err := s.WriteTo(dst); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dst, "SKILL.md"); got != "a v3\n" {
		t.Errorf("SKILL.md = %q", got)
	}
	if got := readFile(t, dst, "run.sh"); got != "#!/bin/sh\n" {
		t.Errorf("run.sh = %q", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dst, "run.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&0o100 == 0 {
			t.Errorf("run.sh has mode %v, want it executable", info.Mode())
		}
		info, err = os.Stat(filepath.Join(dst, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&0o111 != 0 {
			t.Errorf("SKILL.md has mode %v, want it not executable", info.Mode())
		}
	}
	wantDirIntegrity(t, s, dst)

	// Existing files are not overwritten.
	if err := s.WriteTo(dst); err == nil {
		t.Error("WriteTo over existing files succeeded")
	}
}

// wantDirIntegrity checks that the directory written from s hashes to the
// integrity of s, with the modes of the source, as the execute bit is not
// kept on Windows.
func wantDirIntegrity(t *testing.T, s *Source, dir string) {
	t.Helper()
	got, err := gittree.DirTree(s.Tree.Algorithm, dir, gittree.ModesFrom(s.Tree.Entries))
	if err != nil {
		t.Fatal(err)
	}
	if got.Integrity() != s.Integrity() {
		t.Errorf("written files hash to %s, want %s", got.Integrity(), s.Integrity())
	}
}

func TestWriteToSymlink(t *testing.T) {
	r := newRemote(t, gittree.SHA1)
	// The link is recorded without creating it on disk, so that the
	// repository can be made where links cannot.
	h := gitInput(t, r.dir, "SKILL.md", "hash-object", "-w", "--stdin")
	git(t, r.dir, "update-index", "--add", "--cacheinfo", "120000,"+h+",skills/a/sub/link")
	h = gitInput(t, r.dir, "../SKILL.md", "hash-object", "-w", "--stdin")
	git(t, r.dir, "update-index", "--add", "--cacheinfo", "120000,"+h+",skills/a/up")
	git(t, r.dir, "commit", "-q", "-m", "links")

	s := fetchOne(t, depSpec{name: "a", git: r.url, subdir: "skills/a"})
	dst := filepath.Join(realTempDir(t), "a")
	err := s.WriteTo(dst)
	if !canSymlink(t) {
		if err == nil {
			t.Fatal("WriteTo created symbolic links where links cannot be created")
		}
		t.Skipf("symbolic links cannot be created here: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"sub/link": "SKILL.md", "up": "../SKILL.md"} {
		target, err := os.Readlink(filepath.Join(dst, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		if filepath.ToSlash(target) != want {
			t.Errorf("%s points to %q, want %q", p, target, want)
		}
	}
	wantDirIntegrity(t, s, dst)
}

// TestWriteToLineEndings checks that the written bytes are the blobs,
// whatever core.autocrlf and .gitattributes say about checkouts.
func TestWriteToLineEndings(t *testing.T) {
	// core.autocrlf=true for every git command, including those of dpm.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.autocrlf")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")

	files := []file{
		{path: ".gitattributes", data: "*.bin -text\n*.crlf text eol=crlf\n"},
		{path: "skill/a.txt", data: "x\r\ny\r\n"},
		{path: "skill/b.bin", data: "x\r\n"},
		{path: "skill/c.crlf", data: "x\r\n"},
		{path: "skill/d.txt", data: "lf\n"},
	}
	// What git stores, and so what is written.
	want := map[string]string{
		"a.txt":  "x\ny\n",
		"b.bin":  "x\r\n",
		"c.crlf": "x\n",
		"d.txt":  "lf\n",
	}

	repo := realTempDir(t)
	git(t, repo, "init", "-q")
	writeFiles(t, repo, files[:1])
	commitAll(t, repo)
	// Not committed: the path dependency is normalized with a temporary
	// index.
	writeFiles(t, repo, files[1:])
	pathSrc := fetchOne(t, depSpec{name: "skill", path: filepath.Join(repo, "skill")})

	commitAll(t, repo)
	gitSrc := fetchOne(t, depSpec{name: "skill", git: fileURL(repo), subdir: "skill"})
	if pathSrc.Integrity() != gitSrc.Integrity() {
		t.Errorf("path integrity %s, git integrity %s; want them equal", pathSrc.Integrity(), gitSrc.Integrity())
	}

	for name, s := range map[string]*Source{"path": pathSrc, "git": gitSrc} {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(realTempDir(t), "skill")
			if err := s.WriteTo(dst); err != nil {
				t.Fatal(err)
			}
			for p, data := range want {
				if got := readFile(t, dst, p); got != data {
					t.Errorf("%s = %q, want %q", p, got, data)
				}
			}
		})
	}
}

func TestWriteToRejects(t *testing.T) {
	a := gittree.SHA1
	data := []byte("x\n")
	h := gittree.BlobHash(a, data)
	blobs := map[string][]byte{h: data}
	tests := []struct {
		name  string
		entry gittree.Entry
	}{
		{"parent directory", gittree.Entry{Path: "../evil", Mode: gittree.ModeFile, Hash: h}},
		{"absolute path", gittree.Entry{Path: "/evil", Mode: gittree.ModeFile, Hash: h}},
		{"empty element", gittree.Entry{Path: "a//b", Mode: gittree.ModeFile, Hash: h}},
		{"submodule", gittree.Entry{Path: "vendor", Mode: gittree.ModeGitlink, Hash: h}},
		{"missing contents", gittree.Entry{Path: "f", Mode: gittree.ModeFile, Hash: gittree.BlobHash(a, []byte("other"))}},
	}
	if runtime.GOOS == "windows" {
		tests = append(tests, []struct {
			name  string
			entry gittree.Entry
		}{
			{"device name", gittree.Entry{Path: "con.md", Mode: gittree.ModeFile, Hash: h}},
			{"stream name", gittree.Entry{Path: "a:b", Mode: gittree.ModeFile, Hash: h}},
			{"backslash", gittree.Entry{Path: `..\evil`, Mode: gittree.ModeFile, Hash: h}},
		}...)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Source{Dep: "x", Tree: gittree.Tree{Algorithm: a, Entries: []gittree.Entry{tt.entry}}, blobs: blobs}
			root := realTempDir(t)
			if err := s.WriteTo(filepath.Join(root, "x")); err == nil {
				t.Fatal("WriteTo succeeded")
			}
			if _, err := os.Stat(filepath.Join(root, "evil")); err == nil {
				t.Error("a file was written outside the directory")
			}
		})
	}
}

// caseInsensitive reports whether the file system of dir ignores letter
// case in names.
func caseInsensitive(t *testing.T, dir string) bool {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "CaseProbe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filepath.Join(dir, "CaseProbe"))
	_, err := os.Stat(filepath.Join(dir, "caseprobe"))
	return err == nil
}

// TestWriteToLinkUnderLink writes a link "L" and a link "l/y". On a
// case-insensitive file system "l" is the link "L", and "y" must not be
// created where "L" points to.
func TestWriteToLinkUnderLink(t *testing.T) {
	requireSymlinks(t)
	a := gittree.SHA1
	up, here := []byte("../out"), []byte("z")
	s := &Source{
		Dep: "x",
		Tree: gittree.Tree{Algorithm: a, Entries: []gittree.Entry{
			{Path: "L", Mode: gittree.ModeSymlink, Hash: gittree.BlobHash(a, up)},
			{Path: "l/y", Mode: gittree.ModeSymlink, Hash: gittree.BlobHash(a, here)},
		}},
		blobs: map[string][]byte{gittree.BlobHash(a, up): up, gittree.BlobHash(a, here): here},
	}
	root := realTempDir(t)
	if err := os.Mkdir(filepath.Join(root, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := s.WriteTo(filepath.Join(root, "x"))
	if _, statErr := os.Lstat(filepath.Join(root, "out", "y")); statErr == nil {
		t.Error("a link was created outside the directory")
	}
	if caseInsensitive(t, root) {
		if err == nil {
			t.Error("WriteTo succeeded on a case-insensitive file system")
		}
	} else if err != nil {
		t.Errorf("WriteTo on a case-sensitive file system: %v", err)
	}

	// The same check with the same spelling, which every file system with
	// links exercises.
	s.Tree.Entries[1].Path = "L/y"
	err = s.WriteTo(filepath.Join(root, "x2"))
	wantErr(t, err, "L/y", "symbolic link")
	if _, statErr := os.Lstat(filepath.Join(root, "out", "y")); statErr == nil {
		t.Error("a link was created outside the directory")
	}
}

func TestWindowsName(t *testing.T) {
	tests := []struct {
		elem string
		want bool
	}{
		{"SKILL.md", true},
		{"console.md", true},
		{"com10", true},
		{".hidden", true},
		{"nul", false},
		{"NUL", false},
		{"con.md", false},
		{"Aux.tar.gz", false},
		{"com1", false},
		{"lpt9.txt", false},
		{"nul .txt", false},
		{"name.", false},
		{"name ", false},
		{"a:b", false},
		{`a\b`, false},
	}
	for _, tt := range tests {
		if got := windowsName(tt.elem); got != tt.want {
			t.Errorf("windowsName(%q) = %v, want %v", tt.elem, got, tt.want)
		}
	}
}
