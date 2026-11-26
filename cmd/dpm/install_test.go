package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e8n/dpm/internal/deploy"
	"github.com/e8n/dpm/internal/fsutil"
	"github.com/e8n/dpm/internal/state"
)

// TestInstallPlaceAndReplace deploys a path dependency and a Git dependency,
// changes both sources and installs again.
func TestInstallPlaceAndReplace(t *testing.T) {
	p := newProject(t)
	writeFiles(t, p.hub, map[string]string{"packages/commit/SKILL.md": "commit v1\n"})
	commitAll(t, p.hub)
	newRepo(t, p.remote)
	writeFiles(t, p.remote, map[string]string{
		"README.md":                 "not deployed\n",
		"skills/browser/SKILL.md":   "browser v1\n",
		"skills/browser/ref/doc.md": "doc\n",
	})
	commitAll(t, p.remote)
	p.manifest(t, `
[defaults]
to = ["${claude}/skills"]

[deps.commit]
path = "../hub/packages/commit"

[deps.browser]
git = "`+fileURL(p.remote)+`"
subdir = "skills/browser"
`)

	stdout, stderr := p.mustInstall(t)
	wantContains(t, "stdout", stdout,
		"placed   browser -> .claude/skills/browser\n",
		"placed   commit -> .claude/skills/commit\n",
		"dpm.toml: 2 placed\n")
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	wantFiles(t, p.path(".claude/skills/commit"), map[string]string{"SKILL.md": "commit v1\n"})
	wantFiles(t, p.path(".claude/skills/browser"), map[string]string{"SKILL.md": "browser v1\n", "ref/doc.md": "doc\n"})
	wantRecord(t, p.state(t), "browser .claude/skills/browser", "commit .claude/skills/commit")
	wantFiles(t, p.path(".dpm"), map[string]string{
		".gitignore": "*\n",
		"state.toml": string(mustEncode(t, p.state(t))),
	})
	wantMissing(t, p.path(".claude/"+deploy.StagingName))

	// The work tree of a path dependency is used as it is, committed or
	// not; a Git dependency follows its default branch.
	writeFiles(t, p.hub, map[string]string{"packages/commit/SKILL.md": "commit v2\n"})
	writeFiles(t, p.remote, map[string]string{"skills/browser/SKILL.md": "browser v2\n"})
	if err := os.Remove(filepath.Join(p.remote, "skills/browser/ref/doc.md")); err != nil {
		t.Fatal(err)
	}
	commitAll(t, p.remote)
	// A file edited by hand is overwritten.
	writeFiles(t, p.path(".claude/skills/commit"), map[string]string{"SKILL.md": "edited\n", "extra.md": "extra\n"})

	stdout, _ = p.mustInstall(t)
	wantContains(t, "stdout", stdout,
		"replaced browser -> .claude/skills/browser\n",
		"replaced commit -> .claude/skills/commit\n",
		"dpm.toml: 2 replaced\n")
	wantFiles(t, p.path(".claude/skills/commit"), map[string]string{"SKILL.md": "commit v2\n"})
	wantFiles(t, p.path(".claude/skills/browser"), map[string]string{"SKILL.md": "browser v2\n"})
	wantRecord(t, p.state(t), "browser .claude/skills/browser", "commit .claude/skills/commit")
}

