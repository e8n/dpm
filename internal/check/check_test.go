package check

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/e8n/dpm/internal/deploy"
	"github.com/e8n/dpm/internal/gittree"
	"github.com/e8n/dpm/internal/source"
	"github.com/e8n/dpm/internal/state"
)

// file is a file to create with writeDir. exec sets the execute bit, which
// Windows does not keep.
type file struct {
	path string
	data string
	exec bool
}

// writeDir creates dir with exactly the given files and returns it.
func writeDir(t *testing.T, dir string, files ...file) string {
	t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
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
	return dir
}

// treeOf hashes files as a source would, taking the execute bit from the
// files themselves so that the result does not depend on the OS.
func treeOf(t *testing.T, a gittree.Algorithm, files ...file) gittree.Tree {
	t.Helper()
	dir := writeDir(t, filepath.Join(t.TempDir(), "tree"), files...)
	exec := map[string]bool{}
	for _, f := range files {
		exec[f.path] = f.exec
	}
	tree, err := gittree.DirTree(a, dir, func(p string, _ bool) gittree.Mode {
		if exec[p] {
			return gittree.ModeExec
		}
		return gittree.ModeFile
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// contentOf returns the content of tree, as install records it.
func contentOf(t *testing.T, tree gittree.Tree) string {
	t.Helper()
	c, err := tree.Content()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var (
	v1       = []file{{path: "SKILL.md", data: "v1\n"}, {path: "ref/doc.md", data: "doc\n"}}
	v2       = []file{{path: "SKILL.md", data: "v2\n"}, {path: "ref/doc.md", data: "doc\n"}}
	edited   = []file{{path: "SKILL.md", data: "edited\n"}, {path: "ref/doc.md", data: "doc\n"}}
	withExec = []file{{path: "SKILL.md", data: "v1\n"}, {path: "run.sh", data: "#!/bin/sh\n", exec: true}}
	noExec   = []file{{path: "SKILL.md", data: "v1\n"}, {path: "run.sh", data: "#!/bin/sh\n"}}
)

func TestRun(t *testing.T) {
	type want struct {
		kind     Kind
		deployed bool // Deployed is set
		source   bool // Source is set
	}
	tests := []struct {
		name     string
		action   deploy.Action
		recorded []file            // contents when placed; nil: not recorded
		alg      gittree.Algorithm // algorithm of the record (default SHA1)
		disk     []file            // contents of the destination; nil: none
		notDir   bool              // the destination is a regular file
		src      []file            // current contents of the source
		commit   string
		want     []want
	}{
		{name: "missing", action: deploy.Place, src: v1, want: []want{{kind: Missing}}},
		{name: "missing, recorded", action: deploy.Place, recorded: v1, src: v1, want: []want{{kind: Missing}}},
		{name: "adoptable", action: deploy.Adopt, disk: v1, src: v1, want: []want{{kind: Adoptable}}},
		{name: "leftover", action: deploy.Remove, recorded: v1, disk: v1, want: []want{{kind: Leftover}}},
		{name: "forget", action: deploy.Forget, recorded: v1},
		{name: "up to date", action: deploy.Replace, recorded: v1, disk: v1, src: v1},
		{name: "modified", action: deploy.Replace, recorded: v1, disk: edited, src: v1,
			want: []want{{kind: Modified, deployed: true}}},
		{name: "file added", action: deploy.Replace, recorded: v1, disk: append([]file{{path: "extra.md", data: "x\n"}}, v1...), src: v1,
			want: []want{{kind: Modified, deployed: true}}},
		{name: "outdated", action: deploy.Replace, recorded: v1, disk: v1, src: v2, commit: "abc",
			want: []want{{kind: Outdated, source: true}}},
		{name: "modified and outdated", action: deploy.Replace, recorded: v1, disk: edited, src: v2,
			want: []want{{kind: Modified, deployed: true}, {kind: Outdated, source: true}}},
		{name: "not a directory", action: deploy.Replace, recorded: v1, notDir: true, src: v1,
			want: []want{{kind: Modified}}},
		// The execute bit of the destination is ignored: Windows does not
		// keep it, and a destination without it is not modified.
		{name: "execute bit lost", action: deploy.Replace, recorded: withExec, disk: noExec, src: withExec},
		{name: "execute bit added", action: deploy.Replace, recorded: noExec, disk: withExec, src: noExec},
		// A record written with another algorithm is compared with the
		// destination in that algorithm; the source differs from it.
		{name: "sha256 record", action: deploy.Replace, recorded: v1, alg: gittree.SHA256, disk: v1, src: v1,
			want: []want{{kind: Outdated, source: true}}},
	}
	for _, tt := range tests {
		for _, withContent := range []bool{true, false} {
			name := tt.name
			if !withContent {
				name += ", record without content"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				dest := filepath.Join(root, ".claude", "skills", "a")
				switch {
				case tt.notDir:
					if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(dest, []byte("x"), 0o644); err != nil {
						t.Fatal(err)
					}
				case tt.disk != nil:
					writeDir(t, dest, tt.disk...)
				}
				it := deploy.Item{Action: tt.action, Dep: "a", Path: dest, Dir: ".claude/skills/a"}
				alg := tt.alg
				if alg == "" {
					alg = gittree.SHA1
				}
				var recorded string
				if tt.recorded != nil {
					tree := treeOf(t, alg, tt.recorded...)
					recorded = tree.Integrity()
					it.Record = &state.Placed{Dep: "a", Dir: it.Dir, Integrity: recorded}
					if withContent {
						it.Record.Content = contentOf(t, tree)
					}
				}
				var sources []*source.Source
				var srcIntegrity string
				if tt.src != nil {
					src := &source.Source{Dep: "a", Tree: treeOf(t, gittree.SHA1, tt.src...), Commit: tt.commit}
					srcIntegrity = src.Integrity()
					sources = append(sources, src)
				}
				plan := &deploy.Plan{ManifestDir: root, Items: []deploy.Item{it}}

				got, err := Run(plan, sources)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != len(tt.want) {
					t.Fatalf("differences = %+v, want %+v", got, tt.want)
				}
				for i, w := range tt.want {
					d := got[i]
					if d.Kind != w.kind || d.Item.Dep != "a" || d.Item.Path != dest {
						t.Errorf("differences[%d] = %s %s %s, want %s a %s", i, d.Kind, d.Item.Dep, d.Item.Path, w.kind, dest)
					}
					switch {
					case w.deployed && (d.Deployed == "" || d.Deployed == recorded || !strings.HasPrefix(d.Deployed, "git-"+string(alg)+":")):
						t.Errorf("differences[%d].Deployed = %q, want a %s integrity other than %q", i, d.Deployed, alg, recorded)
					case !w.deployed && d.Deployed != "":
						t.Errorf("differences[%d].Deployed = %q, want empty", i, d.Deployed)
					}
					switch {
					case w.source && (d.Source != srcIntegrity || d.Commit != tt.commit):
						t.Errorf("differences[%d].Source, Commit = %q, %q; want %q, %q", i, d.Source, d.Commit, srcIntegrity, tt.commit)
					case !w.source && (d.Source != "" || d.Commit != ""):
						t.Errorf("differences[%d].Source, Commit = %q, %q; want empty", i, d.Source, d.Commit)
					}
				}
			})
		}
	}
}

// TestRunSourceModeChanged changes only the execute bit in the source. The
// source is outdated; the destination is not modified. The record has the
// content, so this holds on Windows too, where the destination has no
// execute bit.
func TestRunSourceModeChanged(t *testing.T) {
	tests := []struct {
		name         string
		placed, now  []file
		diskModeLost bool // the destination lost the execute bit, as on Windows
	}{
		{name: "execute bit removed", placed: withExec, now: noExec},
		{name: "execute bit added", placed: noExec, now: withExec},
		{name: "execute bit removed, not kept at the destination", placed: withExec, now: noExec, diskModeLost: true},
		{name: "execute bit added, not kept at the destination", placed: noExec, now: withExec, diskModeLost: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			disk := tt.placed
			if tt.diskModeLost {
				disk = noExec
			}
			dest := writeDir(t, filepath.Join(root, "a"), disk...)
			placed := treeOf(t, gittree.SHA1, tt.placed...)
			src := &source.Source{Dep: "a", Tree: treeOf(t, gittree.SHA1, tt.now...)}
			plan := &deploy.Plan{ManifestDir: root, Items: []deploy.Item{{
				Action: deploy.Replace, Dep: "a", Path: dest, Dir: "a",
				Record: &state.Placed{Dep: "a", Dir: "a", Integrity: placed.Integrity(), Content: contentOf(t, placed)},
			}}}
			got, err := Run(plan, []*source.Source{src})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Kind != Outdated || got[0].Source != src.Integrity() {
				t.Errorf("differences = %+v, want one outdated", got)
			}
		})
	}
}

// TestRunSourceModeChangedWithoutContent is TestRunSourceModeChanged with
// a record written before the content was added: the modes on disk are
// tried, which works where the file system keeps the execute bit.
func TestRunSourceModeChangedWithoutContent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not keep the execute bit of the destination")
	}
	root := t.TempDir()
	dest := writeDir(t, filepath.Join(root, "a"), withExec...)
	recorded := treeOf(t, gittree.SHA1, withExec...).Integrity()
	src := &source.Source{Dep: "a", Tree: treeOf(t, gittree.SHA1, noExec...)}
	plan := &deploy.Plan{ManifestDir: root, Items: []deploy.Item{{
		Action: deploy.Replace, Dep: "a", Path: dest, Dir: "a",
		Record: &state.Placed{Dep: "a", Dir: "a", Integrity: recorded},
	}}}
	got, err := Run(plan, []*source.Source{src})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != Outdated {
		t.Errorf("differences = %+v, want one outdated", got)
	}
}

