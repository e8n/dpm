package deploy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/e8n/dpm/internal/fsutil"
	"github.com/e8n/dpm/internal/source"
	"github.com/e8n/dpm/internal/state"
)

// move is one rename done by Apply, undone by renaming back.
type move struct {
	from, to string
}

// Apply swaps the staged contents into the destinations and records the
// result in .dpm/state.toml (spec 3.6, step 2):
//
//   - Replace and Adopt move the destination into the staging directory,
//     then move the new contents in;
//   - Place moves the new contents in, creating the "to" directory when
//     needed;
//   - Remove moves the directory into the staging directory;
//   - Forget does nothing on disk.
//
// Every rename goes through fsutil.Rename. When a rename or the write of
// the record fails, the renames done so far are undone in reverse order and
// the staging directories are removed, so that the destinations are as
// before ("to" directories created for Place may stay, empty). If undoing
// fails too, the staging directories are kept, since they hold the previous
// contents, and the error says where they are.
//
// After a successful swap and record, the staging directories, with the
// previous contents in them, are removed. A failure there is returned as a
// warning: the install is complete and the next install removes what is
// left (see Stage).
func (s *Staging) Apply() (warnings []string, err error) {
	var done []move
	rename := func(from, to string) error {
		if err := fsutil.Rename(from, to); err != nil {
			return err
		}
		done = append(done, move{from, to})
		return nil
	}
	err = s.swap(rename)
	if err == nil {
		var st *state.State
		if st, err = s.State(); err == nil {
			err = state.Save(s.plan.ManifestDir, st)
		}
	}
	if err != nil {
		if rerr := rollback(done); rerr != nil {
			return nil, fmt.Errorf("%w; restoring the previous contents also failed: %v (the previous contents are kept in %v; move them back by hand before running install again, which removes these directories)", err, rerr, s.roots)
		}
		if derr := s.Discard(); derr != nil {
			warnings = append(warnings, fmt.Sprintf("cannot remove the staging directory: %v", derr))
		}
		return warnings, fmt.Errorf("%w; the destinations were restored", err)
	}
	for _, root := range s.roots {
		if rerr := os.RemoveAll(root); rerr != nil {
			warnings = append(warnings, fmt.Sprintf("cannot remove %s: %v (the next install removes it)", root, rerr))
		}
	}
	return warnings, nil
}

func (s *Staging) swap(rename func(from, to string) error) error {
	for _, root := range s.roots {
		if err := os.MkdirAll(filepath.Join(root, stagingOld), 0o755); err != nil {
			return err
		}
	}
	for i, it := range s.plan.Items {
		switch it.Action {
		case Replace, Adopt:
			if err := rename(it.Path, s.oldDir(i)); err != nil {
				return fmt.Errorf("cannot move %s away: %w", it.Path, err)
			}
			if err := rename(s.newDir(i), it.Path); err != nil {
				return fmt.Errorf("cannot place %s: %w", it.Path, err)
			}
		case Place:
			if err := os.MkdirAll(filepath.Dir(it.Path), 0o755); err != nil {
				return err
			}
			if err := rename(s.newDir(i), it.Path); err != nil {
				return fmt.Errorf("cannot place %s: %w", it.Path, err)
			}
		case Remove:
			if err := rename(it.Path, s.oldDir(i)); err != nil {
				return fmt.Errorf("cannot remove %s: %w", it.Path, err)
			}
		}
	}
	return nil
}

// rollback undoes the moves in reverse order. It tries every move even
// after a failure, so that as much as possible is restored.
func rollback(done []move) error {
	var errs []error
	for i := len(done) - 1; i >= 0; i-- {
		m := done[i]
		if err := fsutil.Rename(m.to, m.from); err != nil {
			errs = append(errs, fmt.Errorf("cannot move %s back to %s: %w", m.to, m.from, err))
		}
	}
	return errors.Join(errs...)
}

// State returns the record after the plan is applied: every Place,
// Replace and Adopt item with the integrity and the content of its source.
// Remove and Forget items are dropped.
func (s *Staging) State() (*state.State, error) {
	st := &state.State{}
	for _, it := range s.plan.Items {
		if !it.Action.Deploys() {
			continue
		}
		src := s.sources[it.Dep]
		content, err := src.Tree.Content()
		if err != nil {
			return nil, fmt.Errorf("dependency %q: %w", it.Dep, err)
		}
		st.Placed = append(st.Placed, state.Placed{
			Dep:       it.Dep,
			Dir:       it.Dir,
			Integrity: src.Integrity(),
			Content:   content,
		})
	}
	return st, nil
}

// Install stages and applies p in one call. See Stage and Apply.
func Install(p *Plan, sources []*source.Source) (warnings []string, err error) {
	s, err := Stage(p, sources)
	if err != nil {
		return nil, err
	}
	return s.Apply()
}