func mustEncode(t *testing.T, st *state.State) []byte {
	t.Helper()
	data, err := state.Encode(st)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestInstallAdopt overwrites an unrecorded directory named after a
// dependency and leaves the other directories alone.
func TestInstallAdopt(t *testing.T) {
	p := newProject(t)
	writeFiles(t, p.hub, map[string]string{"commit/SKILL.md": "commit\n"})
	commitAll(t, p.hub)
	writeFiles(t, p.dir, map[string]string{
		".claude/skills/commit/SKILL.md": "left by APM\n",
		".claude/skills/commit/old.md":   "old\n",
		".claude/skills/_local/SKILL.md": "local\n",
		".claude/skills/manual/SKILL.md": "manual\n",
	})
	p.manifest(t, `
[deps.commit]
path = "../hub/commit"
to = ["${claude}/skills"]
`)

	stdout, _ := p.mustInstall(t)
	wantContains(t, "stdout", stdout,
		"adopted  commit -> .claude/skills/commit (it was not placed by dpm; overwritten)\n",
		"dpm.toml: 1 adopted\n")
	wantFiles(t, p.path(".claude/skills"), map[string]string{
		"commit/SKILL.md": "commit\n",
		"_local/SKILL.md": "local\n",
		"manual/SKILL.md": "manual\n",
	})
	wantRecord(t, p.state(t), "commit .claude/skills/commit")

	// Adopted once, it is replaced from then on.
	stdout, _ = p.mustInstall(t)
	wantContains(t, "stdout", stdout, "replaced commit -> .claude/skills/commit\n")
}

// TestInstallRemove cleans up the directories of a dependency removed from
// the manifest and of a destination removed from "to".
func TestInstallRemove(t *testing.T) {
	p := newProject(t)
	writeFiles(t, p.hub, map[string]string{
		"a/SKILL.md": "a\n",
		"b/SKILL.md": "b\n",
	})
	commitAll(t, p.hub)
	p.manifest(t, `
[defaults]
to = ["${claude}/skills", "${agents}/skills"]

[deps.a]
path = "../hub/a"

[deps.b]
path = "../hub/b"
`)
	p.mustInstall(t)
	wantRecord(t, p.state(t),
		"a .agents/skills/a", "b .agents/skills/b",
		"a .claude/skills/a", "b .claude/skills/b")
	writeFiles(t, p.dir, map[string]string{".claude/skills/_local/SKILL.md": "local\n"})

	p.manifest(t, `
[defaults]
to = ["${claude}/skills"]

[deps.a]
path = "../hub/a"
`)
	stdout, _ := p.mustInstall(t)
	wantContains(t, "stdout", stdout,
		"replaced a -> .claude/skills/a\n",
		"removed  a -> .agents/skills/a\n",
		"removed  b -> .agents/skills/b\n",
		"removed  b -> .claude/skills/b\n",
		"dpm.toml: 1 replaced, 3 removed\n")
	wantFiles(t, p.path(".claude/skills"), map[string]string{
		"a/SKILL.md":      "a\n",
		"_local/SKILL.md": "local\n",
	})
	wantFiles(t, p.path(".agents/skills"), map[string]string{})
	wantRecord(t, p.state(t), "a .claude/skills/a")
	wantMissing(t, p.path(".agents/"+deploy.StagingName))
}

// TestInstallRollback makes a rename fail part way through the swap and
// checks that the destinations and the record are as before.
func TestInstallRollback(t *testing.T) {
	p := newProject(t)
	writeFiles(t, p.hub, map[string]string{
		"a/SKILL.md":   "a v1\n",
		"b/SKILL.md":   "b v1\n",
		"old/SKILL.md": "old\n",
	})
	commitAll(t, p.hub)
	p.manifest(t, `
[defaults]
to = ["${claude}/skills"]

[deps.a]
path = "../hub/a"

[deps.b]
path = "../hub/b"

[deps.old]
path = "../hub/old"
`)
	p.mustInstall(t)
	stateBefore, err := os.ReadFile(state.File(p.dir))
	if err != nil {
		t.Fatal(err)
	}
	skillsBefore := snapshot(t, p.path(".claude/skills"))

	writeFiles(t, p.hub, map[string]string{
		"a/SKILL.md": "a v2\n",
		"b/SKILL.md": "b v2\n",
		"c/SKILL.md": "c\n",
	})
	p.manifest(t, `
[defaults]
to = ["${claude}/skills"]

[deps.a]
path = "../hub/a"

[deps.b]
path = "../hub/b"

[deps.c]
path = "../hub/c"
`)

	tests := []struct {
		name string
		// failOn is the destination whose rename fails.
		failOn string
	}{
		// The plan is a (replace), b (replace), c (place), old (remove):
		// each case fails after the previous ones were swapped.
		{name: "replace", failOn: "b"},
		{name: "place", failOn: "c"},
		{name: "remove", failOn: "old"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failErr := errors.New("injected failure")
			orig := fsutil.Rename
			t.Cleanup(func() { fsutil.Rename = orig })
			fsutil.Rename = func(from, to string) error {
				if filepath.Base(from) == tt.failOn || filepath.Base(to) == tt.failOn {
					return failErr
				}
				return orig(from, to)
			}

			code, stdout, stderr := p.install(t)
			if code != exitError {
				t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitError, stdout, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			wantContains(t, "stderr", stderr, "injected failure", "the destinations were restored")
			wantFiles(t, p.path(".claude/skills"), skillsBefore)
			stateAfter, err := os.ReadFile(state.File(p.dir))
			if err != nil {
				t.Fatal(err)
			}
			if string(stateAfter) != string(stateBefore) {
				t.Errorf("state.toml changed:\n%s\nwant:\n%s", stateAfter, stateBefore)
			}
			wantMissing(t, p.path(".claude/"+deploy.StagingName))
		})
	}

	// Without the failure, the same install succeeds.
	stdout, _ := p.mustInstall(t)
	wantContains(t, "stdout", stdout, "dpm.toml: 1 placed, 2 replaced, 1 removed\n")
	wantFiles(t, p.path(".claude/skills"), map[string]string{
		"a/SKILL.md": "a v2\n",
		"b/SKILL.md": "b v2\n",
		"c/SKILL.md": "c\n",
	})
}

// TestInstallFetchFailure checks that nothing changes when a dependency
// cannot be fetched.
func TestInstallFetchFailure(t *testing.T) {
	p := newProject(t)
	writeFiles(t, p.hub, map[string]string{"a/SKILL.md": "a\n"})
	commitAll(t, p.hub)
	p.manifest(t, `
[defaults]
to = ["${claude}/skills"]

[deps.a]
path = "../hub/a"
`)
	p.mustInstall(t)
	before := snapshot(t, p.dir)

	writeFiles(t, p.hub, map[string]string{"a/SKILL.md": "a v2\n"})
	p.manifest(t, `
[defaults]
to = ["${claude}/skills"]

[deps.a]
path = "../hub/a"

[deps.missing]
path = "../hub/missing"
`)
	before["dpm.toml"] = snapshot(t, p.dir)["dpm.toml"]
	code, stdout, stderr := p.install(t)
	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	wantContains(t, "stderr", stderr, `dpm: dependency "missing"`)
	wantFiles(t, p.dir, before)
}

// TestInstallWarnings prints the warnings of the sources with the name of
// the dependency.
func TestInstallWarnings(t *testing.T) {
	p := newProject(t)
	outside := filepath.Join(p.root, "outside", "a")
	writeFiles(t, outside, map[string]string{"SKILL.md": "a\n"})
	p.manifest(t, `
[deps.a]
path = "../outside/a"
to = ["${claude}/skills"]
`)
	stdout, stderr := p.mustInstall(t)
	wantContains(t, "stdout", stdout, "placed   a -> .claude/skills/a\n")
	wantContains(t, "stderr", stderr, `warning: dependency "a": `, "is not in a Git work tree")

	code, r := p.installJSON(t)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if len(r.Warnings) != 1 || r.Warnings[0].Dep != "a" || !strings.Contains(r.Warnings[0].Message, "is not in a Git work tree") {
		t.Errorf("warnings = %+v", r.Warnings)
	}
}

// TestInstallJSON checks the --json output of a successful install,
// including a record that is only forgotten.
func TestInstallJSON(t *testing.T) {
	p := newProject(t)
	writeFiles(t, p.hub, map[string]string{"commit/SKILL.md": "commit\n"})
	commitAll(t, p.hub)
	newRepo(t, p.remote)
	writeFiles(t, p.remote, map[string]string{"SKILL.md": "browser\n"})
	head := commitAll(t, p.remote)
	git(t, p.remote, "tag", "v1")
	p.manifest(t, `
[defaults]
to = ["${claude}/skills"]

[deps.commit]
path = "../hub/commit"

[deps.browser]
git = "`+fileURL(p.remote)+`"
ref = "v1"
`)
	code, first := p.installJSON(t)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (error: %v)", code, exitOK, first.Error)
	}

	// A record whose directory is gone and that is no longer in the
	// manifest is forgotten.
	st := p.state(t)
	st.Placed = append(st.Placed, state.Placed{Dep: "gone", Dir: ".claude/skills/gone", Integrity: first.Results[0].Integrity})
	if err := state.Save(p.dir, st); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, p.path(".claude/skills/adopted"), map[string]string{"SKILL.md": "old\n"})
	writeFiles(t, p.hub, map[string]string{"adopted/SKILL.md": "adopted\n"})
	commitAll(t, p.hub)
	p.manifest(t, `
[defaults]
to = ["${claude}/skills"]

[deps.adopted]
path = "../hub/adopted"

[deps.browser]
git = "`+fileURL(p.remote)+`"
ref = "v1"
`)
	code, r := p.installJSON(t)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (error: %v)", code, exitOK, r.Error)
	}
	if r.Command != "install" || !r.OK || r.Error != nil {
		t.Errorf("command, ok, error = %q, %v, %v; want install, true, null", r.Command, r.OK, r.Error)
	}
	if want := filepath.Join(p.dir, "dpm.toml"); r.Manifest != want {
		t.Errorf("manifest = %q, want %q", r.Manifest, want)
	}
	browserIntegrity := first.Results[0].Integrity
	if first.Results[0].Dep != "browser" || !strings.HasPrefix(browserIntegrity, "git-sha1:") {
		t.Fatalf("first results = %+v", first.Results)
	}
	if len(r.Results) != 4 {
		t.Fatalf("results = %+v, want 4 entries", r.Results)
	}
	if !strings.HasPrefix(r.Results[0].Integrity, "git-sha1:") {
		t.Errorf("integrity of adopted = %q", r.Results[0].Integrity)
	}
	want := []resultReport{
		{Dep: "adopted", Dir: ".claude/skills/adopted", Action: "adopt", Integrity: r.Results[0].Integrity},
		{Dep: "browser", Dir: ".claude/skills/browser", Action: "replace", Integrity: browserIntegrity, Commit: head},
		{Dep: "commit", Dir: ".claude/skills/commit", Action: "remove"},
		{Dep: "gone", Dir: ".claude/skills/gone", Action: "forget"},
	}
	for i := range want {
		if r.Results[i] != want[i] {
			t.Errorf("results[%d] = %+v, want %+v", i, r.Results[i], want[i])
		}
	}
	if len(r.Warnings) != 0 {
		t.Errorf("warnings = %+v, want none", r.Warnings)
	}
	wantRecord(t, p.state(t), "adopted .claude/skills/adopted", "browser .claude/skills/browser")
	for _, pl := range p.state(t).Placed {
		if pl.Dep == "browser" && pl.Integrity != browserIntegrity {
			t.Errorf("recorded integrity = %q, want %q", pl.Integrity, browserIntegrity)
		}
	}
}

