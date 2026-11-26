package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/e8n/dpm/internal/state"
)

// check runs "dpm check" with args in the project's directory.
func (p *project) check(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Chdir(p.dir)
	var out, errOut bytes.Buffer
	code = run(append([]string{"check"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

// checkJSON runs "dpm check --json" and decodes its output.
func (p *project) checkJSON(t *testing.T) (int, checkReport) {
	t.Helper()
	code, stdout, stderr := p.check(t, "--json")
	var r checkReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("cannot decode the output: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	return code, r
}

// recorded returns the integrity recorded for dir.
func recorded(t *testing.T, st *state.State, dir string) string {
	t.Helper()
	for _, pl := range st.Placed {
		if pl.Dir == dir {
			return pl.Integrity
		}
	}
	t.Fatalf("%s is not recorded", dir)
	return ""
}

// TestCheckUpToDate reports nothing after an install and changes nothing.
func TestCheckUpToDate(t *testing.T) {
	p := newProject(t)
	writeFiles(t, p.hub, map[string]string{"commit/SKILL.md": "commit\n"})
	commitAll(t, p.hub)
	newRepo(t, p.remote)
	writeFiles(t, p.remote, map[string]string{"SKILL.md": "browser\n"})
	commitAll(t, p.remote)
	p.manifest(t, `
[defaults]
to = ["${claude}/skills", "${agents}/skills"]

[deps.commit]
path = "../hub/commit"

[deps.browser]
git = "`+fileURL(p.remote)+`"
`)
	p.mustInstall(t)
	writeFiles(t, p.dir, map[string]string{".claude/skills/_local/SKILL.md": "local\n"})
	before := snapshot(t, p.dir)

	code, stdout, stderr := p.check(t)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitOK, stdout, stderr)
	}
	if stdout != "dpm.toml: up to date\n" || stderr != "" {
		t.Errorf("stdout, stderr = %q, %q; want %q, empty", stdout, stderr, "dpm.toml: up to date\n")
	}

	code, r := p.checkJSON(t)
	if code != exitOK || r.Command != "check" || !r.OK || r.Error != nil || len(r.Results) != 0 {
		t.Errorf("exit code = %d, report = %+v; want %d, ok with no results", code, r, exitOK)
	}
	wantFiles(t, p.dir, before)
}

// TestCheckDifferences reports each of the five differences, and nothing
// for a record that install would only forget or for a local directory.
// It changes nothing; install then brings everything up to date.
func TestCheckDifferences(t *testing.T) {
	p := newProject(t)
	for _, dep := range []string{"missing", "modified", "both", "leftover", "adoptable"} {
		writeFiles(t, p.hub, map[string]string{dep + "/SKILL.md": dep + "\n"})
	}
	commitAll(t, p.hub)
	newRepo(t, p.remote)
	writeFiles(t, p.remote, map[string]string{"SKILL.md": "outdated v1\n"})
	commitAll(t, p.remote)
	p.manifest(t, `
[defaults]
to = ["${claude}/skills"]

[deps.missing]
path = "../hub/missing"

[deps.modified]
path = "../hub/modified"

[deps.both]
path = "../hub/both"

[deps.leftover]
path = "../hub/leftover"

[deps.outdated]
git = "`+fileURL(p.remote)+`"
`)
	p.mustInstall(t)
	st := p.state(t)
	// A record that install would only forget: no longer in the manifest
	// and already gone.
	st.Placed = append(st.Placed, state.Placed{Dep: "gone", Dir: ".claude/skills/gone", Integrity: st.Placed[0].Integrity})
	if err := state.Save(p.dir, st); err != nil {
		t.Fatal(err)
	}
	st = p.state(t)

	if err := os.RemoveAll(p.path(".claude/skills/missing")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, p.dir, map[string]string{
		".claude/skills/modified/SKILL.md":  "edited\n",
		".claude/skills/both/SKILL.md":      "edited\n",
		".claude/skills/adoptable/SKILL.md": "left by APM\n",
		".claude/skills/_local/SKILL.md":    "local\n",
	})
	writeFiles(t, p.hub, map[string]string{"both/SKILL.md": "both v2\n"})
	writeFiles(t, p.remote, map[string]string{"SKILL.md": "outdated v2\n"})
	head := commitAll(t, p.remote)
	p.manifest(t, `
[defaults]
to = ["${claude}/skills"]

[deps.missing]
path = "../hub/missing"

[deps.modified]
path = "../hub/modified"

[deps.both]
path = "../hub/both"

[deps.adoptable]
path = "../hub/adoptable"

[deps.outdated]
git = "`+fileURL(p.remote)+`"

[deps.new]
path = "../hub/missing"
`)
	before := snapshot(t, p.dir)

	code, stdout, stderr := p.check(t)
	if code != exitError {
		t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitError, stdout, stderr)
	}
	want := `adoptable adoptable -> .claude/skills/adoptable (not placed by dpm)
modified  both -> .claude/skills/both (edited since it was deployed)
outdated  both -> .claude/skills/both (the source has changed)
missing   missing -> .claude/skills/missing (not deployed)
modified  modified -> .claude/skills/modified (edited since it was deployed)
missing   new -> .claude/skills/new (not deployed)
outdated  outdated -> .claude/skills/outdated (the source has changed)
leftover  leftover -> .claude/skills/leftover (no longer in dpm.toml)
dpm.toml: 2 missing, 2 modified, 2 outdated, 1 leftover, 1 adoptable; "dpm install" updates them, overwriting the local edits
`
	if stdout != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	wantFiles(t, p.dir, before)

	code, r := p.checkJSON(t)
	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	if r.Command != "check" || r.OK || r.Error != nil {
		t.Errorf("command, ok, error = %q, %v, %v; want check, false, null", r.Command, r.OK, r.Error)
	}
	if wantManifest := filepath.Join(p.dir, "dpm.toml"); r.Manifest != wantManifest {
		t.Errorf("manifest = %q, want %q", r.Manifest, wantManifest)
	}
	if len(r.Results) != 8 {
		t.Fatalf("results = %+v, want 8 entries", r.Results)
	}
	for _, i := range []int{1, 4} {
		if d := r.Results[i].Deployed; !strings.HasPrefix(d, "git-sha1:") || d == r.Results[i].Integrity {
			t.Errorf("results[%d].deployed = %q, want an integrity other than %q", i, d, r.Results[i].Integrity)
		}
	}
	for _, i := range []int{2, 6} {
		if s := r.Results[i].Source; !strings.HasPrefix(s, "git-sha1:") || s == r.Results[i].Integrity {
			t.Errorf("results[%d].source = %q, want an integrity other than %q", i, s, r.Results[i].Integrity)
		}
	}
	wantResults := []diffReport{
		{Dep: "adoptable", Dir: ".claude/skills/adoptable", Kind: "adoptable"},
		{Dep: "both", Dir: ".claude/skills/both", Kind: "modified", Integrity: recorded(t, st, ".claude/skills/both"), Deployed: r.Results[1].Deployed},
		{Dep: "both", Dir: ".claude/skills/both", Kind: "outdated", Integrity: recorded(t, st, ".claude/skills/both"), Source: r.Results[2].Source},
		{Dep: "missing", Dir: ".claude/skills/missing", Kind: "missing", Integrity: recorded(t, st, ".claude/skills/missing")},
		{Dep: "modified", Dir: ".claude/skills/modified", Kind: "modified", Integrity: recorded(t, st, ".claude/skills/modified"), Deployed: r.Results[4].Deployed},
		{Dep: "new", Dir: ".claude/skills/new", Kind: "missing"},
		{Dep: "outdated", Dir: ".claude/skills/outdated", Kind: "outdated", Integrity: recorded(t, st, ".claude/skills/outdated"), Source: r.Results[6].Source, Commit: head},
		{Dep: "leftover", Dir: ".claude/skills/leftover", Kind: "leftover", Integrity: recorded(t, st, ".claude/skills/leftover")},
	}
	for i := range wantResults {
		if r.Results[i] != wantResults[i] {
			t.Errorf("results[%d] = %+v, want %+v", i, r.Results[i], wantResults[i])
		}
	}
	wantFiles(t, p.dir, before)

	p.mustInstall(t)
	code, stdout, _ = p.check(t)
	if code != exitOK || stdout != "dpm.toml: up to date\n" {
		t.Errorf("after install: exit code = %d, stdout = %q; want %d, up to date", code, stdout, exitOK)
	}
}

// TestCheckExecuteBit ignores the execute bit of the deployed files, which
// Windows does not keep (spec 3.5).
func TestCheckExecuteBit(t *testing.T) {
	p := newProject(t)
	writeFiles(t, p.hub, map[string]string{"a/SKILL.md": "a\n", "a/run.sh": "#!/bin/sh\n"})
	script := filepath.Join(p.hub, "a", "run.sh")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, p.hub, "add", "-A")
	git(t, p.hub, "update-index", "--chmod=+x", "a/run.sh")
	git(t, p.hub, "commit", "-q", "-m", "executable")
	if mode := git(t, p.hub, "ls-files", "-s", "a/run.sh"); !strings.HasPrefix(mode, "100755 ") {
		t.Fatalf("ls-files = %q, want mode 100755", mode)
	}
	p.manifest(t, `
[deps.a]
path = "../hub/a"
to = ["${claude}/skills"]
`)
	p.mustInstall(t)
	// Lose the execute bit where the file system keeps it.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(p.path(".claude/skills/a/run.sh"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code, stdout, stderr := p.check(t)
	if code != exitOK || stdout != "dpm.toml: up to date\n" {
		t.Errorf("exit code = %d, stdout = %q, stderr = %q; want %d, up to date", code, stdout, stderr, exitOK)
	}
}

// TestCheckWarnings prints the warnings of the sources.
func TestCheckWarnings(t *testing.T) {
	p := newProject(t)
	writeFiles(t, p.root, map[string]string{"outside/a/SKILL.md": "a\n"})
	p.manifest(t, `
[deps.a]
path = "../outside/a"
to = ["${claude}/skills"]
`)
	p.mustInstall(t)
	code, stdout, stderr := p.check(t)
	if code != exitOK || stdout != "dpm.toml: up to date\n" {
		t.Errorf("exit code = %d, stdout = %q; want %d, up to date", code, stdout, exitOK)
	}
	wantContains(t, "stderr", stderr, `warning: dependency "a": `, "is not in a Git work tree")

	_, r := p.checkJSON(t)
	if !r.OK || len(r.Warnings) != 1 || r.Warnings[0].Dep != "a" || !strings.Contains(r.Warnings[0].Message, "is not in a Git work tree") {
		t.Errorf("ok, warnings = %v, %+v", r.OK, r.Warnings)
	}
}

// TestCheckJSONKeys checks the exact keys of the --json output, which
// scripts rely on.
func TestCheckJSONKeys(t *testing.T) {
	p := newProject(t)
	writeFiles(t, p.hub, map[string]string{"a/SKILL.md": "a\n", "b/SKILL.md": "b\n"})
	commitAll(t, p.hub)
	p.manifest(t, `
[defaults]
to = ["${claude}/skills"]

[deps.a]
path = "../hub/a"
`)
	p.mustInstall(t)
	integrity := p.state(t).Placed[0].Integrity
	writeFiles(t, p.hub, map[string]string{"a/SKILL.md": "a v2\n"})
	p.manifest(t, `
[defaults]
to = ["${claude}/skills"]

[deps.a]
path = "../hub/a"

[deps.b]
path = "../hub/b"
`)
	// Without "modified", the summary does not warn about local edits.
	_, text, _ := p.check(t)
	wantContains(t, "stdout", text, "\ndpm.toml: 1 missing, 1 outdated; \"dpm install\" updates them\n")
	_, stdout, _ := p.check(t, "--json")
	var r checkReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil || len(r.Results) != 2 {
		t.Fatalf("output = %s (error %v), want two results", stdout, err)
	}
	manifestJSON := strings.ReplaceAll(filepath.Join(p.dir, "dpm.toml"), `\`, `\\`)
	want := `{
  "command": "check",
  "ok": false,
  "manifest": "` + manifestJSON + `",
  "results": [
    {
      "dep": "a",
      "dir": ".claude/skills/a",
      "kind": "outdated",
      "integrity": "` + integrity + `",
      "source": "` + r.Results[0].Source + `"
    },
    {
      "dep": "b",
      "dir": ".claude/skills/b",
      "kind": "missing"
    }
  ],
  "warnings": [],
  "error": null
}
`
	if stdout != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", stdout, want)
	}
}

// TestCheckErrors covers failures and usage errors. None of them creates
// anything.
func TestCheckErrors(t *testing.T) {
	tests := []struct {
		name       string
		manifest   string // "" for no dpm.toml
		args       []string
		wantCode   int
		wantStdout string
		wantStderr []string
	}{
		{
			name:       "no manifest",
			wantCode:   exitError,
			wantStderr: []string{"dpm: dpm.toml was not found in "},
		},
		{
			name:       "invalid manifest",
			manifest:   "[deps.a]\npath = \"../a\"\nto = [\"skills\"]\n",
			wantCode:   exitError,
			wantStderr: []string{"dpm: dpm.toml: deps.a: "},
		},
		{
			name:       "missing source",
			manifest:   "[deps.a]\npath = \"../nothing\"\nto = [\"${claude}/skills\"]\n",
			wantCode:   exitError,
			wantStderr: []string{`dpm: dependency "a"`},
		},
		{
			name:       "unexpected argument",
			args:       []string{"extra"},
			wantCode:   exitUsage,
			wantStderr: []string{`dpm check: unexpected argument "extra"`, "Usage: dpm check"},
		},
		{
			name:       "unknown flag",
			args:       []string{"--fix"},
			wantCode:   exitUsage,
			wantStderr: []string{"flag provided but not defined: -fix", "Usage: dpm check"},
		},
		{
			name:       "help",
			args:       []string{"-h"},
			wantCode:   exitOK,
			wantStdout: "Usage: dpm check [--json]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProject(t)
			if tt.manifest != "" {
				p.manifest(t, tt.manifest)
			}
			code, stdout, stderr := p.check(t, tt.args...)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, tt.wantCode, stderr)
			}
			if tt.wantStdout == "" && stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			wantContains(t, "stdout", stdout, tt.wantStdout)
			wantContains(t, "stderr", stderr, tt.wantStderr...)
			wantMissing(t, p.path(".claude"))
			wantMissing(t, p.path(".dpm"))
		})
	}
}

// TestCheckJSONError checks the --json output of a failed check.
func TestCheckJSONError(t *testing.T) {
	p := newProject(t)
	code, stdout, stderr := p.check(t, "--json")
	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	wantContains(t, "stderr", stderr, "dpm: dpm.toml was not found in ")
	var r checkReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("cannot decode the output: %v\n%s", err, stdout)
	}
	if r.Command != "check" || r.OK || r.Manifest != "" || r.Error == nil {
		t.Fatalf("report = %+v", r)
	}
	wantContains(t, "error", *r.Error, "dpm.toml was not found in ")
	wantContains(t, "stdout", stdout, `"results": [],`, `"warnings": [],`)
}

// TestCheckSourceModeChanged changes only the execute bit in the source
// after an install. check reports the source as outdated and the
// destination as not modified, on Windows too, where the destination has
// no execute bit: install records the content, the hash with the execute
// bit ignored. A record without the content still works as before.
func TestCheckSourceModeChanged(t *testing.T) {
	p := newProject(t)
	newRepo(t, p.remote)
	writeFiles(t, p.remote, map[string]string{"SKILL.md": "skill\n", "run.sh": "#!/bin/sh\n"})
	git(t, p.remote, "add", "-A")
	// update-index sets the mode in the index whatever the file system
	// keeps.
	git(t, p.remote, "update-index", "--chmod=+x", "run.sh")
	git(t, p.remote, "commit", "-q", "-m", "executable")
	p.manifest(t, `
[deps.tool]
git = "`+fileURL(p.remote)+`"
to = ["${claude}/skills"]
`)
	p.mustInstall(t)
	st := p.state(t)
	if len(st.Placed) != 1 || st.Placed[0].Content == "" || st.Placed[0].Content == st.Placed[0].Integrity {
		t.Fatalf("record = %+v, want a content other than the integrity", st.Placed)
	}
	if code, r := p.checkJSON(t); code != exitOK || len(r.Results) != 0 {
		t.Fatalf("after install: exit code = %d, report = %+v; want no differences", code, r)
	}

	// A record written before the content was added: the modes of the
	// source are used for the destination.
	legacy := p.state(t)
	legacy.Placed[0].Content = ""
	if err := state.Save(p.dir, legacy); err != nil {
		t.Fatal(err)
	}
	if code, r := p.checkJSON(t); code != exitOK || len(r.Results) != 0 {
		t.Fatalf("record without content: exit code = %d, report = %+v; want no differences", code, r)
	}
	if err := state.Save(p.dir, st); err != nil {
		t.Fatal(err)
	}

	git(t, p.remote, "update-index", "--chmod=-x", "run.sh")
	git(t, p.remote, "commit", "-q", "-m", "not executable")
	code, r := p.checkJSON(t)
	if code != exitError || len(r.Results) != 1 || r.Results[0].Kind != "outdated" {
		t.Fatalf("exit code = %d, report = %+v; want only outdated", code, r)
	}
	if r.Results[0].Integrity != st.Placed[0].Integrity || r.Results[0].Source == st.Placed[0].Integrity {
		t.Errorf("result = %+v, want the recorded integrity and a different source", r.Results[0])
	}
}

// TestCheckAPMHint: without dpm.toml but with apm.yml, check says that
// install generates dpm.toml, and converts nothing itself.
func TestCheckAPMHint(t *testing.T) {
	const hint = `"dpm install" generates it from apm.yml`
	p := newProject(t)
	code, stdout, stderr := p.check(t)
	if code != exitError || strings.Contains(stderr, hint) {
		t.Errorf("without apm.yml: exit code = %d, stderr = %q; want %d without the hint", code, stderr, exitError)
	}

	writeFiles(t, p.dir, map[string]string{"apm.yml": apmYML})
	code, stdout, stderr = p.check(t)
	if code != exitError || stdout != "" {
		t.Errorf("exit code = %d, stdout = %q; want %d, empty", code, stdout, exitError)
	}
	wantContains(t, "stderr", stderr, "dpm: dpm.toml was not found in ", hint)

	code, r := p.checkJSON(t)
	if code != exitError || r.Error == nil {
		t.Fatalf("exit code = %d, report = %+v", code, r)
	}
	wantContains(t, "error", *r.Error, "dpm.toml was not found in ", hint)
	wantMissing(t, p.path("dpm.toml"))
	wantMissing(t, p.path(".dpm"))
	if got, err := os.ReadFile(p.path("apm.yml")); err != nil || string(got) != apmYML {
		t.Errorf("apm.yml = %q, %v; want it unchanged", got, err)
	}
}
