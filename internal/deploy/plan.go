// Package deploy decides what to do with each destination directory and
// carries it out (spec/2026/11/20261117-01 3.6).
//
// The work is split in three steps so that each can be tested alone and so
// that check can reuse the decision without changing anything:
//
//  1. NewPlan (plan.go) decides the action for every destination from the
//     manifest, the record of the previous deployment and what exists on
//     disk. It changes nothing.
//  2. Stage (stage.go) writes the new contents into staging directories
//     next to the destinations. A failure here leaves the destinations as
//     they were.
//  3. Apply (apply.go) swaps the directories with renames, records the
//     result, and rolls the swap back when any step fails.
package deploy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/e8n/dpm/internal/fsutil"
	"github.com/e8n/dpm/internal/manifest"
	"github.com/e8n/dpm/internal/state"
)

// Action is what is done with one destination directory.
type Action int

// The actions of the table in spec 3.6. A directory that is neither
// recorded nor named after a dependency (a local "_*" directory or one
// placed by hand) gets no item at all: dpm does not touch it.
const (
	// Place deploys a dependency where nothing exists yet.
	Place Action = iota + 1
	// Replace overwrites a directory that dpm placed before, even when it
	// was edited by hand.
	Replace
	// Adopt overwrites a directory that is not recorded but is named after
	// the dependency (left by APM, by an install in another environment,
	// or committed to the repository).
	Adopt
	// Remove deletes a recorded directory that is no longer in the
	// manifest.
	Remove
	// Forget drops a record whose directory no longer needs dpm's action:
	// it is gone already, or it is another spelling of a destination that
	// is handled by another item.
	Forget
)

var actionNames = map[Action]string{
	Place:   "place",
	Replace: "replace",
	Adopt:   "adopt",
	Remove:  "remove",
	Forget:  "forget",
}

// String returns the lowercase English name of the action.
func (a Action) String() string {
	if s, ok := actionNames[a]; ok {
		return s
	}
	return fmt.Sprintf("Action(%d)", int(a))
}

// Deploys reports whether the action writes the dependency's contents.
func (a Action) Deploys() bool {
	return a == Place || a == Replace || a == Adopt
}

// Item is the action for one directory.
type Item struct {
	Action Action
	// Dep is the name of the dependency. For Remove and Forget it is the
	// name in the record.
	Dep string
	// Target is the destination in the manifest. It is the zero value for
	// Remove and Forget.
	Target manifest.Target
	// Path is the directory as an absolute OS path.
	Path string
	// Dir is the directory in the form recorded in .dpm/state.toml.
	Dir string
	// Record is the entry of the previous deployment for this directory,
	// or nil when it is not recorded. For Place it is non-nil when a
	// recorded directory has disappeared; for Replace it gives the
	// integrity that was placed, to compare with the disk and the source.
	Record *state.Placed
}

// Plan lists the actions in a fixed order: the destinations in the order
// of the dependencies and of their "to" entries, then the records that are
// no longer in the manifest, sorted by Dir.
type Plan struct {
	// ManifestDir is the directory that recorded paths are relative to.
	ManifestDir string
	Items       []Item
}

// Filter returns the items with one of the given actions, in plan order.
func (p *Plan) Filter(actions ...Action) []Item {
	var out []Item
	for _, it := range p.Items {
		for _, a := range actions {
			if it.Action == a {
				out = append(out, it)
				break
			}
		}
	}
	return out
}

// Disk is what the plan needs to know about the file system. OSDisk is the
// real one; tests can use a fake.
type Disk interface {
	// Exists reports whether anything exists at the OS path p. Symbolic
	// links are not followed: a link counts as existing.
	Exists(p string) (bool, error)
	// SameFile reports whether the existing paths a and b are the same
	// file, such as two spellings of one directory on a file system that
	// ignores letter case.
	SameFile(a, b string) (bool, error)
}

// OSDisk is the Disk of the running system.
type OSDisk struct{}

// Exists implements Disk.
func (OSDisk) Exists(p string) (bool, error) {
	_, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// SameFile implements Disk.
func (OSDisk) SameFile(a, b string) (bool, error) {
	ia, err := os.Lstat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Lstat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ia, ib), nil
}

