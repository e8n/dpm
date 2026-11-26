package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/e8n/dpm/internal/gittree"
	"github.com/e8n/dpm/internal/state"
)

// TestMain isolates git from the configuration of the machine: the system
// and global configuration files are not read (Git for Windows sets
// core.autocrlf=true in its system configuration), and the identity used
// by commits is fixed.
func TestMain(m *testing.M) {
	code, err := runTests(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runTests(m *testing.M) (int, error) {
	tmp, err := os.MkdirTemp("", "dpm-test-")
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

// newRepo creates a repository in dir with settings fixed so that the test
// does not depend on the platform defaults.
func newRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	git(t, dir, "config", "core.autocrlf", "false")
	git(t, dir, "config", "core.filemode", fmt.Sprint(runtime.GOOS != "windows"))
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

// writeFiles writes files given as slash path -> content under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// snapshot returns the regular files under dir as slash path -> content,
// or an empty map when dir does not exist.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, p)
			out[filepath.ToSlash(rel)] = string(data)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return out
}

// wantFiles checks that dir holds exactly the files in want.
func wantFiles(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	got := snapshot(t, dir)
	for k, v := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("%s: %s is missing", dir, k)
		} else if g != v {
			t.Errorf("%s: %s = %q, want %q", dir, k, g, v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: unexpected file %s", dir, k)
		}
	}
}

// wantMissing checks that nothing exists at p.
func wantMissing(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Errorf("%s exists (err = %v), want it missing", p, err)
	}
}

// project is a manifest's directory with sources next to it:
//
//	root/
//	  project/   dpm.toml and the destinations
//	  hub/       a Git work tree with path dependencies
//	  remote/    a Git repository used through file://
type project struct {
	root, dir, hub, remote string
}

func newProject(t *testing.T) *project {
	t.Helper()
	root := realTempDir(t)
	p := &project{
		root:   root,
		dir:    filepath.Join(root, "project"),
		hub:    filepath.Join(root, "hub"),
		remote: filepath.Join(root, "remote"),
	}
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	newRepo(t, p.hub)
	return p
}

// path returns the OS path of a slash path relative to the project.
func (p *project) path(rel string) string {
	return filepath.Join(p.dir, filepath.FromSlash(rel))
}

// manifest writes dpm.toml.
func (p *project) manifest(t *testing.T, content string) {
	t.Helper()
	writeFiles(t, p.dir, map[string]string{"dpm.toml": content})
}

// install runs "dpm install" with args in the project's directory.
func (p *project) install(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Chdir(p.dir)
	var out, errOut bytes.Buffer
	code = run(append([]string{"install"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

// mustInstall runs "dpm install" and fails the test unless it succeeds.
func (p *project) mustInstall(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()
	code, stdout, stderr := p.install(t, args...)
	if code != exitOK {
		t.Fatalf("install exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitOK, stdout, stderr)
	}
	return stdout, stderr
}

// installJSON runs "dpm install --json" and decodes its output.
func (p *project) installJSON(t *testing.T) (int, installReport) {
	t.Helper()
	code, stdout, stderr := p.install(t, "--json")
	var r installReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("cannot decode the output: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	return code, r
}

// state reads the record of the project.
func (p *project) state(t *testing.T) *state.State {
	t.Helper()
	st, err := state.Load(p.dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// wantRecord checks the dep and dir of every record, in order.
func wantRecord(t *testing.T, st *state.State, want ...string) {
	t.Helper()
	var got []string
	for _, pl := range st.Placed {
		got = append(got, pl.Dep+" "+pl.Dir)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("record = %q, want %q", got, want)
	}
}

// wantContains checks that s contains every one of subs.
func wantContains(t *testing.T, name, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("%s does not contain %q:\n%s", name, sub, s)
		}
	}
}
