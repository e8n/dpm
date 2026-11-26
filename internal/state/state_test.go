package state

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	sha1Integrity   = "git-sha1:0123456789abcdef0123456789abcdef01234567"
	sha1Content     = "git-sha1:89abcdef0123456789abcdef0123456789abcdef"
	sha256Content   = "git-sha256:89abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567"
	sha256Integrity = "git-sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func writeState(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(File(dir), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestLoadMissing(t *testing.T) {
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Placed) != 0 {
		t.Errorf("Placed = %v, want none", s.Placed)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	s := &State{Placed: []Placed{
		{Dep: "b", Dir: ".claude/skills/b", Integrity: sha1Integrity, Content: sha1Content},
		{Dep: "a", Dir: "../.claude/skills/a", Integrity: sha256Integrity, Content: sha256Content},
		// Written before content was added.
		{Dep: "a", Dir: ".agents/skills/a", Integrity: sha1Integrity},
	}}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	want := `# Written by dpm. Do not edit.
version = 1

[[placed]]
dep = "a"
dir = "../.claude/skills/a"
integrity = "` + sha256Integrity + `"
content = "` + sha256Content + `"

[[placed]]
dep = "a"
dir = ".agents/skills/a"
integrity = "` + sha1Integrity + `"

[[placed]]
dep = "b"
dir = ".claude/skills/b"
integrity = "` + sha1Integrity + `"
content = "` + sha1Content + `"
`
	if got := readFile(t, File(dir)); got != want {
		t.Errorf("state.toml =\n%s\nwant\n%s", got, want)
	}
	if got := readFile(t, filepath.Join(dir, DirName, ".gitignore")); got != "*\n" {
		t.Errorf(".gitignore = %q", got)
	}
	entries, err := os.ReadDir(filepath.Join(dir, DirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf(".dpm has %d entries, want .gitignore and state.toml only (no temporary file)", len(entries))
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Placed) != 3 || loaded.Placed[0].Dir != "../.claude/skills/a" || loaded.Placed[0].Integrity != sha256Integrity ||
		loaded.Placed[0].Content != sha256Content || loaded.Placed[1].Content != "" || loaded.Placed[2].Content != sha1Content {
		t.Errorf("Load = %+v", loaded.Placed)
	}

	// Saving again, in another order, gives the same bytes.
	loaded.Placed[0], loaded.Placed[2] = loaded.Placed[2], loaded.Placed[0]
	if err := Save(dir, loaded); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, File(dir)); got != want {
		t.Errorf("state.toml after the second save =\n%s", got)
	}
}

func TestSaveKeepsGitignore(t *testing.T) {
	dir := t.TempDir()
	writeState(t, dir, "version = 1\n")
	p := filepath.Join(dir, DirName, ".gitignore")
	if err := os.WriteFile(p, []byte("*\n!keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, &State{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, p); got != "*\n!keep\n" {
		t.Errorf(".gitignore = %q, want it unchanged", got)
	}
	if got := readFile(t, File(dir)); got != header+"version = 1\n" {
		t.Errorf("state.toml = %q", got)
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	err := Save(dir, &State{Placed: []Placed{{Dep: "a", Dir: "x/b", Integrity: sha1Integrity}}})
	if err == nil {
		t.Fatal("Save accepted an entry whose dir does not end with the dependency name")
	}
	if _, err := os.Stat(filepath.Join(dir, DirName)); err == nil {
		t.Error(".dpm was created for an invalid state")
	}
}

func TestLoadErrors(t *testing.T) {
	entry := func(dep, dir, integrity string) string {
		return "[[placed]]\ndep = \"" + dep + "\"\ndir = \"" + dir + "\"\nintegrity = \"" + integrity + "\"\n"
	}
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"syntax error", "version = \n", "state.toml"},
		{"missing version", entry("a", "x/a", sha1Integrity), "version is missing"},
		{"newer version", "version = 2\n", "unsupported version 2"},
		{"unknown key", "version = 1\nowner = \"x\"\n", "unknown keys: owner"},
		{"unknown key in an entry", "version = 1\n" + entry("a", "x/a", sha1Integrity) + "date = 1\n", "unknown keys: placed.date"},
		{"empty dep", "version = 1\n" + entry("", "x/a", sha1Integrity), "invalid dep"},
		{"dep with a separator", "version = 1\n" + entry("x/a", "x/a", sha1Integrity), "invalid dep"},
		{"empty dir", "version = 1\n" + entry("a", "", sha1Integrity), "invalid dir"},
		{"dir with a backslash", "version = 1\n" + entry("a", `x\\\\a`, sha1Integrity), "invalid dir"},
		{"dir not clean", "version = 1\n" + entry("a", "x/../y/a", sha1Integrity), "invalid dir"},
		{"dir is the manifest's directory", "version = 1\n" + entry("a", ".", sha1Integrity), "does not end with"},
		{"dir not named after the dependency", "version = 1\n" + entry("a", "x/b", sha1Integrity), "does not end with"},
		{"invalid integrity", "version = 1\n" + entry("a", "x/a", "sha1:00"), "invalid integrity"},
		{"invalid content", "version = 1\n" + entry("a", "x/a", sha1Integrity) + "content = \"git-sha1:00\"\n", "content: invalid integrity"},
		{"content in another algorithm", "version = 1\n" + entry("a", "x/a", sha1Integrity) + "content = \"" + sha256Content + "\"\n", "does not use the algorithm"},
		{"dir with three leading slashes", "version = 1\n" + entry("a", "///server/share/a", sha1Integrity), "invalid dir"},
		{"UNC dir not clean", "version = 1\n" + entry("a", "//server/x/../a", sha1Integrity), "invalid dir"},
		{"recorded twice", "version = 1\n" + entry("a", "x/a", sha1Integrity) + entry("a", "x/a", sha1Integrity), "recorded twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeState(t, dir, tt.content)
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one containing %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), File(dir)) {
				t.Errorf("err = %v, want the file name", err)
			}
		})
	}
}