// NewPlan decides the action for every destination of deps and every
// directory in the record st (spec 3.6):
//
//   - a destination that does not exist is placed;
//   - a destination that exists and is recorded is replaced;
//   - a destination that exists but is not recorded is adopted (its name
//     is the dependency's name, since that is how destinations are named);
//   - a recorded directory that is not a destination any more is removed,
//     or forgotten when it no longer exists.
//
// A record matches a destination when Dir is the same string, or when both
// exist and are the same file on disk (another spelling on a file system
// that ignores letter case), so that such a directory is not removed right
// after it is replaced.
//
// It is an error when two destinations are the same directory or one is
// inside the other, compared ignoring letter case on every OS (for example
// [deps.Commit] and [deps.commit] in the same "to"): such a manifest would
// deploy twice to one directory on Windows and macOS. It is also an error
// when a directory to remove contains a destination or the manifest's
// directory.
func NewPlan(manifestDir string, deps []manifest.Dep, st *state.State, disk Disk) (*Plan, error) {
	p := &Plan{ManifestDir: manifestDir}
	for _, d := range deps {
		for _, t := range d.To {
			p.Items = append(p.Items, Item{
				Dep:    d.Name,
				Target: t,
				Path:   t.Path,
				Dir:    state.RelDir(manifestDir, t.Path),
			})
		}
	}
	if err := checkTargets(p.Items); err != nil {
		return nil, err
	}
	targets := len(p.Items)

	exists := make([]bool, targets)
	for i := range p.Items {
		ok, err := disk.Exists(p.Items[i].Path)
		if err != nil {
			return nil, err
		}
		exists[i] = ok
	}

	var records []state.Placed
	if st != nil {
		records = st.Placed
	}
	// Records with the same Dir as a destination are matched first, so
	// that the result does not depend on the order of the records.
	byDir := make(map[string]int, targets)
	for i, it := range p.Items[:targets] {
		byDir[it.Dir] = i
	}
	var unmatched []state.Placed
	for _, rec := range records {
		if i, ok := byDir[rec.Dir]; ok && p.Items[i].Record == nil {
			p.Items[i].Record = &rec
			continue
		}
		unmatched = append(unmatched, rec)
	}
	var rest []Item
	for _, rec := range unmatched {
		it := Item{Dep: rec.Dep, Path: rec.Path(manifestDir), Dir: rec.Dir, Record: &rec}
		ok, err := disk.Exists(it.Path)
		if err != nil {
			return nil, err
		}
		if !ok {
			it.Action = Forget
			rest = append(rest, it)
			continue
		}
		i, err := sameTarget(disk, p.Items[:targets], exists, it.Path)
		if err != nil {
			return nil, err
		}
		switch {
		case i >= 0 && p.Items[i].Record == nil:
			p.Items[i].Record = &rec
			continue
		case i >= 0:
			it.Action = Forget
		case within(it.Path, p.Items[:targets]):
			// The directory goes away with the destination that contains
			// it, which exists and is replaced or adopted.
			it.Action = Forget
		default:
			if err := checkRemove(manifestDir, p.Items[:targets], it); err != nil {
				return nil, err
			}
			it.Action = Remove
		}
		rest = append(rest, it)
	}

	for i := range p.Items {
		switch {
		case !exists[i]:
			p.Items[i].Action = Place
		case p.Items[i].Record != nil:
			p.Items[i].Action = Replace
		default:
			p.Items[i].Action = Adopt
		}
	}
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].Dir < rest[j].Dir })
	p.Items = append(p.Items, rest...)
	return p, nil
}

// sameTarget returns the index of the existing destination that is the same
// file as path, or -1.
func sameTarget(disk Disk, targets []Item, exists []bool, path string) (int, error) {
	for i, t := range targets {
		if !exists[i] {
			continue
		}
		same, err := disk.SameFile(t.Path, path)
		if err != nil {
			return -1, err
		}
		if same {
			return i, nil
		}
	}
	return -1, nil
}

// checkTargets rejects destinations that are the same directory or nested.
func checkTargets(items []Item) error {
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			a, b := items[i], items[j]
			switch {
			case samePathFold(a.Path, b.Path):
				return fmt.Errorf("dependencies %q and %q are deployed to the same directory %s (names are compared ignoring letter case)", a.Dep, b.Dep, a.Path)
			case inside(b.Path, a.Path):
				return fmt.Errorf("dependency %q is deployed to %s, inside %s of dependency %q", b.Dep, b.Path, a.Path, a.Dep)
			case inside(a.Path, b.Path):
				return fmt.Errorf("dependency %q is deployed to %s, inside %s of dependency %q", a.Dep, a.Path, b.Path, b.Dep)
			}
		}
	}
	return nil
}

// checkRemove rejects removing a recorded directory that contains a
// destination or the manifest's directory. A record of dpm never does; this
// guards against a record edited by hand.
func checkRemove(manifestDir string, targets []Item, it Item) error {
	if samePathFold(it.Path, manifestDir) || inside(manifestDir, it.Path) {
		return fmt.Errorf("the recorded directory %s contains the manifest; refusing to remove it (check %s)", it.Path, state.File(manifestDir))
	}
	for _, t := range targets {
		if inside(t.Path, it.Path) {
			return fmt.Errorf("the recorded directory %s of %q contains %s of dependency %q; refusing to remove it", it.Path, it.Dep, t.Path, t.Dep)
		}
	}
	return nil
}

// within reports whether path is inside one of the destinations.
func within(path string, targets []Item) bool {
	for _, t := range targets {
		if inside(path, t.Path) {
			return true
		}
	}
	return false
}

// foldSlash normalizes an OS path for comparisons that ignore letter case.
func foldSlash(p string) string {
	return strings.ToLower(fsutil.ToSlash(p))
}

// samePathFold reports whether a and b are the same path ignoring letter
// case.
func samePathFold(a, b string) bool {
	return foldSlash(a) == foldSlash(b)
}

// inside reports whether path is strictly inside dir, ignoring letter case.
func inside(path, dir string) bool {
	d := foldSlash(dir)
	if !strings.HasSuffix(d, "/") {
		d += "/"
	}
	return strings.HasPrefix(foldSlash(path), d)
}

// stagingRoot returns the staging directory for the destination path: a
// ".dpm-staging" directory next to the "to" directory, so that it is on the
// same file system and is not read as a skill (spec 3.6, 3.9).
func stagingRoot(path string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(path)), StagingName)
}
