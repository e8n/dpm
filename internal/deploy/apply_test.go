package deploy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e8n/dpm/internal/fsutil"
	"github.com/e8n/dpm/internal/gittree"
	"github.com/e8n/dpm/internal/manifest"
	"github.com/e8n/dpm/internal/source"
	"github.com/e8n/dpm/internal/state"
)

func mkdirs(t *testing.T, dir string, rels ...string) {
	t.Helper()
	for _, r := range rels {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(r)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
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

// snapshot returns the regular files under dir (slash path -> content),
// skipping the top level directories in skip. Directories are not listed:
// an empty "to" directory left by a rollback is allowed.
func snapshot(t *testing.T, dir string, skip ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		for _, s := range skip {
			if rel == s && d.IsDir() {
				return filepath.SkipDir
			}
		}
		if d.Type().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameSnapshot(t *testing.T, got, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("%s is missing", k)
		} else if g != v {
			t.Errorf("%s = %q, want %q", k, g, v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected file %s", k)
		}
	}
}

func notExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s should not exist (err = %v)", p, err)
	}
}

// installEnv is a manifest directory with sources under src/ and
// destinations under .claude/skills and .agents/skills:
//
//   - a: placed into .claude/skills and .agents/skills (which does not
//     exist yet);
//   - b: recorded and edited by hand, so replaced;
//   - c: not recorded but present, so adopted;
//   - _local: a local directory that is not touched;
//   - old: recorded but no longer in the manifest, so removed;
//   - gone: recorded but already deleted, so forgotten.
type installEnv struct {
	dir     string
	deps    []manifest.Dep
	state   *state.State
	sources []*source.Source
}

func newInstallEnv(t *testing.T) installEnv {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := planEnv{dir: dir}
	writeFiles(t, dir, map[string]string{
		"src/a/SKILL.md":                 "a new\n",
		"src/b/SKILL.md":                 "b new\n",
		"src/b/sub/x.txt":                "x\n",
		"src/c/SKILL.md":                 "c new\n",
		".claude/skills/b/SKILL.md":      "b old\n",
		".claude/skills/b/edited.txt":    "by hand\n",
		".claude/skills/c/SKILL.md":      "c by APM\n",
		".claude/skills/_local/SKILL.md": "local\n",
		".claude/skills/old/SKILL.md":    "old\n",
	})
	deps := []manifest.Dep{
		e.dep("a", ".claude/skills", ".agents/skills"),
		e.dep("b", ".claude/skills"),
		e.dep("c", ".claude/skills"),
	}
	for i := range deps {
		deps[i].PathAbs = filepath.Join(dir, "src", deps[i].Name)
	}
	sources, err := source.Fetch(deps)
	if err != nil {
		t.Fatal(err)
	}
	st := e.record(".claude/skills/b", ".claude/skills/old", ".claude/skills/gone")
	for i := range st.Placed {
		st.Placed[i].Integrity = sources[1].Integrity()
	}
	if err := state.Save(dir, st); err != nil {
		t.Fatal(err)
	}
	return installEnv{dir: dir, deps: deps, state: st, sources: sources}
}

