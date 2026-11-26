package gittree

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

var algorithms = []Algorithm{SHA1, SHA256}

func TestBlobHashKnownValues(t *testing.T) {
	tests := []struct {
		a    Algorithm
		data string
		want string
	}{
		{SHA1, "", "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"},
		{SHA256, "", "473a0f4c3be8a93681a267e3b1e9a7dcda1185436fe141f7749120a303721813"},
	}
	for _, tt := range tests {
		if got := BlobHash(tt.a, []byte(tt.data)); got != tt.want {
			t.Errorf("BlobHash(%s, %q) = %s, want %s", tt.a, tt.data, got, tt.want)
		}
	}
}

func TestBlobHashMatchesGit(t *testing.T) {
	contents := []string{
		"",
		"hello\n",
		"crlf\r\nline\r\n",
		"no newline at end",
		"\x00binary\xff\xfe",
		strings.Repeat("long line ", 10000),
	}
	for _, a := range algorithms {
		repo := newRepo(t, a)
		for _, data := range contents {
			name := fmt.Sprintf("%s/%.20q", a, data)
			t.Run(name, func(t *testing.T) {
				want := gitInput(t, repo, nil, data, "hash-object", "--no-filters", "--stdin")
				if got := BlobHash(a, []byte(data)); got != want {
					t.Errorf("BlobHash = %s, git hash-object = %s", got, want)
				}
			})
		}
	}
}

func TestTreeHashEmpty(t *testing.T) {
	want := map[Algorithm]string{
		SHA1:   "4b825dc642cb6eb9a060e54bf8d69288fbee4904",
		SHA256: "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321",
	}
	for _, a := range algorithms {
		got, err := TreeHash(a, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != want[a] {
			t.Errorf("TreeHash(%s, nil) = %s, want %s", a, got, want[a])
		}
	}
}

// writeTreeFromEntries returns the tree that git writes for an index that
// holds the entries.
func writeTreeFromEntries(t *testing.T, repo string, entries []Entry) string {
	t.Helper()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(t.TempDir(), "index")}
	var in strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&in, "%s %s\t%s\n", e.Mode, e.Hash, e.Path)
	}
	gitInput(t, repo, env, in.String(), "update-index", "--add", "--index-info")
	return gitEnv(t, repo, env, "write-tree", "--missing-ok")
}

func TestTreeHashMatchesGit(t *testing.T) {
	// Each entry gets a blob hash made from its path, so that entries
	// differ from each other.
	tests := []struct {
		name  string
		paths map[string]Mode
	}{
		{"single file", map[string]Mode{"a": ModeFile}},
		{"modes", map[string]Mode{
			"file": ModeFile, "exec": ModeExec, "link": ModeSymlink, "sub": ModeGitlink,
		}},
		{"directory order", map[string]Mode{
			// "a" is a directory, sorted as "a/": after "a-b", "a.txt",
			// and before "a0".
			"a/x": ModeFile, "a.txt": ModeFile, "a-b": ModeFile, "a0": ModeFile,
			"a b": ModeFile, "A": ModeFile, "b/a/c": ModeFile, "b/a.c": ModeFile,
		}},
		{"nested", map[string]Mode{
			"skills/one/SKILL.md":        ModeFile,
			"skills/one/scripts/run.sh":  ModeExec,
			"skills/two/SKILL.md":        ModeFile,
			"skills/two/refs/deep/x.txt": ModeFile,
			"README.md":                  ModeFile,
		}},
		{"non-ASCII names", map[string]Mode{
			"日本語/ファイル.md": ModeFile, "é": ModeFile, "e": ModeFile,
		}},
	}
	for _, a := range algorithms {
		repo := newRepo(t, a)
		for _, tt := range tests {
			t.Run(string(a)+"/"+tt.name, func(t *testing.T) {
				var entries []Entry
				for p, m := range tt.paths {
					entries = append(entries, Entry{Path: p, Mode: m, Hash: BlobHash(a, []byte(p))})
				}
				got, err := TreeHash(a, entries)
				if err != nil {
					t.Fatal(err)
				}
				if want := writeTreeFromEntries(t, repo, entries); got != want {
					t.Errorf("TreeHash = %s, git write-tree = %s", got, want)
				}
			})
		}
	}
}

