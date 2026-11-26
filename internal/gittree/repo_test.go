package gittree

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commitAll records every file of the work tree in a new commit.
func commitAll(t *testing.T, repo string) {
	t.Helper()
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "commit")
}

// readIndex returns the bytes of the repository's index (nil when there is
// none).
func readIndex(t *testing.T, repo string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return data
}

// committedTrees returns, for each subdir, the tree that "git add -A"
// followed by a commit would record, using the repository's own index. It
// changes the index, so call it after the checks that need the index.
func committedTrees(t *testing.T, repo string, a Algorithm, subdirs []string) []string {
	t.Helper()
	git(t, repo, "add", "-A")
	root := git(t, repo, "write-tree")
	empty, err := TreeHash(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range subdirs {
		h, err := Git{Dir: repo}.Run("rev-parse", "--verify", "-q", root+":"+s)
		if err != nil {
			out = append(out, empty)
			continue
		}
		out = append(out, strings.TrimSpace(string(h)))
	}
	return out
}

// mkdir creates the directory p (a slash path) under dir.
func mkdir(t *testing.T, dir, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(p)), 0o755); err != nil {
		t.Fatal(err)
	}
}

// entryOf returns the entry with the given path.
func entryOf(t *testing.T, tree Tree, p string) Entry {
	t.Helper()
	for _, e := range tree.Entries {
		if e.Path == p {
			return e
		}
	}
	t.Fatalf("no entry %q in %v", p, tree.Entries)
	return Entry{}
}

// wantBlob checks that the entry p of tree holds data.
func wantBlob(t *testing.T, tree Tree, p, data string) {
	t.Helper()
	if got, want := entryOf(t, tree, p).Hash, BlobHash(tree.Algorithm, []byte(data)); got != want {
		t.Errorf("%s: blob %s, want %s (%q)", p, got, want, data)
	}
}

