package source

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/e8n/dpm/internal/gittree"
)

// TestMain isolates git from the configuration of the machine: the system
// and global configuration files are not read (Git for Windows sets
// core.autocrlf=true in its system configuration), and the identity used
// by commits is fixed. Tests that need a setting configure it in their own
// repository.
func TestMain(m *testing.M) {
	code, err := runTests(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runTests(m *testing.M) (int, error) {
	tmp, err := os.MkdirTemp("", "source-test-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)
	global := filepath.Join(tmp, "gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		return 0, err
	}
	env := map[string]string{
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL":   global,
		"GIT_AUTHOR_NAME":     "dpm test",
		"GIT_AUTHOR_EMAIL":    "test@example.com",
		"GIT_COMMITTER_NAME":  "dpm test",
		"GIT_COMMITTER_EMAIL": "test@example.com",
	}
	for k, v := range env {
		if err := os.Setenv(k, v); err != nil {
			return 0, err
		}
	}
	return m.Run(), nil
}

// git runs git in dir and returns its trimmed standard output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gittree.Git{Dir: dir}.Run(args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// realTempDir returns t.TempDir() with symbolic links resolved.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// newRepo creates a repository with the given object format and returns
// its top level directory.
func newRepo(t *testing.T, a gittree.Algorithm) string {
	t.Helper()
	dir := realTempDir(t)
	git(t, dir, "init", "-q", "--object-format="+string(a))
	// Fixed per repository so that the test does not depend on the
	// platform defaults.
	git(t, dir, "config", "core.autocrlf", "false")
	git(t, dir, "config", "core.filemode", fmt.Sprint(runtime.GOOS != "windows"))
	git(t, dir, "config", "core.symlinks", "true")
	return dir
}

// commitAll records every file of the work tree in a new commit and returns
// its hash.
func commitAll(t *testing.T, repo string) string {
	t.Helper()
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "commit")
	return git(t, repo, "rev-parse", "HEAD")
}

// fileURL returns the file:// URL of a local directory.
func fileURL(dir string) string {
	p := filepath.ToSlash(dir)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // a Windows drive: file:///C:/...
	}
	return "file://" + p
}

// file describes a file to create with writeFiles.
type file struct {
	path string // slash path
	data string
	exec bool   // execute bit (ignored on Windows)
	link string // when not empty, create a symbolic link to this target
}

// writeFiles creates files under dir.
func writeFiles(t *testing.T, dir string, files []file) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		os.Remove(p)
		if f.link != "" {
			if err := os.Symlink(f.link, p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		mode := os.FileMode(0o644)
		if f.exec {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(f.data), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
}

// canSymlink reports whether symbolic links can be created here; on
// Windows that needs Developer Mode or administrator rights.
func canSymlink(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	return os.Symlink("target", filepath.Join(dir, "link")) == nil
}

// requireSymlinks skips the test when symbolic links cannot be created.
func requireSymlinks(t *testing.T) {
	t.Helper()
	if !canSymlink(t) {
		t.Skip("symbolic links cannot be created here")
	}
}

// fetchOne fetches one dependency and fails the test on an error.
func fetchOne(t *testing.T, dep depSpec) *Source {
	t.Helper()
	srcs, err := Fetch(deps(dep))
	if err != nil {
		t.Fatal(err)
	}
	return srcs[0]
}

// wantErr checks that err is not nil and contains every one of subs.
func wantErr(t *testing.T, err error, subs ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want one containing %q", subs)
	}
	for _, s := range subs {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not contain %q", err, s)
		}
	}
}

// revParse returns the output of "git rev-parse --verify <rev>" in repo.
func revParse(t *testing.T, repo, rev string) string {
	t.Helper()
	return git(t, repo, "rev-parse", "--verify", rev)
}

// gitInput runs git in dir with stdin and returns its trimmed output.
func gitInput(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	out, err := gittree.Git{Dir: dir}.RunInput(strings.NewReader(stdin), args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}
