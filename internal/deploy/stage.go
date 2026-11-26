package deploy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/e8n/dpm/internal/source"
)

// StagingName is the name of the staging directory, created next to each
// "to" directory (".claude/.dpm-staging" for ".claude/skills").
const StagingName = ".dpm-staging"

// Layout of a staging directory:
//
//	.dpm-staging/
//	  .gitignore    "*", so that Git ignores whatever is left after a crash
//	  new/<n>       the new contents of item n of the plan
//	  old/<n>       the previous contents of item n, moved out by Apply
//
// Items are numbered by their index in the plan, which keeps the names
// unique even when several "to" directories share a parent.
const (
	stagingNew = "new"
	stagingOld = "old"
)

// Staging holds the new contents of every destination of a plan, written
// next to the destinations and ready to be swapped in by Apply.
type Staging struct {
	plan    *Plan
	sources map[string]*source.Source
	// roots lists the staging directories, one per distinct parent of the
	// "to" directories involved.
	roots []string
	// rootOf is the staging directory of each item that is moved (nil
	// entries are not used).
	rootOf []string
}

// Stage writes the contents of every Place, Replace and Adopt item of p
// into staging directories. sources must hold a fetched source for each of
// those dependencies. When Stage fails, it removes what it created and the
// destinations are left as they were (spec 3.6, step 1).
//
// A staging directory left by an earlier run that failed is removed before
// it is used. Only dpm writes there, and what it may hold (the new contents,
// or the previous contents of directories that dpm owns or adopts) is
// overwritten by this install anyway.
func Stage(p *Plan, sources []*source.Source) (*Staging, error) {
	s := &Staging{
		plan:    p,
		sources: make(map[string]*source.Source, len(sources)),
		rootOf:  make([]string, len(p.Items)),
	}
	for _, src := range sources {
		s.sources[src.Dep] = src
	}
	seen := make(map[string]bool)
	for i, it := range p.Items {
		if !it.Action.Deploys() && it.Action != Remove {
			continue
		}
		if it.Action.Deploys() && s.sources[it.Dep] == nil {
			return nil, fmt.Errorf("dependency %q was not fetched", it.Dep)
		}
		root := stagingRoot(it.Path)
		for _, other := range p.Items {
			if other.Action != Forget && (samePathFold(root, other.Path) || inside(root, other.Path) || inside(other.Path, root)) {
				return nil, fmt.Errorf("the staging directory %s overlaps %s of dependency %q", root, other.Path, other.Dep)
			}
		}
		s.rootOf[i] = root
		if !seen[root] {
			seen[root] = true
			s.roots = append(s.roots, root)
		}
	}

	if err := s.stage(); err != nil {
		if cerr := s.Discard(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return nil, err
	}
	return s, nil
}

func (s *Staging) stage() error {
	// Prepare every root before writing, so that two spellings of one
	// directory on a file system that ignores letter case do not remove
	// what the other has written.
	for _, root := range s.roots {
		if err := os.RemoveAll(root); err != nil {
			return fmt.Errorf("cannot remove the staging directory left by an earlier run: %w", err)
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, gitignoreName), []byte("*\n"), 0o644); err != nil {
			return err
		}
	}
	for i, it := range s.plan.Items {
		if !it.Action.Deploys() {
			continue
		}
		if err := s.sources[it.Dep].WriteTo(s.newDir(i)); err != nil {
			return fmt.Errorf("dependency %q: cannot prepare %s: %w", it.Dep, it.Path, err)
		}
	}
	return nil
}

const gitignoreName = ".gitignore"

// newDir returns where the new contents of item i are staged.
func (s *Staging) newDir(i int) string {
	return filepath.Join(s.rootOf[i], stagingNew, strconv.Itoa(i))
}

// oldDir returns where the previous contents of item i are moved.
func (s *Staging) oldDir(i int) string {
	return filepath.Join(s.rootOf[i], stagingOld, strconv.Itoa(i))
}

// Discard removes the staging directories without changing the
// destinations. Call it when the plan is not applied.
func (s *Staging) Discard() error {
	var errs []error
	for _, root := range s.roots {
		if err := os.RemoveAll(root); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
