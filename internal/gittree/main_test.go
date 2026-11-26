package gittree

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
	tmp, err := os.MkdirTemp("", "gittree-test-")
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
	return gitEnv(t, dir, nil, args...)
}

// gitEnv is git with additional environment variables.
func gitEnv(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	out, err := Git{Dir: dir, Env: env}.Run(args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// gitInput runs git in dir with stdin and returns its trimmed output.
func gitInput(t *testing.T, dir string, env []string, stdin string, args ...string) string {
	t.Helper()
	out, err := Git{Dir: dir, Env: env}.RunInput(strings.NewReader(stdin), args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// newRepo creates an empty repository with the given object format and
// returns its top level directory (with symbolic links resolved, as git
// reports it).
func newRepo(t *testing.T, a Algorithm) string {
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

// realTempDir returns t.TempDir() with symbolic links resolved.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
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
		if f.link != "" {
			os.Remove(p)
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
		// WriteFile does not change the mode of an existing file.
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
}

// requireSymlinks skips the test when symbolic links cannot be created,
// which happens on Windows without the privilege (Developer Mode or
// administrator).
func requireSymlinks(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symbolic links cannot be created here: %v", err)
	}
}

// requireExecBit skips the test on Windows, where files have no execute bit.
func requireExecBit(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows file systems do not keep the execute bit")
	}
}
