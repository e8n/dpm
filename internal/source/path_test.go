package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e8n/dpm/internal/gittree"
)

// committedTree returns the tree that "git add -A" followed by a commit
// would record for subdir, using the repository's own index.
func committedTree(t *testing.T, repo, subdir string) string {
	t.Helper()
	git(t, repo, "add", "-A")
	return revParse(t, repo, git(t, repo, "write-tree")+":"+subdir)
}

// checkBlobs checks that the contents read for every entry hash to the
// entry's hash.
func checkBlobs(t *testing.T, s *Source) {
	t.Helper()
	for _, e := range s.Tree.Entries {
		data, err := s.blob(e)
		if err != nil {
			t.Fatal(err)
		}
		if got := gittree.BlobHash(s.Tree.Algorithm, data); got != e.Hash {
			t.Errorf("%s: contents hash to %s, want %s", e.Path, got, e.Hash)
		}
	}
}

func TestFetchPathInWorkTree(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, repo string)
	}{
		{"unchanged", func(t *testing.T, repo string) {}},
		{"modified", func(t *testing.T, repo string) {
			writeFiles(t, repo, []file{{path: "skills/a/SKILL.md", data: "changed\n"}})
		}},
		{"untracked", func(t *testing.T, repo string) {
			writeFiles(t, repo, []file{{path: "skills/a/new/file.txt", data: "new\n"}})
		}},
		{"deleted", func(t *testing.T, repo string) {
			if err := os.Remove(filepath.Join(repo, "skills", "a", "notes.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{"ignored file added", func(t *testing.T, repo string) {
			writeFiles(t, repo, []file{{path: "skills/a/debug.log", data: "log\n"}})
		}},
	}
	for _, a := range []gittree.Algorithm{gittree.SHA1, gittree.SHA256} {
		for _, tt := range tests {
			t.Run(string(a)+"/"+tt.name, func(t *testing.T) {
				repo := newRepo(t, a)
				writeFiles(t, repo, []file{
					{path: ".gitignore", data: "*.log\n"},
					{path: "skills/a/SKILL.md", data: "a\n"},
					{path: "skills/a/notes.txt", data: "notes\n"},
					{path: "skills/b/SKILL.md", data: "b\n"},
				})
				commitAll(t, repo)
				headA := revParse(t, repo, "HEAD:skills/a")
				headB := revParse(t, repo, "HEAD:skills/b")
				tt.change(t, repo)

				srcs, err := Fetch(deps(
					depSpec{name: "a", path: filepath.Join(repo, "skills", "a")},
					depSpec{name: "b", path: filepath.Join(repo, "skills", "b")},
				))
				if err != nil {
					t.Fatal(err)
				}
				wantA := committedTree(t, repo, "skills/a")
				if tt.name == "unchanged" || tt.name == "ignored file added" {
					if wantA != headA {
						t.Fatalf("test setup: the tree changed")
					}
				}
				if got := srcs[0].Integrity(); got != gittree.FormatIntegrity(a, wantA) {
					t.Errorf("a: integrity %s, want %s", got, wantA)
				}
				if got := srcs[1].Tree.Hash; got != headB {
					t.Errorf("b: tree %s, want %s", got, headB)
				}
				for _, s := range srcs {
					if len(s.Warnings) != 0 || s.Commit != "" {
						t.Errorf("%s: warnings %q, commit %q; want none", s.Dep, s.Warnings, s.Commit)
					}
					checkBlobs(t, s)
				}
			})
		}
	}
}

func TestFetchPathTopLevelAndLink(t *testing.T) {
	repo := newRepo(t, gittree.SHA1)
	writeFiles(t, repo, []file{{path: "SKILL.md", data: "top\n"}})
	commitAll(t, repo)

	s := fetchOne(t, depSpec{name: "top", path: repo})
	if got, want := s.Tree.Hash, revParse(t, repo, "HEAD:"); got != want {
		t.Errorf("tree %s, want %s", got, want)
	}

	if !canSymlink(t) {
		t.Skip("symbolic links cannot be created here")
	}
	link := filepath.Join(realTempDir(t), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	s = fetchOne(t, depSpec{name: "top", path: link})
	if got, want := s.Tree.Hash, revParse(t, repo, "HEAD:"); got != want {
		t.Errorf("through a link: tree %s, want %s", got, want)
	}
}

func TestFetchPathOutsideGit(t *testing.T) {
	dir := realTempDir(t)
	writeFiles(t, dir, []file{
		{path: "SKILL.md", data: "plain\r\n"},
		{path: "bin/run.sh", data: "#!/bin/sh\n", exec: true},
	})
	if canSymlink(t) {
		writeFiles(t, dir, []file{{path: "link", link: "SKILL.md"}})
	}
	s := fetchOne(t, depSpec{name: "plain", path: dir})
	want, err := gittree.DirTree(gittree.SHA1, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Integrity() != want.Integrity() {
		t.Errorf("integrity %s, want %s", s.Integrity(), want.Integrity())
	}
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], dir) || !strings.Contains(s.Warnings[0], "not in a Git work tree") {
		t.Errorf("warnings %q, want one about %s", s.Warnings, dir)
	}
	checkBlobs(t, s)
}

func TestFetchPathErrors(t *testing.T) {
	repo := newRepo(t, gittree.SHA1)
	writeFiles(t, repo, []file{
		{path: ".gitignore", data: "*.log\nignored/\n"},
		{path: "skills/a/SKILL.md", data: "a\n"},
		{path: "skills/logs/x.log", data: "log\n"},
		{path: "ignored/SKILL.md", data: "i\n"},
		{path: "skills/nested/SKILL.md", data: "n\n"},
	})
	commitAll(t, repo)
	// An untracked repository inside the dependency becomes a gitlink.
	inner := filepath.Join(repo, "skills", "nested", "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, inner, "init", "-q")
	writeFiles(t, inner, []file{{path: "f", data: "f\n"}})
	commitAll(t, inner)
	if err := os.MkdirAll(filepath.Join(repo, "skills", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	emptyPlain := realTempDir(t)
	// A repository whose work tree is not where its .git is.
	elsewhere := newRepo(t, gittree.SHA1)
	writeFiles(t, elsewhere, []file{{path: "skills/w/SKILL.md", data: "w\n"}})
	git(t, elsewhere, "config", "core.worktree", realTempDir(t))
	// A repository inside a directory that is not in a work tree.
	plainWithRepo := realTempDir(t)
	writeFiles(t, plainWithRepo, []file{{path: "SKILL.md", data: "p\n"}})
	git(t, filepath.Join(plainWithRepo), "init", "-q", "inner")

	tests := []struct {
		name string
		path string
		want []string
	}{
		{"missing", filepath.Join(repo, "skills", "none"), []string{`"x"`, "path"}},
		{"file", filepath.Join(repo, "skills", "a", "SKILL.md"), []string{`"x"`, "not a directory"}},
		{"only ignored files", filepath.Join(repo, "skills", "logs"), []string{`"x"`, "no files"}},
		{"ignored directory", filepath.Join(repo, "ignored"), []string{`"x"`, "no files"}},
		{"empty directory in a work tree", filepath.Join(repo, "skills", "empty"), []string{`"x"`, "no files"}},
		{"empty directory outside Git", emptyPlain, []string{`"x"`, "no files"}},
		{"nested repository", filepath.Join(repo, "skills", "nested"), []string{`"x"`, "inner", "submodule"}},
		{"core.worktree elsewhere", filepath.Join(elsewhere, "skills", "w"), []string{`"x"`, "top level directory"}},
		{"repository outside Git", plainWithRepo, []string{`"x"`, "inner/.git/", "Git repository"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Fetch(deps(depSpec{name: "x", path: tt.path}))
			wantErr(t, err, tt.want...)
		})
	}
}

// TestFetchPathLetterCase renames a committed directory on disk to a
// different letter case (outside git). On a case-insensitive file system
// the directory then has the spelling on disk, which git does not record;
// with or without changes it must not give a tree that is silently wrong.
func TestFetchPathLetterCase(t *testing.T) {
	repo := newRepo(t, gittree.SHA1)
	if !caseInsensitive(t, repo) {
		t.Skip("the file system is case-sensitive")
	}
	git(t, repo, "config", "core.ignorecase", "true")
	writeFiles(t, repo, []file{{path: "skills/a/SKILL.md", data: "a\n"}})
	commitAll(t, repo)
	for _, step := range [][2]string{{"skills", "tmp"}, {"tmp", "Skills"}} {
		if err := os.Rename(filepath.Join(repo, step[0]), filepath.Join(repo, step[1])); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(repo, "Skills", "a")
	head := revParse(t, repo, "HEAD:skills/a")

	check := func(name string) {
		s, err := Fetch(deps(depSpec{name: "a", path: dir}))
		if err != nil {
			t.Logf("%s: error: %v", name, err)
			return
		}
		want := committedTree(t, repo, "skills/a")
		git(t, repo, "reset", "-q")
		t.Logf("%s: tree %s, head %s, committed %s", name, s[0].Tree.Hash, head, want)
		if s[0].Tree.Hash != want {
			t.Errorf("%s: tree %s, want %s or an error", name, s[0].Tree.Hash, want)
		}
	}
	check("unchanged")
	writeFiles(t, repo, []file{{path: "Skills/a/new.txt", data: "new\n"}})
	check("untracked file")
}

// TestFetchPathLinkedWorkTree fetches a directory of a work tree added with
// "git worktree add", whose .git is a file.
func TestFetchPathLinkedWorkTree(t *testing.T) {
	repo := newRepo(t, gittree.SHA1)
	writeFiles(t, repo, []file{{path: "skills/a/SKILL.md", data: "a\n"}})
	commitAll(t, repo)
	wt := filepath.Join(realTempDir(t), "wt")
	git(t, repo, "worktree", "add", "-q", "--detach", wt)
	writeFiles(t, wt, []file{{path: "skills/a/new.txt", data: "new\n"}})

	s := fetchOne(t, depSpec{name: "a", path: filepath.Join(wt, "skills", "a")})
	if want := committedTree(t, wt, "skills/a"); s.Tree.Hash != want {
		t.Errorf("tree %s, want %s", s.Tree.Hash, want)
	}
	if s.Tree.Hash == revParse(t, repo, "HEAD:skills/a") {
		t.Error("the untracked file in the linked work tree was not included")
	}
	checkBlobs(t, s)
}
