package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// setHome points the home directory at dir for the duration of the test.
func setHome(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	} else {
		t.Setenv("HOME", dir)
	}
}

func TestHasHomePrefix(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"~", true},
		{"~/", true},
		{"~/.claude", true},
		{`~\.claude`, true},
		{"~user/x", false},
		{"~~", false},
		{"", false},
		{"./~", false},
		{".claude", false},
	}
	for _, tt := range tests {
		if got := HasHomePrefix(tt.in); got != tt.want {
			t.Errorf("HasHomePrefix(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	tests := []struct {
		in   string
		want string
	}{
		{"~", home},
		{"~/", home},
		{"~/.claude", filepath.Join(home, ".claude")},
		{"~/a/b", filepath.Join(home, "a", "b")},
		{`~\a`, filepath.Join(home, "a")},
		{".claude", ".claude"},
		{"~user/x", "~user/x"},
	}
	for _, tt := range tests {
		got, err := ExpandHome(tt.in)
		if err != nil {
			t.Fatalf("ExpandHome(%q): %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("ExpandHome(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestIsAbs(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"/usr/local", true},
		{"/", true},
		{`\foo`, true},
		{`\\server\share`, true},
		{"//server/share", true},
		{`C:\Users`, true},
		{"c:/Users", true},
		{"C:foo", true},
		{"C:", true},
		{"", false},
		{".claude", false},
		{"../hub", false},
		{"~/x", false},
		{"1:foo", false},
		{"a/b:c", false},
	}
	for _, tt := range tests {
		if got := IsAbs(tt.in); got != tt.want {
			t.Errorf("IsAbs(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestToSlash(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{filepath.Join("a", "b", "c"), "a/b/c"},
		{filepath.Join("a", "..", "b"), "b"},
		{"a/./b/", "a/b"},
		{".", "."},
		{"", "."},
	}
	if runtime.GOOS == "windows" {
		tests = append(tests,
			struct{ in, want string }{`C:\Users\x\..\y`, "C:/Users/y"},
			struct{ in, want string }{`a\b`, "a/b"},
		)
	}
	for _, tt := range tests {
		if got := ToSlash(tt.in); got != tt.want {
			t.Errorf("ToSlash(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSamePath(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{filepath.Join("a", "b"), "a/b", true},
		{"a/b/../c", "a/c", true},
		{"a/b", "a/c", false},
	}
	for _, tt := range tests {
		if got := SamePath(tt.a, tt.b); got != tt.want {
			t.Errorf("SamePath(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestSameSlashPathCase(t *testing.T) {
	if !sameSlashPath("C:/Users/A", "c:/users/a", true) {
		t.Error("case-insensitive comparison: want true")
	}
	if sameSlashPath("C:/Users/A", "c:/users/a", false) {
		t.Error("case-sensitive comparison: want false")
	}
}

func TestRealPath(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(target, "dpm.toml")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := RealPath(file)
	if err != nil {
		t.Fatal(err)
	}
	if got != file {
		t.Errorf("RealPath(%q) = %q, want %q", file, got, file)
	}

	link := filepath.Join(dir, "link.toml")
	if err := os.Symlink(file, link); err != nil {
		t.Skipf("cannot create a symbolic link (Windows may require a privilege): %v", err)
	}
	got, err = RealPath(link)
	if err != nil {
		t.Fatal(err)
	}
	if got != file {
		t.Errorf("RealPath(%q) = %q, want %q", link, got, file)
	}

	if _, err := RealPath(filepath.Join(dir, "missing")); err == nil {
		t.Error("RealPath of a missing path: want an error")
	}
}