func TestWorkTree(t *testing.T) {
	base := []file{
		{path: "README.md", data: "readme\n"},
		{path: "skills/one/SKILL.md", data: "one\n"},
		{path: "skills/one/refs/a.txt", data: "a\n"},
		{path: "skills/two/SKILL.md", data: "two\n"},
		{path: ".gitignore", data: "*.log\n"},
	}
	tests := []struct {
		name    string
		algos   []Algorithm
		require func(*testing.T)
		// config is set in the repository before anything is written.
		config [][2]string
		// commit is written and committed; change is written afterwards.
		commit []file
		change []file
		// prepare runs after change.
		prepare func(t *testing.T, repo string)
		subdirs []string
		// check runs on the result in addition to the comparison with
		// what git would commit.
		check func(t *testing.T, trees []Tree)
		// wantHead reports that the trees must equal those of HEAD.
		wantHead bool
	}{
		{
			name:     "clean",
			commit:   base,
			subdirs:  []string{"skills/one", "", "skills/two"},
			wantHead: true,
		},
		{
			name:     "changes outside the directories",
			commit:   base,
			change:   []file{{path: "README.md", data: "changed\n"}, {path: "new.txt", data: "new\n"}},
			subdirs:  []string{"skills/one"},
			wantHead: true,
		},
		{
			name:    "modified file",
			commit:  base,
			change:  []file{{path: "skills/one/SKILL.md", data: "changed\n"}},
			subdirs: []string{"skills/one", "skills/two"},
			check: func(t *testing.T, trees []Tree) {
				wantBlob(t, trees[0], "SKILL.md", "changed\n")
			},
		},
		{
			name:    "untracked file",
			commit:  base,
			change:  []file{{path: "skills/one/new/file.txt", data: "new\n"}},
			subdirs: []string{"skills/one"},
			check: func(t *testing.T, trees []Tree) {
				wantBlob(t, trees[0], "new/file.txt", "new\n")
			},
		},
		{
			name:     "ignored file",
			commit:   base,
			change:   []file{{path: "skills/one/debug.log", data: "log\n"}},
			subdirs:  []string{"skills/one"},
			wantHead: true,
			check: func(t *testing.T, trees []Tree) {
				for _, e := range trees[0].Entries {
					if e.Path == "debug.log" {
						t.Error("ignored file debug.log is in the tree")
					}
				}
			},
		},
		{
			name:   "deleted file",
			commit: base,
			prepare: func(t *testing.T, repo string) {
				if err := os.Remove(filepath.Join(repo, "skills", "one", "refs", "a.txt")); err != nil {
					t.Fatal(err)
				}
			},
			subdirs: []string{"skills/one"},
			check: func(t *testing.T, trees []Tree) {
				if len(trees[0].Entries) != 1 {
					t.Errorf("entries = %v, want only SKILL.md", trees[0].Entries)
				}
			},
		},
		{
			name:   "staged change",
			commit: base,
			change: []file{{path: "skills/two/SKILL.md", data: "staged\n"}},
			prepare: func(t *testing.T, repo string) {
				git(t, repo, "add", "skills/two/SKILL.md")
			},
			subdirs: []string{"skills/two"},
			check: func(t *testing.T, trees []Tree) {
				wantBlob(t, trees[0], "SKILL.md", "staged\n")
			},
		},
		{
			name:    "overlapping directories",
			commit:  base,
			change:  []file{{path: "skills/two/x.txt", data: "x\n"}},
			subdirs: []string{"skills", "skills/two", "skills/one"},
		},
		{
			name:   "unborn HEAD",
			change: base,
			prepare: func(t *testing.T, repo string) {
				if err := os.MkdirAll(filepath.Join(repo, "emptydir"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			subdirs: []string{"", "skills/one", "emptydir"},
		},
		{
			name:   "directory without recorded files",
			commit: base,
			prepare: func(t *testing.T, repo string) {
				writeFiles(t, repo, []file{{path: "logs/only.log", data: "x\n"}})
				if err := os.MkdirAll(filepath.Join(repo, "empty", "nested"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			subdirs:  []string{"logs", "empty", "skills/one"},
			wantHead: false,
			check: func(t *testing.T, trees []Tree) {
				empty, _ := TreeHash(trees[0].Algorithm, nil)
				for i := 0; i < 2; i++ {
					if trees[i].Hash != empty || len(trees[i].Entries) != 0 {
						t.Errorf("tree %d = %+v, want the empty tree", i, trees[i])
					}
				}
			},
		},
		{
			name:   "ignored directories with changes elsewhere",
			commit: base,
			prepare: func(t *testing.T, repo string) {
				writeFiles(t, repo, []file{
					{path: ".gitignore", data: "*.log\nign/\ntracked/\n"},
					{path: "tracked/a.txt", data: "a\n"},
				})
				git(t, repo, "add", "-f", ".gitignore", "tracked/a.txt")
				git(t, repo, "commit", "-q", "-m", "ignored")
				writeFiles(t, repo, []file{
					{path: "ign/sub/x.txt", data: "x\n"},
					{path: "tracked/a.txt", data: "changed\n"},
					{path: "tracked/new.txt", data: "ignored\n"},
					{path: "skills/one/SKILL.md", data: "changed\n"},
				})
			},
			subdirs: []string{"ign", "ign/sub", "tracked", "skills/one"},
			check: func(t *testing.T, trees []Tree) {
				empty, _ := TreeHash(trees[0].Algorithm, nil)
				if trees[0].Hash != empty || trees[1].Hash != empty {
					t.Errorf("ignored directories = %s, %s, want the empty tree", trees[0].Hash, trees[1].Hash)
				}
				if len(trees[2].Entries) != 1 {
					t.Errorf("tracked entries = %v, want only a.txt", trees[2].Entries)
				}
				wantBlob(t, trees[2], "a.txt", "changed\n")
			},
		},
		{
			name:   "ignored directories without other changes",
			commit: base,
			prepare: func(t *testing.T, repo string) {
				writeFiles(t, repo, []file{
					{path: ".gitignore", data: "*.log\nign/\n"},
					{path: "ign/x.txt", data: "x\n"},
				})
				commitAll(t, repo)
			},
			subdirs: []string{"ign", "skills/one"},
			check: func(t *testing.T, trees []Tree) {
				empty, _ := TreeHash(trees[0].Algorithm, nil)
				if trees[0].Hash != empty {
					t.Errorf("ignored directory = %s, want the empty tree", trees[0].Hash)
				}
			},
		},
		{
			name:    "names with spaces",
			commit:  append(append([]file(nil), base...), file{path: "my dir/a b.txt", data: "a\n"}),
			change:  []file{{path: "my dir/c d.txt", data: "c\n"}},
			prepare: func(t *testing.T, repo string) { mkdir(t, repo, "empty dir") },
			subdirs: []string{"my dir", "empty dir"},
		},
		{
			name:     "glob characters are literal",
			commit:   append(append([]file(nil), base...), file{path: "[ab]/x.txt", data: "x\n"}, file{path: "a/y.txt", data: "y\n"}),
			change:   []file{{path: "a/y.txt", data: "changed\n"}},
			subdirs:  []string{"[ab]"},
			wantHead: true,
		},
		{
			name:   "core.autocrlf=true, CRLF in the work tree of an LF commit",
			config: [][2]string{{"core.autocrlf", "true"}},
			commit: base,
			change: []file{
				{path: "skills/one/SKILL.md", data: "one\r\n"},
				{path: "skills/one/refs/a.txt", data: "a\r\n"},
			},
			subdirs:  []string{"skills/one"},
			wantHead: true,
		},
		{
			name:   "core.autocrlf=true, changed file with CRLF",
			config: [][2]string{{"core.autocrlf", "true"}},
			commit: base,
			change: []file{
				{path: "skills/one/SKILL.md", data: "changed\r\nline\r\n"},
				{path: "skills/one/new.txt", data: "new\r\n"},
			},
			subdirs: []string{"skills/one"},
			check: func(t *testing.T, trees []Tree) {
				wantBlob(t, trees[0], "SKILL.md", "changed\nline\n")
				wantBlob(t, trees[0], "new.txt", "new\n")
			},
		},
		{
			name: ".gitattributes eol and -text",
			commit: append(append([]file(nil), base...),
				file{path: ".gitattributes", data: "*.txt eol=crlf\n*.bin -text\n*.md text\n"}),
			change: []file{
				{path: "skills/one/crlf.txt", data: "a\r\nb\r\n"},
				{path: "skills/one/raw.bin", data: "a\r\nb\r\n"},
				{path: "skills/one/SKILL.md", data: "md\r\n"},
			},
			subdirs: []string{"skills/one"},
			check: func(t *testing.T, trees []Tree) {
				wantBlob(t, trees[0], "crlf.txt", "a\nb\n")
				wantBlob(t, trees[0], "raw.bin", "a\r\nb\r\n")
				wantBlob(t, trees[0], "SKILL.md", "md\n")
			},
		},
		{
			name: ".gitattributes text=auto",
			commit: append(append([]file(nil), base...),
				file{path: ".gitattributes", data: "* text=auto\n"}),
			change: []file{
				{path: "skills/one/auto.txt", data: "a\r\nb\r\n"},
				{path: "skills/one/binary.dat", data: "\x00a\r\n"},
			},
			subdirs: []string{"skills/one"},
			check: func(t *testing.T, trees []Tree) {
				wantBlob(t, trees[0], "auto.txt", "a\nb\n")
				wantBlob(t, trees[0], "binary.dat", "\x00a\r\n")
			},
		},
		{
			name:    "execute bit",
			require: requireExecBit,
			commit:  append(append([]file(nil), base...), file{path: "skills/one/run.sh", data: "#!/bin/sh\n", exec: true}),
			change:  []file{{path: "skills/one/SKILL.md", data: "one\n", exec: true}},
			subdirs: []string{"skills/one"},
			check: func(t *testing.T, trees []Tree) {
				for p, m := range map[string]Mode{"run.sh": ModeExec, "SKILL.md": ModeExec, "refs/a.txt": ModeFile} {
					if got := entryOf(t, trees[0], p).Mode; got != m {
						t.Errorf("%s: mode %s, want %s", p, got, m)
					}
				}
			},
		},
		{
			// As on Windows: the mode of a changed file comes from HEAD.
			name:   "execute bit from HEAD with core.filemode=false",
			config: [][2]string{{"core.filemode", "false"}},
			commit: base,
			prepare: func(t *testing.T, repo string) {
				git(t, repo, "update-index", "--chmod=+x", "skills/one/SKILL.md")
				git(t, repo, "commit", "-q", "-m", "chmod")
				writeFiles(t, repo, []file{{path: "skills/one/SKILL.md", data: "changed\n"}})
			},
			subdirs: []string{"skills/one"},
			check: func(t *testing.T, trees []Tree) {
				if got := entryOf(t, trees[0], "SKILL.md").Mode; got != ModeExec {
					t.Errorf("SKILL.md: mode %s, want %s", got, ModeExec)
				}
				wantBlob(t, trees[0], "SKILL.md", "changed\n")
			},
		},
		{
			name:    "symbolic links",
			require: requireSymlinks,
			commit:  append(append([]file(nil), base...), file{path: "skills/one/committed", link: "SKILL.md"}),
			change:  []file{{path: "skills/one/new-link", link: "refs/a.txt"}},
			subdirs: []string{"skills/one"},
			check: func(t *testing.T, trees []Tree) {
				for p, target := range map[string]string{"committed": "SKILL.md", "new-link": "refs/a.txt"} {
					e := entryOf(t, trees[0], p)
					if e.Mode != ModeSymlink {
						t.Errorf("%s: mode %s, want %s", p, e.Mode, ModeSymlink)
					}
					wantBlob(t, trees[0], p, target)
				}
			},
		},
	}

	for _, tt := range tests {
		algos := tt.algos
		if algos == nil {
			algos = algorithms
		}
		for _, a := range algos {
			t.Run(string(a)+"/"+tt.name, func(t *testing.T) {
				if tt.require != nil {
					tt.require(t)
				}
				repo := newRepo(t, a)
				for _, kv := range tt.config {
					git(t, repo, "config", kv[0], kv[1])
				}
				if tt.commit != nil {
					writeFiles(t, repo, tt.commit)
					commitAll(t, repo)
				}
				writeFiles(t, repo, tt.change)
				if tt.prepare != nil {
					tt.prepare(t, repo)
				}
				indexBefore := readIndex(t, repo)

				trees, err := WorkTree(repo, tt.subdirs)
				if err != nil {
					t.Fatal(err)
				}

				if !bytes.Equal(readIndex(t, repo), indexBefore) {
					t.Error("the repository's index was changed")
				}
				if len(trees) != len(tt.subdirs) {
					t.Fatalf("got %d trees, want %d", len(trees), len(tt.subdirs))
				}
				for i, tree := range trees {
					if tree.Algorithm != a {
						t.Errorf("%q: algorithm %s, want %s", tt.subdirs[i], tree.Algorithm, a)
					}
					// The entries describe the same tree.
					h, err := TreeHash(a, tree.Entries)
					if err != nil {
						t.Fatalf("%q: %v", tt.subdirs[i], err)
					}
					if h != tree.Hash {
						t.Errorf("%q: tree %s, but its entries give %s", tt.subdirs[i], tree.Hash, h)
					}
				}
				if tt.wantHead {
					for i, s := range tt.subdirs {
						if want := git(t, repo, "rev-parse", "HEAD:"+s); trees[i].Hash != want {
							t.Errorf("%q: tree %s, want HEAD:%s = %s", s, trees[i].Hash, s, want)
						}
					}
				}
				if tt.check != nil {
					tt.check(t, trees)
				}
				want := committedTrees(t, repo, a, tt.subdirs)
				for i, s := range tt.subdirs {
					if trees[i].Hash != want[i] {
						t.Errorf("%q: tree %s, want %s (what git add -A would commit)", s, trees[i].Hash, want[i])
					}
				}
			})
		}
	}
}

func TestWorkTreeEntries(t *testing.T) {
	repo := newRepo(t, SHA1)
	writeFiles(t, repo, []file{
		{path: "skill/b.txt", data: "b\n"},
		{path: "skill/a/c.txt", data: "c\n"},
		{path: "skill/a.txt", data: "a\n"},
		{path: "other.txt", data: "o\n"},
	})
	commitAll(t, repo)
	trees, err := WorkTree(repo, []string{"skill"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{Path: "a.txt", Mode: ModeFile, Hash: BlobHash(SHA1, []byte("a\n"))},
		{Path: "a/c.txt", Mode: ModeFile, Hash: BlobHash(SHA1, []byte("c\n"))},
		{Path: "b.txt", Mode: ModeFile, Hash: BlobHash(SHA1, []byte("b\n"))},
	}
	if len(trees[0].Entries) != len(want) {
		t.Fatalf("entries = %v, want %v", trees[0].Entries, want)
	}
	for i := range want {
		if trees[0].Entries[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, trees[0].Entries[i], want[i])
		}
	}
}

func TestWorkTreeErrors(t *testing.T) {
	repo := newRepo(t, SHA1)
	writeFiles(t, repo, []file{{path: "dir/file.txt", data: "x\n"}})
	commitAll(t, repo)
	notRepo := realTempDir(t)
	if err := os.MkdirAll(filepath.Join(notRepo, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		top     string
		subdirs []string
		want    string
	}{
		{"missing directory", repo, []string{"missing"}, ""},
		{"file", repo, []string{"dir/file.txt"}, "not a directory"},
		{"parent", repo, []string{"../x"}, "invalid directory"},
		{"absolute", repo, []string{"/dir"}, "invalid directory"},
		{"unclean", repo, []string{"dir/"}, "invalid directory"},
		{"not a repository", notRepo, []string{"dir"}, "git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := WorkTree(tt.top, tt.subdirs)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("WorkTree error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
	if trees, err := WorkTree(repo, nil); err != nil || trees != nil {
		t.Errorf("WorkTree with no directories = %v, %v", trees, err)
	}
}

func TestReadTrees(t *testing.T) {
	for _, a := range algorithms {
		t.Run(string(a), func(t *testing.T) {
			repo := newRepo(t, a)
			writeFiles(t, repo, []file{
				{path: "x/a.txt", data: "a\n"},
				{path: "y/b.txt", data: "b\n"},
				{path: "file.txt", data: "f\n"},
			})
			commitAll(t, repo)
			commit := git(t, repo, "rev-parse", "HEAD")
			g := Git{Dir: repo}

			trees, err := ReadTrees(g, a, commit, []string{"y", "", "x"})
			if err != nil {
				t.Fatal(err)
			}
			for i, s := range []string{"y", "", "x"} {
				if want := git(t, repo, "rev-parse", commit+":"+s); trees[i].Hash != want {
					t.Errorf("%q: %s, want %s", s, trees[i].Hash, want)
				}
			}
			if len(trees[1].Entries) != 3 {
				t.Errorf("root entries = %v, want 3", trees[1].Entries)
			}
			wantBlob(t, trees[0], "b.txt", "b\n")

			for _, missing := range []string{"missing", "my dir", "a b c"} {
				if _, err := ReadTrees(g, a, commit, []string{"x", missing}); !errors.Is(err, ErrNotFound) {
					t.Errorf("missing directory %q: error = %v, want ErrNotFound", missing, err)
				}
			}
			if _, err := ReadTrees(g, a, commit, []string{"file.txt"}); err == nil || !strings.Contains(err.Error(), "not a directory") {
				t.Errorf("file: error = %v, want not a directory", err)
			}
		})
	}
}

func TestGitError(t *testing.T) {
	dir := realTempDir(t)
	_, err := Git{Dir: dir}.Run("rev-parse", "--verify", "no-such-ref")
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want *Error", err)
	}
	if e.ExitCode == 0 || e.ExitCode == -1 || e.Stderr == "" {
		t.Errorf("Error = %+v, want an exit code and standard error", e)
	}
	if !strings.Contains(err.Error(), "rev-parse --verify no-such-ref") {
		t.Errorf("message %q does not show the command", err.Error())
	}
}

func TestGitIgnoresInheritedEnvironment(t *testing.T) {
	repo := newRepo(t, SHA1)
	writeFiles(t, repo, []file{{path: "dir/a.txt", data: "a\n"}})
	commitAll(t, repo)
	writeFiles(t, repo, []file{{path: "dir/b.txt", data: "b\n"}})
	other := newRepo(t, SHA256)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, "index"))
	t.Setenv("GIT_GLOB_PATHSPECS", "1")
	t.Setenv("GIT_ICASE_PATHSPECS", "1")

	out, err := Git{Dir: repo}.Run("rev-parse", "--show-object-format")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "sha1" {
		t.Errorf("object format = %s, want sha1 (GIT_DIR must be ignored)", got)
	}
	trees, err := WorkTree(repo, []string{"dir"})
	if err != nil {
		t.Fatal(err)
	}
	if len(trees[0].Entries) != 2 {
		t.Errorf("entries = %v, want a.txt and b.txt", trees[0].Entries)
	}
}