func TestRelDirAndPath(t *testing.T) {
	m := t.TempDir()
	tests := []struct {
		dir  string
		want string
	}{
		{filepath.Join(m, ".claude", "skills", "a"), ".claude/skills/a"},
		{filepath.Join(filepath.Dir(m), ".claude", "skills", "a"), "../.claude/skills/a"},
	}
	for _, tt := range tests {
		got := RelDir(m, tt.dir)
		if got != tt.want {
			t.Errorf("RelDir(%s) = %q, want %q", tt.dir, got, tt.want)
		}
		if p := (Placed{Dir: got}).Path(m); p != tt.dir {
			t.Errorf("Path(%q) = %s, want %s", got, p, tt.dir)
		}
	}
}

func TestRelDirOtherDrive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows has paths without a relative path between them")
	}
	m := `C:\work\project`
	dir := `D:\skills\a`
	got := RelDir(m, dir)
	if got != "D:/skills/a" {
		t.Fatalf("RelDir = %q, want the absolute slash path", got)
	}
	if p := (Placed{Dir: got}).Path(m); p != dir {
		t.Errorf("Path = %s, want %s", p, dir)
	}
}

// TestLoadUNCDir reads a record of a UNC destination written on Windows.
// The leading "//" is part of the path on every OS.
func TestLoadUNCDir(t *testing.T) {
	dir := t.TempDir()
	writeState(t, dir, "version = 1\n[[placed]]\ndep = \"a\"\ndir = \"//server/share/skills/a\"\nintegrity = \""+sha1Integrity+"\"\n")
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Placed) != 1 || s.Placed[0].Dir != "//server/share/skills/a" {
		t.Fatalf("Load = %+v", s.Placed)
	}
	want := filepath.FromSlash("//server/share/skills/a")
	if p := s.Placed[0].Path(dir); p != want {
		t.Errorf("Path = %s, want %s", p, want)
	}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, File(dir)); !strings.Contains(got, `dir = "//server/share/skills/a"`) {
		t.Errorf("state.toml =\n%s\nwant the UNC dir kept", got)
	}
}

// TestRelDirUNC records a UNC destination, which has no path relative to a
// manifest on a drive, with its leading "//" so that Path gives it back
// and the next install matches it with the record.
func TestRelDirUNC(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows has UNC paths")
	}
	m := `C:\work\project`
	tests := []struct {
		dir  string
		want string
	}{
		{`\\server\share\skills\a`, "//server/share/skills/a"},
		{`\\server\share\x\..\skills\a`, "//server/share/skills/a"},
	}
	for _, tt := range tests {
		got := RelDir(m, tt.dir)
		if got != tt.want {
			t.Errorf("RelDir(%s) = %q, want %q", tt.dir, got, tt.want)
		}
		if err := validate(Placed{Dep: "a", Dir: got, Integrity: sha1Integrity}); err != nil {
			t.Errorf("validate(%q): %v", got, err)
		}
		if p := (Placed{Dir: got}).Path(m); p != `\\server\share\skills\a` {
			t.Errorf("Path(%q) = %s, want %s", got, p, `\\server\share\skills\a`)
		}
	}
}
