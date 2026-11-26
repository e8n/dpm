package deploy

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e8n/dpm/internal/manifest"
	"github.com/e8n/dpm/internal/state"
)

const testIntegrity = "git-sha1:0123456789abcdef0123456789abcdef01234567"

// fakeDisk maps existing OS paths to an identity; paths with the same
// identity are the same file.
type fakeDisk map[string]string

func (d fakeDisk) Exists(p string) (bool, error) {
	_, ok := d[p]
	return ok, nil
}

func (d fakeDisk) SameFile(a, b string) (bool, error) {
	ia, oka := d[a]
	ib, okb := d[b]
	if !oka || !okb {
		return false, errors.New("not found")
	}
	return ia == ib, nil
}

// planEnv builds dependencies, records and disks relative to a manifest
// directory.
type planEnv struct {
	dir string
}

func newPlanEnv(t *testing.T) planEnv {
	return planEnv{dir: t.TempDir()}
}

// path returns the OS path of the slash path rel under the manifest dir.
func (e planEnv) path(rel string) string {
	return filepath.Join(e.dir, filepath.FromSlash(rel))
}

// dep returns a dependency deployed to each "to" directory (slash paths
// relative to the manifest dir).
func (e planEnv) dep(name string, toDirs ...string) manifest.Dep {
	d := manifest.Dep{Name: name, Path: "src/" + name}
	for _, to := range toDirs {
		d.To = append(d.To, manifest.Target{
			Raw:  "${x}/" + to,
			Dir:  e.path(to),
			Path: e.path(to + "/" + name),
		})
	}
	return d
}

func (e planEnv) record(dirs ...string) *state.State {
	st := &state.State{}
	for _, d := range dirs {
		st.Placed = append(st.Placed, state.Placed{Dep: d[strings.LastIndex(d, "/")+1:], Dir: d, Integrity: testIntegrity})
	}
	return st
}

// disk returns a fakeDisk where each slash path exists as a distinct file.
func (e planEnv) disk(rels ...string) fakeDisk {
	d := fakeDisk{}
	for _, r := range rels {
		d[e.path(r)] = r
	}
	return d
}

// wantItem is the expected form of an Item.
type wantItem struct {
	action   Action
	dep      string
	dir      string
	recorded bool
}

func summarize(p *Plan) []wantItem {
	var out []wantItem
	for _, it := range p.Items {
		out = append(out, wantItem{it.Action, it.Dep, it.Dir, it.Record != nil})
	}
	return out
}