// TestRunOrder keeps the order of the plan.
func TestRunOrder(t *testing.T) {
	root := t.TempDir()
	plan := &deploy.Plan{ManifestDir: root, Items: []deploy.Item{
		{Action: deploy.Adopt, Dep: "b", Path: filepath.Join(root, "b"), Dir: "b"},
		{Action: deploy.Place, Dep: "a", Path: filepath.Join(root, "a"), Dir: "a"},
		{Action: deploy.Forget, Dep: "c", Path: filepath.Join(root, "c"), Dir: "c"},
		{Action: deploy.Remove, Dep: "d", Path: filepath.Join(root, "d"), Dir: "d"},
	}}
	got, err := Run(plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, d := range got {
		kinds = append(kinds, d.Item.Dep+" "+d.Kind.String())
	}
	if want := "b adoptable, a missing, d leftover"; strings.Join(kinds, ", ") != want {
		t.Errorf("differences = %q, want %q", strings.Join(kinds, ", "), want)
	}
}

// TestRunNotFetched is an error rather than a silent "up to date".
func TestRunNotFetched(t *testing.T) {
	root := t.TempDir()
	dest := writeDir(t, filepath.Join(root, "a"), v1...)
	plan := &deploy.Plan{ManifestDir: root, Items: []deploy.Item{{
		Action: deploy.Replace, Dep: "a", Path: dest, Dir: "a",
		Record: &state.Placed{Dep: "a", Dir: "a", Integrity: treeOf(t, gittree.SHA1, v1...).Integrity()},
	}}}
	_, err := Run(plan, nil)
	if err == nil || !strings.Contains(err.Error(), `dependency "a" was not fetched`) {
		t.Errorf("error = %v, want one about the missing source", err)
	}
}

func TestKindString(t *testing.T) {
	for k, want := range map[Kind]string{
		Missing: "missing", Modified: "modified", Outdated: "outdated",
		Leftover: "leftover", Adoptable: "adoptable", Kind(0): "Kind(0)",
	} {
		if got := k.String(); got != want {
			t.Errorf("Kind(%d).String() = %q, want %q", int(k), got, want)
		}
	}
}

// TestRunModifiedDeployed reports, for a record with the content, the
// integrity of the edited destination computed with the modes of the
// source, which can be compared with the integrity in the record.
func TestRunModifiedDeployed(t *testing.T) {
	root := t.TempDir()
	editedExec := []file{{path: "SKILL.md", data: "edited\n"}, {path: "run.sh", data: "#!/bin/sh\n", exec: true}}
	// The destination lost the execute bit, as on Windows.
	dest := writeDir(t, filepath.Join(root, "a"), file{path: "SKILL.md", data: "edited\n"}, file{path: "run.sh", data: "#!/bin/sh\n"})
	placed := treeOf(t, gittree.SHA1, withExec...)
	src := &source.Source{Dep: "a", Tree: placed}
	plan := &deploy.Plan{ManifestDir: root, Items: []deploy.Item{{
		Action: deploy.Replace, Dep: "a", Path: dest, Dir: "a",
		Record: &state.Placed{Dep: "a", Dir: "a", Integrity: placed.Integrity(), Content: contentOf(t, placed)},
	}}}
	got, err := Run(plan, []*source.Source{src})
	if err != nil {
		t.Fatal(err)
	}
	want := treeOf(t, gittree.SHA1, editedExec...).Integrity()
	if len(got) != 1 || got[0].Kind != Modified || got[0].Deployed != want {
		t.Errorf("differences = %+v, want one modified with Deployed %s", got, want)
	}
}