func TestTreeHashErrors(t *testing.T) {
	h := BlobHash(SHA1, nil)
	tests := []struct {
		name    string
		entries []Entry
		want    string
	}{
		{"duplicate", []Entry{{"a", ModeFile, h}, {"a", ModeExec, h}}, "duplicate path"},
		{"file then directory", []Entry{{"a", ModeFile, h}, {"a/b", ModeFile, h}}, "both a file and a directory"},
		{"directory then file", []Entry{{"a/b", ModeFile, h}, {"a", ModeFile, h}}, "both a file and a directory"},
		{"empty path", []Entry{{"", ModeFile, h}}, "invalid path"},
		{"absolute", []Entry{{"/a", ModeFile, h}}, "invalid path"},
		{"dot dot", []Entry{{"../a", ModeFile, h}}, "invalid path"},
		{"dot", []Entry{{"./a", ModeFile, h}}, "invalid path"},
		{"double slash", []Entry{{"a//b", ModeFile, h}}, "invalid path"},
		{"trailing slash", []Entry{{"a/", ModeFile, h}}, "invalid path"},
		{"tree mode", []Entry{{"a", ModeTree, h}}, "unsupported mode"},
		{"short hash", []Entry{{"a", ModeFile, h[:39]}}, "hexadecimal digits"},
		{"upper case hash", []Entry{{"a", ModeFile, strings.ToUpper(h)}}, "invalid hexadecimal"},
		{"SHA-256 hash in SHA-1 tree", []Entry{{"a", ModeFile, BlobHash(SHA256, nil)}}, "hexadecimal digits"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := TreeHash(SHA1, tt.entries)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("TreeHash error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestIntegrity(t *testing.T) {
	sha1Hex := BlobHash(SHA1, nil)
	sha256Hex := BlobHash(SHA256, nil)
	valid := []struct {
		a   Algorithm
		hex string
		s   string
	}{
		{SHA1, sha1Hex, "git-sha1:" + sha1Hex},
		{SHA256, sha256Hex, "git-sha256:" + sha256Hex},
	}
	for _, tt := range valid {
		if got := FormatIntegrity(tt.a, tt.hex); got != tt.s {
			t.Errorf("FormatIntegrity = %q, want %q", got, tt.s)
		}
		if got := (Tree{Algorithm: tt.a, Hash: tt.hex}).Integrity(); got != tt.s {
			t.Errorf("Tree.Integrity = %q, want %q", got, tt.s)
		}
		a, h, err := ParseIntegrity(tt.s)
		if err != nil || a != tt.a || h != tt.hex {
			t.Errorf("ParseIntegrity(%q) = %s, %s, %v", tt.s, a, h, err)
		}
	}

	invalid := []string{
		"",
		sha1Hex,
		"sha1:" + sha1Hex,
		"git-sha1" + sha1Hex,
		"git-md5:" + sha1Hex,
		"git-sha1:" + sha256Hex,
		"git-sha256:" + sha1Hex,
		"git-sha1:" + strings.ToUpper(sha1Hex),
		"git-sha1:" + sha1Hex[:39] + "g",
		"xgit-sha1:" + sha1Hex,
	}
	for _, s := range invalid {
		if a, h, err := ParseIntegrity(s); err == nil {
			t.Errorf("ParseIntegrity(%q) = %s, %s, want an error", s, a, h)
		}
	}
}

func TestParseMode(t *testing.T) {
	tests := []struct {
		s       string
		want    Mode
		wantErr bool
	}{
		{"100644", ModeFile, false},
		{"100755", ModeExec, false},
		{"120000", ModeSymlink, false},
		{"160000", ModeGitlink, false},
		{"040000", ModeTree, false},
		{"40000", ModeTree, false},
		{"100664", 0, true},
		{"abc", 0, true},
		{"", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseMode(tt.s)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseMode(%q) = %v, %v; want %v, error %v", tt.s, got, err, tt.want, tt.wantErr)
		}
	}
	if s := ModeTree.String(); s != "40000" {
		t.Errorf("ModeTree.String() = %q, want 40000", s)
	}
}

func TestParseAlgorithm(t *testing.T) {
	for _, a := range algorithms {
		if got, err := ParseAlgorithm(string(a)); err != nil || got != a {
			t.Errorf("ParseAlgorithm(%q) = %q, %v", a, got, err)
		}
	}
	if _, err := ParseAlgorithm("md5"); err == nil {
		t.Error("ParseAlgorithm(md5) succeeded, want an error")
	}
}

func TestContent(t *testing.T) {
	files := []file{
		{path: "SKILL.md", data: "skill\n"},
		{path: "run.sh", data: "#!/bin/sh\n", exec: true},
		{path: "sub/tool.sh", data: "tool\n", exec: true},
	}
	for _, a := range algorithms {
		t.Run(string(a), func(t *testing.T) {
			var src, want []Entry
			for _, f := range files {
				mode := ModeFile
				if f.exec {
					mode = ModeExec
				}
				h := BlobHash(a, []byte(f.data))
				src = append(src, Entry{Path: f.path, Mode: mode, Hash: h})
				want = append(want, Entry{Path: f.path, Mode: ModeFile, Hash: h})
			}
			link := Entry{Path: "link", Mode: ModeSymlink, Hash: BlobHash(a, []byte("SKILL.md"))}
			src = append(src, link)
			want = append(want, link)
			srcHash, err := TreeHash(a, src)
			if err != nil {
				t.Fatal(err)
			}
			wantHash, err := TreeHash(a, want)
			if err != nil {
				t.Fatal(err)
			}
			tree := Tree{Algorithm: a, Hash: srcHash, Entries: src}

			got, err := tree.Content()
			if err != nil {
				t.Fatal(err)
			}
			if got != FormatIntegrity(a, wantHash) {
				t.Errorf("Content = %s, want %s (symbolic links kept, every file 100644)", got, FormatIntegrity(a, wantHash))
			}
			if got == tree.Integrity() {
				t.Errorf("Content = Integrity = %s, want them to differ when files are executable", got)
			}
			if tree.Entries[1].Mode != ModeExec {
				t.Errorf("Content changed the entries of the tree")
			}

			// Without executable files, the content is the integrity.
			plain := Tree{Algorithm: a, Hash: wantHash, Entries: want}
			if got, err := plain.Content(); err != nil || got != plain.Integrity() {
				t.Errorf("Content = %s, %v; want the integrity %s", got, err, plain.Integrity())
			}
		})
	}
}

// TestContentMatchesDirTree hashes a deployed directory with IgnoreExecBit:
// the result is the content of the source whatever the execute bits on
// disk are (Windows keeps none).
func TestContentMatchesDirTree(t *testing.T) {
	files := []file{
		{path: "SKILL.md", data: "skill\n"},
		{path: "run.sh", data: "#!/bin/sh\n", exec: true},
		{path: "sub/tool.sh", data: "tool\n"},
	}
	for _, a := range algorithms {
		t.Run(string(a), func(t *testing.T) {
			var entries []Entry
			for _, f := range files {
				mode := ModeFile
				if f.exec {
					mode = ModeExec
				}
				entries = append(entries, Entry{Path: f.path, Mode: mode, Hash: BlobHash(a, []byte(f.data))})
			}
			h, err := TreeHash(a, entries)
			if err != nil {
				t.Fatal(err)
			}
			want, err := (Tree{Algorithm: a, Hash: h, Entries: entries}).Content()
			if err != nil {
				t.Fatal(err)
			}
			// The deployed copy has the execute bit on the other files.
			deployed := make([]file, len(files))
			for i, f := range files {
				f.exec = !f.exec
				deployed[i] = f
			}
			dir := realTempDir(t)
			writeFiles(t, dir, deployed)
			got, err := DirTree(a, dir, IgnoreExecBit)
			if err != nil {
				t.Fatal(err)
			}
			if got.Integrity() != want {
				t.Errorf("DirTree with IgnoreExecBit = %s, want the content %s", got.Integrity(), want)
			}
		})
	}
}