func TestNewPlan(t *testing.T) {
	e := newPlanEnv(t)
	sk := ".claude/skills"
	tests := []struct {
		name  string
		deps  []manifest.Dep
		state *state.State
		disk  fakeDisk
		want  []wantItem
	}{
		{
			name: "missing directory is placed",
			deps: []manifest.Dep{e.dep("a", sk)},
			disk: e.disk(),
			want: []wantItem{{Place, "a", sk + "/a", false}},
		},
		{
			name:  "recorded directory that disappeared is placed again",
			deps:  []manifest.Dep{e.dep("a", sk)},
			state: e.record(sk + "/a"),
			disk:  e.disk(),
			want:  []wantItem{{Place, "a", sk + "/a", true}},
		},
		{
			name:  "recorded directory is replaced",
			deps:  []manifest.Dep{e.dep("a", sk)},
			state: e.record(sk + "/a"),
			disk:  e.disk(sk + "/a"),
			want:  []wantItem{{Replace, "a", sk + "/a", true}},
		},
		{
			name: "unrecorded directory named after the dependency is adopted",
			deps: []manifest.Dep{e.dep("a", sk)},
			disk: e.disk(sk + "/a"),
			want: []wantItem{{Adopt, "a", sk + "/a", false}},
		},
		{
			name:  "unrecorded directory with another name is not touched",
			deps:  []manifest.Dep{e.dep("a", sk)},
			state: e.record(),
			disk:  e.disk(sk+"/_local", sk+"/by-hand", sk+"/a"),
			want:  []wantItem{{Adopt, "a", sk + "/a", false}},
		},
		{
			name:  "recorded directory no longer in the manifest is removed",
			deps:  []manifest.Dep{e.dep("a", sk)},
			state: e.record(sk+"/a", sk+"/old"),
			disk:  e.disk(sk+"/a", sk+"/old"),
			want: []wantItem{
				{Replace, "a", sk + "/a", true},
				{Remove, "old", sk + "/old", true},
			},
		},
		{
			name:  "recorded directory no longer in the manifest and gone is forgotten",
			deps:  nil,
			state: e.record(sk + "/old"),
			disk:  e.disk(),
			want:  []wantItem{{Forget, "old", sk + "/old", true}},
		},
		{
			name:  "destination removed from to",
			deps:  []manifest.Dep{e.dep("a", ".agents/skills")},
			state: e.record(sk+"/a", ".agents/skills/a"),
			disk:  e.disk(sk+"/a", ".agents/skills/a"),
			want: []wantItem{
				{Replace, "a", ".agents/skills/a", true},
				{Remove, "a", sk + "/a", true},
			},
		},
		{
			name:  "order: dependencies and to entries, then removals by dir",
			deps:  []manifest.Dep{e.dep("b", sk, ".agents/skills"), e.dep("a", sk)},
			state: e.record(sk+"/z", sk+"/y", ".agents/skills/b"),
			disk:  e.disk(sk+"/z", sk+"/y", ".agents/skills/b"),
			want: []wantItem{
				{Place, "b", sk + "/b", false},
				{Replace, "b", ".agents/skills/b", true},
				{Place, "a", sk + "/a", false},
				{Remove, "y", sk + "/y", true},
				{Remove, "z", sk + "/z", true},
			},
		},
		{
			name:  "record inside a destination goes away with it",
			deps:  []manifest.Dep{e.dep("a", sk)},
			state: e.record(sk + "/a/inner/b"),
			disk:  e.disk(sk+"/a", sk+"/a/inner/b"),
			want: []wantItem{
				{Adopt, "a", sk + "/a", false},
				{Forget, "b", sk + "/a/inner/b", true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewPlan(e.dir, tt.deps, tt.state, tt.disk)
			if err != nil {
				t.Fatal(err)
			}
			got := summarize(p)
			if len(got) != len(tt.want) {
				t.Fatalf("items = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("item %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
			for _, it := range p.Items {
				if it.Action.Deploys() && it.Path != it.Target.Path {
					t.Errorf("%s: Path %s, want the target path %s", it.Dir, it.Path, it.Target.Path)
				}
				if !it.Action.Deploys() && it.Path != e.path(it.Dir) {
					t.Errorf("%s: Path %s, want %s", it.Dir, it.Path, e.path(it.Dir))
				}
			}
		})
	}
}

// TestNewPlanOtherSpelling covers a record that names the destination with
// another letter case, on a file system that ignores it: the destination is
// replaced, and the record is not removed (which would remove the new
// contents).
func TestNewPlanOtherSpelling(t *testing.T) {
	e := newPlanEnv(t)
	disk := fakeDisk{
		e.path(".claude/skills/commit"): "same",
		e.path(".claude/skills/Commit"): "same",
	}
	tests := []struct {
		name    string
		records []string
		want    []wantItem
	}{
		{
			name:    "only the other spelling is recorded",
			records: []string{".claude/skills/Commit"},
			want:    []wantItem{{Replace, "commit", ".claude/skills/commit", true}},
		},
		{
			name:    "both spellings are recorded",
			records: []string{".claude/skills/Commit", ".claude/skills/commit"},
			want: []wantItem{
				{Replace, "commit", ".claude/skills/commit", true},
				{Forget, "Commit", ".claude/skills/Commit", true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewPlan(e.dir, []manifest.Dep{e.dep("commit", ".claude/skills")}, e.record(tt.records...), disk)
			if err != nil {
				t.Fatal(err)
			}
			got := summarize(p)
			if len(got) != len(tt.want) {
				t.Fatalf("items = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("item %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
			if r := p.Items[0].Record; len(tt.records) > 1 && r.Dir != ".claude/skills/commit" {
				t.Errorf("the record with the same dir should be matched first, got %+v", r)
			}
		})
	}
}

func TestNewPlanErrors(t *testing.T) {
	e := newPlanEnv(t)
	sk := ".claude/skills"
	base := filepath.Base(e.dir)
	tests := []struct {
		name  string
		deps  []manifest.Dep
		state *state.State
		disk  fakeDisk
		want  string
	}{
		{
			name: "names that differ only in letter case",
			deps: []manifest.Dep{e.dep("Commit", sk), e.dep("commit", sk)},
			want: "same directory",
		},
		{
			name: "destination inside another",
			deps: []manifest.Dep{e.dep("skills", ".claude"), e.dep("commit", sk)},
			want: "inside",
		},
		{
			name:  "removing a directory that contains a destination",
			deps:  []manifest.Dep{e.dep("b", sk+"/a")},
			state: e.record(sk + "/a"),
			disk:  e.disk(sk + "/a"),
			want:  "refusing to remove",
		},
		{
			name:  "removing the manifest's directory",
			deps:  nil,
			state: e.record("../" + base),
			disk:  fakeDisk{e.dir: "m"},
			want:  "contains the manifest",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disk := tt.disk
			if disk == nil {
				disk = fakeDisk{}
			}
			_, err := NewPlan(e.dir, tt.deps, tt.state, disk)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestPlanFilter(t *testing.T) {
	p := &Plan{Items: []Item{{Action: Place, Dep: "a"}, {Action: Remove, Dep: "b"}, {Action: Adopt, Dep: "c"}}}
	got := p.Filter(Adopt, Place)
	if len(got) != 2 || got[0].Dep != "a" || got[1].Dep != "c" {
		t.Errorf("Filter = %+v", got)
	}
}

func TestOSDisk(t *testing.T) {
	dir := t.TempDir()
	e := planEnv{dir: dir}
	mkdirs(t, dir, "a")
	var d OSDisk
	if ok, err := d.Exists(e.path("a")); !ok || err != nil {
		t.Errorf("Exists(a) = %v, %v", ok, err)
	}
	if ok, err := d.Exists(e.path("missing")); ok || err != nil {
		t.Errorf("Exists(missing) = %v, %v", ok, err)
	}
	if ok, err := d.SameFile(e.path("a"), e.path("a/.")); !ok || err != nil {
		t.Errorf("SameFile(a, a/.) = %v, %v", ok, err)
	}
	mkdirs(t, dir, "b")
	if ok, err := d.SameFile(e.path("a"), e.path("b")); ok || err != nil {
		t.Errorf("SameFile(a, b) = %v, %v", ok, err)
	}
}