func (e installEnv) plan(t *testing.T) *Plan {
	t.Helper()
	p, err := NewPlan(e.dir, e.deps, e.state, OSDisk{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (e installEnv) path(rel string) string {
	return filepath.Join(e.dir, filepath.FromSlash(rel))
}

// failRename replaces fsutil.Rename with one that fails on the calls for
// which fail returns true (calls are numbered from 1).
func failRename(t *testing.T, fail func(call int) bool) {
	t.Helper()
	orig := fsutil.Rename
	t.Cleanup(func() { fsutil.Rename = orig })
	call := 0
	fsutil.Rename = func(from, to string) error {
		call++
		if fail(call) {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: errors.New("injected failure")}
		}
		return orig(from, to)
	}
}

func TestInstall(t *testing.T) {
	e := newInstallEnv(t)
	p := e.plan(t)
	var actions []string
	for _, it := range p.Items {
		actions = append(actions, it.Action.String()+" "+it.Dir)
	}
	wantActions := "place .claude/skills/a, place .agents/skills/a, replace .claude/skills/b, adopt .claude/skills/c, forget .claude/skills/gone, remove .claude/skills/old"
	if got := strings.Join(actions, ", "); got != wantActions {
		t.Fatalf("plan = %s\nwant %s", got, wantActions)
	}

	warnings, err := Install(p, e.sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q", warnings)
	}
	sameSnapshot(t, snapshot(t, e.dir, "src"), map[string]string{
		".claude/skills/a/SKILL.md":      "a new\n",
		".agents/skills/a/SKILL.md":      "a new\n",
		".claude/skills/b/SKILL.md":      "b new\n",
		".claude/skills/b/sub/x.txt":     "x\n",
		".claude/skills/c/SKILL.md":      "c new\n",
		".claude/skills/_local/SKILL.md": "local\n",
		".dpm/.gitignore":                "*\n",
		".dpm/state.toml":                stateFile(t, e, ".agents/skills/a", ".claude/skills/a", ".claude/skills/b", ".claude/skills/c"),
	})
	notExist(t, e.path(".claude/"+StagingName))
	notExist(t, e.path(".agents/"+StagingName))
}

// stateFile returns the expected content of state.toml with the given
// directories, each with the integrity and the content of its source.
func stateFile(t *testing.T, e installEnv, dirs ...string) string {
	t.Helper()
	bySource := map[string]*source.Source{}
	for _, s := range e.sources {
		bySource[s.Dep] = s
	}
	st := &state.State{}
	for _, d := range dirs {
		dep := filepath.Base(d)
		content, err := bySource[dep].Tree.Content()
		if err != nil {
			t.Fatal(err)
		}
		st.Placed = append(st.Placed, state.Placed{Dep: dep, Dir: d, Integrity: bySource[dep].Integrity(), Content: content})
	}
	data, err := state.Encode(st)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestInstallRollback makes each rename of the swap fail in turn and
// checks that the destinations and the record are as before.
func TestInstallRollback(t *testing.T) {
	// The plan of installEnv renames 7 times: a (2 places), b and c (2 each)
	// and old (1).
	const renames = 7
	for n := 1; n <= renames; n++ {
		t.Run(fmt.Sprintf("rename %d", n), func(t *testing.T) {
			e := newInstallEnv(t)
			before := snapshot(t, e.dir)
			p := e.plan(t)
			failRename(t, func(call int) bool { return call == n })
			_, err := Install(p, e.sources)
			if err == nil || !strings.Contains(err.Error(), "injected failure") || !strings.Contains(err.Error(), "restored") {
				t.Fatalf("err = %v, want the injected failure and a rollback", err)
			}
			sameSnapshot(t, snapshot(t, e.dir), before)
			notExist(t, e.path(".claude/"+StagingName))
			notExist(t, e.path(".agents/"+StagingName))
		})
	}
}

// TestInstallRollbackOnRecordFailure makes the write of the record fail
// after every directory is swapped.
func TestInstallRollbackOnRecordFailure(t *testing.T) {
	e := newInstallEnv(t)
	// A directory in place of state.toml makes state.Save fail.
	if err := os.Remove(e.path(".dpm/state.toml")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, e.dir, map[string]string{".dpm/state.toml/keep": ""})
	before := snapshot(t, e.dir)
	_, err := Install(e.plan(t), e.sources)
	if err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("err = %v, want a rollback", err)
	}
	sameSnapshot(t, snapshot(t, e.dir), before)
	notExist(t, e.path(".claude/"+StagingName))
}

// TestInstallRollbackFailure makes the rollback fail too: the staging
// directory, which holds the previous contents, must be kept.
func TestInstallRollbackFailure(t *testing.T) {
	e := newInstallEnv(t)
	p := e.plan(t)
	// Call 4 places the new b after the old b is moved away (calls 1-3);
	// every later call, including the rollback, fails.
	failRename(t, func(call int) bool { return call >= 4 })
	_, err := Install(p, e.sources)
	if err == nil || !strings.Contains(err.Error(), "restoring the previous contents also failed") {
		t.Fatalf("err = %v, want a failed rollback", err)
	}
	got := snapshot(t, e.path(".claude/"+StagingName))
	if got["old/2/SKILL.md"] != "b old\n" || got["old/2/edited.txt"] != "by hand\n" {
		t.Errorf("the previous contents of b are not kept in the staging directory: %v", got)
	}
}

func TestStageRemovesLeftovers(t *testing.T) {
	e := newInstallEnv(t)
	writeFiles(t, e.dir, map[string]string{".claude/" + StagingName + "/new/0/left.txt": "left\n"})
	if _, err := Install(e.plan(t), e.sources); err != nil {
		t.Fatal(err)
	}
	notExist(t, e.path(".claude/skills/a/left.txt"))
	notExist(t, e.path(".claude/"+StagingName))
}

func TestStageAndDiscard(t *testing.T) {
	e := newInstallEnv(t)
	before := snapshot(t, e.dir, "src", ".claude/"+StagingName, ".agents/"+StagingName)
	s, err := Stage(e.plan(t), e.sources)
	if err != nil {
		t.Fatal(err)
	}
	staged := snapshot(t, e.path(".claude/"+StagingName))
	if staged[".gitignore"] != "*\n" || staged["new/0/SKILL.md"] != "a new\n" || staged["new/2/sub/x.txt"] != "x\n" {
		t.Errorf("staged files = %v", staged)
	}
	if got := snapshot(t, e.path(".agents/"+StagingName)); got["new/1/SKILL.md"] != "a new\n" {
		t.Errorf("staged files for .agents = %v", got)
	}
	sameSnapshot(t, snapshot(t, e.dir, "src", ".claude/"+StagingName, ".agents/"+StagingName), before)
	if err := s.Discard(); err != nil {
		t.Fatal(err)
	}
	notExist(t, e.path(".claude/"+StagingName))
	notExist(t, e.path(".agents/"+StagingName))
}

// TestStageWriteFailure makes the write of the third item fail after the
// first two are staged: the staging directories must be removed and the
// destinations left as they were.
func TestStageWriteFailure(t *testing.T) {
	e := newInstallEnv(t)
	before := snapshot(t, e.dir, "src")
	// A submodule entry cannot be written, so WriteTo fails for b.
	broken := &source.Source{Dep: "b", Tree: gittree.Tree{
		Algorithm: gittree.SHA1,
		Entries:   []gittree.Entry{{Path: "sub", Mode: gittree.ModeGitlink, Hash: strings.Repeat("0", 40)}},
	}}
	sources := []*source.Source{e.sources[0], broken, e.sources[2]}
	_, err := Stage(e.plan(t), sources)
	if err == nil || !strings.Contains(err.Error(), "submodule") {
		t.Fatalf("err = %v, want the write failure of b", err)
	}
	sameSnapshot(t, snapshot(t, e.dir, "src"), before)
	notExist(t, e.path(".claude/"+StagingName))
	notExist(t, e.path(".agents/"+StagingName))
}

func TestStageErrors(t *testing.T) {
	e := newInstallEnv(t)
	p := e.plan(t)
	_, err := Stage(p, e.sources[1:])
	if err == nil || !strings.Contains(err.Error(), `"a" was not fetched`) {
		t.Errorf("err = %v, want a missing source", err)
	}

	pe := planEnv{dir: e.dir}
	clash := pe.dep(StagingName, ".claude")
	p, err = NewPlan(e.dir, []manifest.Dep{clash, e.deps[0]}, nil, OSDisk{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Stage(p, append([]*source.Source{{Dep: StagingName}}, e.sources...))
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Errorf("err = %v, want an overlap with the staging directory", err)
	}
}

// TestInstallTwice checks that a second install replaces what the first
// placed and records the same state.
func TestInstallTwice(t *testing.T) {
	e := newInstallEnv(t)
	if _, err := Install(e.plan(t), e.sources); err != nil {
		t.Fatal(err)
	}
	first := snapshot(t, e.dir)
	st, err := state.Load(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewPlan(e.dir, e.deps, st, OSDisk{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range p.Items {
		if it.Action != Replace {
			t.Errorf("%s: %s, want replace", it.Dir, it.Action)
		}
	}
	if _, err := Install(p, e.sources); err != nil {
		t.Fatal(err)
	}
	sameSnapshot(t, snapshot(t, e.dir), first)
}

// TestStateContent records the content, which differs from the integrity
// when the source has executable files.
func TestStateContent(t *testing.T) {
	a := gittree.SHA1
	entries := []gittree.Entry{
		{Path: "SKILL.md", Mode: gittree.ModeFile, Hash: gittree.BlobHash(a, []byte("skill\n"))},
		{Path: "run.sh", Mode: gittree.ModeExec, Hash: gittree.BlobHash(a, []byte("#!/bin/sh\n"))},
	}
	h, err := gittree.TreeHash(a, entries)
	if err != nil {
		t.Fatal(err)
	}
	src := &source.Source{Dep: "a", Tree: gittree.Tree{Algorithm: a, Hash: h, Entries: entries}}
	allFile := append([]gittree.Entry(nil), entries...)
	allFile[1].Mode = gittree.ModeFile
	contentHash, err := gittree.TreeHash(a, allFile)
	if err != nil {
		t.Fatal(err)
	}
	s := &Staging{
		plan: &Plan{Items: []Item{
			{Action: Place, Dep: "a", Dir: ".claude/skills/a"},
			{Action: Remove, Dep: "b", Dir: ".claude/skills/b"},
		}},
		sources: map[string]*source.Source{"a": src},
	}
	st, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	want := state.Placed{Dep: "a", Dir: ".claude/skills/a", Integrity: src.Integrity(), Content: gittree.FormatIntegrity(a, contentHash)}
	if len(st.Placed) != 1 || st.Placed[0] != want {
		t.Errorf("State = %+v, want [%+v]", st.Placed, want)
	}
	if want.Content == want.Integrity {
		t.Error("the content equals the integrity; the test needs an executable file")
	}
}
