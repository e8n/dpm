// Package check compares the deployed directories with the manifest, the
// record of the previous deployment and the current contents of the sources
// (spec/2026/11/20261117-01 3.7). It changes nothing: it reads the plan that
// install would carry out (package deploy), the fetched sources (package
// source) and the destinations on disk.
package check

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/e8n/dpm/internal/deploy"
	"github.com/e8n/dpm/internal/gittree"
	"github.com/e8n/dpm/internal/source"
	"github.com/e8n/dpm/internal/state"
)

// Kind is one of the differences that check reports.
type Kind int

// The five differences of spec 3.7.
const (
	// Missing: a destination of the manifest does not exist (install
	// places it). This includes a recorded directory that has disappeared.
	Missing Kind = iota + 1
	// Modified: the contents of a recorded destination differ from the
	// record (it was edited by hand). The execute bit is ignored (spec
	// 3.5).
	Modified
	// Outdated: the current contents of the source differ from the
	// integrity in the record (install updates the destination).
	Outdated
	// Leftover: a recorded directory that is no longer in the manifest
	// still exists (install removes it).
	Leftover
	// Adoptable: a destination exists but is not recorded; it is named
	// after the dependency (install adopts and overwrites it).
	Adoptable
)

var kindNames = map[Kind]string{
	Missing:   "missing",
	Modified:  "modified",
	Outdated:  "outdated",
	Leftover:  "leftover",
	Adoptable: "adoptable",
}

// String returns the lowercase English name of the kind.
func (k Kind) String() string {
	if s, ok := kindNames[k]; ok {
		return s
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// Difference is one difference found by Run.
type Difference struct {
	Kind Kind
	// Item is the planned action for the directory. Its Dep, Dir and Path
	// name the directory, and its Record is the entry of the previous
	// deployment, or nil when it is not recorded.
	Item deploy.Item
	// Deployed is the integrity computed from the destination with the
	// algorithm of the record. Set for Modified only.
	Deployed string
	// Source is the current integrity of the source, and Commit the commit
	// that the ref of a Git dependency resolved to. Set for Outdated only.
	Source string
	Commit string
}

// Run returns the differences between the plan and what install would
// deploy, in plan order. sources are the fetched dependencies of the
// manifest that the plan was made from.
//
// A directory that matches more than one kind gets one difference for each
// (a recorded destination can be both Modified and Outdated). Records that
// the plan only forgets (their directory is already gone, or it is handled
// by another item) are not differences: install changes nothing on disk
// for them.
func Run(p *deploy.Plan, sources []*source.Source) ([]Difference, error) {
	byDep := make(map[string]*source.Source, len(sources))
	for _, s := range sources {
		byDep[s.Dep] = s
	}
	var out []Difference
	for _, it := range p.Items {
		switch it.Action {
		case deploy.Place:
			out = append(out, Difference{Kind: Missing, Item: it})
		case deploy.Adopt:
			out = append(out, Difference{Kind: Adoptable, Item: it})
		case deploy.Remove:
			out = append(out, Difference{Kind: Leftover, Item: it})
		case deploy.Replace:
			src := byDep[it.Dep]
			if src == nil {
				return nil, fmt.Errorf("dependency %q was not fetched", it.Dep)
			}
			deployed, same, err := compareDeployed(it.Path, *it.Record, src.Tree.Entries)
			if err != nil {
				return nil, fmt.Errorf("dependency %q: %w", it.Dep, err)
			}
			if !same {
				out = append(out, Difference{Kind: Modified, Item: it, Deployed: deployed})
			}
			if s := src.Integrity(); s != it.Record.Integrity {
				out = append(out, Difference{Kind: Outdated, Item: it, Source: s, Commit: src.Commit})
			}
		}
	}
	return out, nil
}

// compareDeployed reports whether the directory at path has the contents
// of the record, ignoring the execute bit, which Windows does not keep
// (spec 3.5). When they differ, it also returns the integrity of the
// directory computed with the algorithm of the record.
//
// With the content in the record, the directory is hashed with every
// regular file as 100644 (gittree.IgnoreExecBit) and compared with it.
// The returned integrity takes the mode of each file from the source's
// current entries (gittree.ModesFrom), so that it can be compared with the
// integrity in the record and with the source's.
//
// A record without the content was written before it was added. The
// directory is then compared with the integrity, with the mode of each
// file taken from the source's current entries and, when that does not
// match, from the disk: the modes on disk are the placed modes (where the
// file system keeps them) when only the mode changed in the source, which
// is then reported as Outdated and not as Modified. A destination whose
// contents are as placed is still reported as Modified when the mode
// changed in the source and the file system does not keep the execute bit
// (Windows). A match needs the same contents as recorded, so no edit of
// the contents is missed.
//
// A destination that is neither a directory nor a link to one never
// matches.
func compareDeployed(path string, rec state.Placed, entries []gittree.Entry) (string, bool, error) {
	a, _, err := gittree.ParseIntegrity(rec.Integrity)
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		// A symbolic link whose target is gone.
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() {
		return "", false, nil
	}
	sourceModes := gittree.ModesFrom(entries)
	if rec.Content != "" {
		t, err := gittree.DirTree(a, path, gittree.IgnoreExecBit)
		if err != nil {
			return "", false, err
		}
		if t.Integrity() == rec.Content {
			return rec.Integrity, true, nil
		}
		t, err = gittree.DirTree(a, path, sourceModes)
		if err != nil {
			return "", false, err
		}
		return t.Integrity(), false, nil
	}
	var first string
	for _, modes := range []gittree.FileModeFunc{sourceModes, gittree.ModeFromDisk} {
		t, err := gittree.DirTree(a, path, modes)
		if err != nil {
			return "", false, err
		}
		if t.Integrity() == rec.Integrity {
			return rec.Integrity, true, nil
		}
		if first == "" {
			first = t.Integrity()
		}
	}
	return first, false, nil
}