// TestInstallJSONKeys checks the exact keys of the --json output, which
// scripts rely on.
func TestInstallJSONKeys(t *testing.T) {
	p := newProject(t)
	writeFiles(t, p.hub, map[string]string{"a/SKILL.md": "a\n"})
	commitAll(t, p.hub)
	p.manifest(t, `
[deps.a]
path = "../hub/a"
to = ["${claude}/skills"]
`)
	_, stdout, _ := p.install(t, "--json")
	integrity := p.state(t).Placed[0].Integrity
	manifestJSON := strings.ReplaceAll(filepath.Join(p.dir, "dpm.toml"), `\`, `\\`)
	want := `{
  "command": "install",
  "ok": true,
  "manifest": "` + manifestJSON + `",
  "results": [
    {
      "dep": "a",
      "dir": ".claude/skills/a",
      "action": "place",
      "integrity": "` + integrity + `"
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

// TestInstallErrors covers failures before anything is deployed.
func TestInstallErrors(t *testing.T) {
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
			name:       "same destination",
			manifest:   "[defaults]\nto = [\"${claude}/skills\"]\n[deps.Commit]\npath = \"../a\"\n[deps.commit]\npath = \"../a\"\n",
			wantCode:   exitError,
			wantStderr: []string{"are deployed to the same directory"},
		},
		{
			name:       "unexpected argument",
			args:       []string{"extra"},
			wantCode:   exitUsage,
			wantStderr: []string{`dpm install: unexpected argument "extra"`, "Usage: dpm install"},
		},
		{
			name:       "unknown flag",
			args:       []string{"--frozen"},
			wantCode:   exitUsage,
			wantStderr: []string{"flag provided but not defined: -frozen", "Usage: dpm install"},
		},
		{
			name:       "help",
			args:       []string{"--help"},
			wantCode:   exitOK,
			wantStdout: "Usage: dpm install [--json]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProject(t)
			if tt.manifest != "" {
				p.manifest(t, tt.manifest)
			}
			code, stdout, stderr := p.install(t, tt.args...)
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

// TestInstallJSONError checks the --json output of a failed install.
func TestInstallJSONError(t *testing.T) {
	p := newProject(t)
	code, stdout, stderr := p.install(t, "--json")
	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	wantContains(t, "stderr", stderr, "dpm: dpm.toml was not found in ")
	_, r := p.installJSON(t)
	if r.Command != "install" || r.OK || r.Manifest != "" || r.Error == nil {
		t.Fatalf("report = %+v", r)
	}
	wantContains(t, "error", *r.Error, "dpm.toml was not found in ")
	if r.Results == nil || len(r.Results) != 0 || r.Warnings == nil || len(r.Warnings) != 0 {
		t.Errorf("results, warnings = %#v, %#v; want empty arrays", r.Results, r.Warnings)
	}
	wantContains(t, "stdout", stdout, `"results": [],`, `"warnings": [],`, `"error": "dpm.toml was not found in `)
}

// apmYML is an apm.yml as APM writes it, with a byte order mark.
const apmYML = "\xef\xbb\xbfname: project\nversion: 1.0.0\ntargets:\n- claude\ndependencies:\n  apm:\n  - ../hub/packages/commit\n  - ../hub/packages/review\n  mcp: []\nincludes: auto\nscripts: {}\n"

// apmDpmTOML is the dpm.toml generated from apmYML.
const apmDpmTOML = `# Generated by dpm from apm.yml.

[defaults]
to = ["${claude}/skills"]

[deps.commit]
path = "../hub/packages/commit"

[deps.review]
path = "../hub/packages/review"
`

// TestInstallFromAPM generates dpm.toml from apm.yml, installs it over the
// directories that APM placed, and leaves apm.yml and apm.lock.yaml alone.
func TestInstallFromAPM(t *testing.T) {
	p := newProject(t)
	writeFiles(t, p.hub, map[string]string{
		"packages/commit/SKILL.md": "commit\n",
		"packages/review/SKILL.md": "review\n",
	})
	commitAll(t, p.hub)
	apmFiles := map[string]string{
		"apm.yml":       apmYML,
		"apm.lock.yaml": "lockfile_version: '1'\n",
	}
	writeFiles(t, p.dir, apmFiles)
	writeFiles(t, p.path(".claude/skills"), map[string]string{
		"commit/SKILL.md": "placed by APM\n",
		"_local/SKILL.md": "local\n",
	})

	stdout, stderr := p.mustInstall(t)
	if !strings.HasPrefix(stdout, "generated dpm.toml from apm.yml (apm.yml and apm.lock.yaml are unchanged)\n") {
		t.Errorf("stdout does not start with the generated line:\n%s", stdout)
	}
	wantContains(t, "stdout", stdout,
		"adopted  commit -> .claude/skills/commit (it was not placed by dpm; overwritten)\n",
		"placed   review -> .claude/skills/review\n",
		"dpm.toml: 1 placed, 1 adopted\n")
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	got, err := os.ReadFile(p.path("dpm.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != apmDpmTOML {
		t.Errorf("dpm.toml =\n%s\nwant:\n%s", got, apmDpmTOML)
	}
	for name, content := range apmFiles {
		if b, err := os.ReadFile(p.path(name)); err != nil || string(b) != content {
			t.Errorf("%s = %q (err = %v), want it unchanged", name, b, err)
		}
	}
	wantFiles(t, p.path(".claude/skills"), map[string]string{
		"commit/SKILL.md": "commit\n",
		"review/SKILL.md": "review\n",
		"_local/SKILL.md": "local\n",
	})
	wantRecord(t, p.state(t), "commit .claude/skills/commit", "review .claude/skills/review")

	// From now on dpm.toml is used, and apm.yml is ignored.
	writeFiles(t, p.dir, map[string]string{"apm.yml": "not: [valid\n"})
	stdout, _ = p.mustInstall(t)
	if strings.Contains(stdout, "generated") {
		t.Errorf("dpm.toml was generated again:\n%s", stdout)
	}
	wantContains(t, "stdout", stdout, "dpm.toml: 2 replaced\n")
}

// TestInstallFromAPMJSON checks generated_from in the --json output.
func TestInstallFromAPMJSON(t *testing.T) {
	p := newProject(t)
	writeFiles(t, p.hub, map[string]string{
		"packages/commit/SKILL.md": "commit\n",
		"packages/review/SKILL.md": "review\n",
	})
	commitAll(t, p.hub)
	writeFiles(t, p.dir, map[string]string{"apm.yml": apmYML})

	code, r := p.installJSON(t)
	if code != exitOK || !r.OK {
		t.Fatalf("exit code = %d, report = %+v", code, r)
	}
	if want := p.path("apm.yml"); r.GeneratedFrom != want {
		t.Errorf("generated_from = %q, want %q", r.GeneratedFrom, want)
	}
	if want := p.path("dpm.toml"); r.Manifest != want {
		t.Errorf("manifest = %q, want %q", r.Manifest, want)
	}
	if len(r.Results) != 2 {
		t.Errorf("results = %+v, want 2 entries", r.Results)
	}

	_, stdout, _ := p.install(t, "--json")
	if strings.Contains(stdout, "generated_from") {
		t.Errorf("generated_from is present when dpm.toml was not generated:\n%s", stdout)
	}

	// dpm.toml stays when the install fails after generating it.
	q := newProject(t)
	writeFiles(t, q.dir, map[string]string{"apm.yml": apmYML})
	code, r = q.installJSON(t)
	if code != exitError || r.OK || r.Error == nil {
		t.Fatalf("exit code = %d, report = %+v; want a failure (the sources do not exist)", code, r)
	}
	if r.GeneratedFrom != q.path("apm.yml") {
		t.Errorf("generated_from = %q, want %q", r.GeneratedFrom, q.path("apm.yml"))
	}
	if _, err := os.Stat(q.path("dpm.toml")); err != nil {
		t.Errorf("dpm.toml: %v", err)
	}
}

// TestInstallFromAPMErrors covers an apm.yml that cannot be converted and
// check, which never converts.
func TestInstallFromAPMErrors(t *testing.T) {
	p := newProject(t)
	writeFiles(t, p.dir, map[string]string{"apm.yml": "targets: [claude, copilot]\ndependencies:\n  apm:\n  - ../hub/packages/commit\n  - gitlab.com/owner/repo\n  mcp:\n  - io.github.example/server\n"})

	code, stdout, stderr := p.install(t)
	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	wantContains(t, "stderr", stderr,
		"dpm: apm.yml cannot be converted to dpm.toml:\n",
		"\n  line 1: target \"copilot\" is not supported",
		"\n  line 5: dependency \"gitlab.com/owner/repo\": ",
		"\n  line 7: MCP server \"io.github.example/server\" is not supported\n",
		"nothing was written; write dpm.toml by hand\n")
	wantMissing(t, p.path("dpm.toml"))
	wantMissing(t, p.path(".claude"))
	wantMissing(t, p.path(".dpm"))

	_, r := p.installJSON(t)
	if r.OK || r.Error == nil || r.GeneratedFrom != "" {
		t.Fatalf("report = %+v", r)
	}
	wantContains(t, "error", *r.Error, "apm.yml cannot be converted to dpm.toml:\n")

	// check reports the missing dpm.toml and does not generate it.
	q := newProject(t)
	writeFiles(t, q.dir, map[string]string{"apm.yml": apmYML})
	code, _, stderr = q.check(t)
	if code != exitError {
		t.Errorf("check exit code = %d, want %d", code, exitError)
	}
	wantContains(t, "check stderr", stderr, "dpm: dpm.toml was not found in ")
	wantMissing(t, q.path("dpm.toml"))
}
