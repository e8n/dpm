package main

import (
	"os"
	"testing"

	"github.com/e8n/dpm/internal/deploy"
	"github.com/e8n/dpm/internal/state"
)

// TestInstallRollbackOpenFile keeps a file of a destination open, which
// makes Windows refuse to move that directory, and checks that the
// directories swapped before it are moved back (spec 3.6).
func TestInstallRollbackOpenFile(t *testing.T) {
	p := newProject(t)
	writeFiles(t, p.hub, map[string]string{
		"a/SKILL.md": "a v1\n",
		"b/SKILL.md": "b v1\n",
	})
	commitAll(t, p.hub)
	p.manifest(t, `
[defaults]
to = ["${claude}/skills"]

[deps.a]
path = "../hub/a"

[deps.b]
path = "../hub/b"
`)
	p.mustInstall(t)
	stateBefore, err := os.ReadFile(state.File(p.dir))
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, p.hub, map[string]string{
		"a/SKILL.md": "a v2\n",
		"b/SKILL.md": "b v2\n",
	})

	// a is swapped first, then moving b away fails.
	f, err := os.Open(p.path(".claude/skills/b/SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	code, stdout, stderr := p.install(t)
	f.Close()
	if code != exitError {
		t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitError, stdout, stderr)
	}
	wantContains(t, "stderr", stderr, "cannot move ", "the destinations were restored")
	wantFiles(t, p.path(".claude/skills"), map[string]string{
		"a/SKILL.md": "a v1\n",
		"b/SKILL.md": "b v1\n",
	})
	stateAfter, err := os.ReadFile(state.File(p.dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(stateAfter) != string(stateBefore) {
		t.Errorf("state.toml changed:\n%s\nwant:\n%s", stateAfter, stateBefore)
	}
	wantMissing(t, p.path(".claude/"+deploy.StagingName))

	// Once the file is closed, the install succeeds.
	stdout, _ = p.mustInstall(t)
	wantContains(t, "stdout", stdout, "dpm.toml: 2 replaced\n")
	wantFiles(t, p.path(".claude/skills"), map[string]string{
		"a/SKILL.md": "a v2\n",
		"b/SKILL.md": "b v2\n",
	})
}
