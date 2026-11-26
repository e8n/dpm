package source

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/e8n/dpm/internal/fsutil"
	"github.com/e8n/dpm/internal/gittree"
	"github.com/e8n/dpm/internal/manifest"
)

// nonGitAlgorithm is the object format used for a directory that is not in
// a Git work tree.
const nonGitAlgorithm = gittree.SHA1

// workTreeDir is a path dependency inside a Git work tree.
type workTreeDir struct {
	index  int    // index of the dependency
	top    string // top level directory of the work tree
	prefix string // the directory relative to top, as a slash path
}

// fetchPaths fetches the path dependencies deps[i] for i in idx into
// out[i].
//
// A directory inside a Git work tree gets the tree that git would record
// for it (gittree.WorkTree), and its files are the blobs that git stores,
// read from the repository. The directories of one work tree are hashed
// together. A directory outside any work tree is hashed from the bytes on
// disk, and a warning says so (spec/2026/11/20261117-01 3.5).
func fetchPaths(deps []manifest.Dep, idx []int, out []*Source) error {
	var inRepo []workTreeDir
	for _, i := range idx {
		d := deps[i]
		dir, err := realDir(d.PathAbs)
		if err != nil {
			return depError(d.Name, err)
		}
		top, managed, err := findWorkTree(dir)
		if err != nil {
			return depError(d.Name, err)
		}
		if !managed {
			s, err := fetchPlainDir(d.Name, dir)
			if err != nil {
				return depError(d.Name, err)
			}
			out[i] = s
			continue
		}
		rel, err := filepath.Rel(top, dir)
		if err != nil {
			return depError(d.Name, err)
		}
		prefix := filepath.ToSlash(rel)
		if prefix == "." {
			prefix = ""
		}
		inRepo = append(inRepo, workTreeDir{index: i, top: top, prefix: prefix})
	}

	// Group by work tree, keeping the order in which they appear.
	var tops []string
	groups := map[string][]workTreeDir{}
	for _, w := range inRepo {
		if _, ok := groups[w.top]; !ok {
			tops = append(tops, w.top)
		}
		groups[w.top] = append(groups[w.top], w)
	}
	for _, top := range tops {
		if err := fetchWorkTree(deps, groups[top], out); err != nil {
			return err
		}
	}
	return nil
}

// realDir returns p with symbolic links resolved (on Windows, also with
// the letter case of the names on disk), after checking that it is a
// directory.
func realDir(p string) (string, error) {
	dir, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("path: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path: %s is not a directory", p)
	}
	return dir, nil
}

// findWorkTree returns the top level directory of the Git work tree that
// dir is in: the nearest of dir and its parents that has a ".git" entry (a
// directory, or a file for a linked work tree or a submodule). managed is
// false when there is none.
//
// This is decided here, not from the result of git: git reports "not a git
// repository" with the same exit status as other fatal errors (such as a
// repository with dubious ownership), in a translated message. A directory
// that has a ".git" but that git cannot read is an error (from
// checkTopLevel), not a directory outside Git.
//
// The path of dir relative to the top level is spelled as dir is (dir
// comes from realDir: on Windows the spelling of the names on disk, on
// other systems the spelling given). When git records a directory with
// a different letter case (renamed outside git on a case-insensitive file
// system), the directory matches nothing in git and fetchWorkTree reports
// that it has no files.
func findWorkTree(dir string) (top string, managed bool, err error) {
	for {
		_, err := os.Lstat(filepath.Join(dir, ".git"))
		if err == nil {
			return dir, true, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", false, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false, nil
		}
		dir = parent
	}
}

// checkTopLevel checks with one git command that git uses top as the top
// level directory of its work tree (it would not, for example, with
// core.worktree set, or across a file system boundary).
func checkTopLevel(top string) error {
	out, err := gittree.Git{Dir: top}.Run("rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	got := filepath.FromSlash(strings.TrimRight(string(out), "\r\n"))
	if !fsutil.SamePath(got, top) && !sameFile(got, top) {
		return fmt.Errorf("git uses %s as the top level directory of the work tree that has %s, which dpm does not support", got, filepath.Join(top, ".git"))
	}
	return nil
}

// sameFile reports whether a and b are the same directory, which they can
// be with different spellings (letter case on a case-insensitive file
// system, or another route to the same directory).
func sameFile(a, b string) bool {
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ia, ib)
}

// fetchWorkTree fetches path dependencies in the same Git work tree.
func fetchWorkTree(deps []manifest.Dep, dirs []workTreeDir, out []*Source) error {
	top := dirs[0].top
	subdirs := make([]string, len(dirs))
	names := make([]string, len(dirs))
	for i, w := range dirs {
		subdirs[i] = w.prefix
		names[i] = fmt.Sprintf("%q", deps[w.index].Name)
	}
	label := "dependency " + names[0]
	if len(names) > 1 {
		label = "dependencies " + strings.Join(names, ", ")
	}
	if err := checkTopLevel(top); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	trees, err := gittree.WorkTree(top, subdirs)
	if err != nil {
		return fmt.Errorf("%s in %s: %w", label, top, err)
	}
	var hashes []string
	for i, w := range dirs {
		d := deps[w.index]
		if len(trees[i].Entries) == 0 {
			return depError(d.Name, fmt.Errorf("%s has no files that Git would record (it is empty, only has ignored files, or is spelled with a different letter case than in Git)", d.PathAbs))
		}
		if err := checkTree(trees[i], d.PathAbs); err != nil {
			return depError(d.Name, err)
		}
		hashes = append(hashes, entryHashes(trees[i].Entries)...)
	}
	blobs, err := readBlobs(gittree.Git{Dir: top}, hashes)
	if err != nil {
		return fmt.Errorf("reading files from the repository %s: %w", top, err)
	}
	for i, w := range dirs {
		out[w.index] = &Source{Dep: deps[w.index].Name, Tree: trees[i], blobs: blobs}
	}
	return nil
}

// fetchPlainDir fetches a directory that is not in a Git work tree.
func fetchPlainDir(name, dir string) (*Source, error) {
	tree, err := gittree.DirTree(nonGitAlgorithm, dir, gittree.ModeFromDisk)
	if err != nil {
		return nil, err
	}
	if err := checkTree(tree, dir); err != nil {
		return nil, err
	}
	for _, e := range tree.Entries {
		for _, elem := range strings.Split(e.Path, "/") {
			if strings.EqualFold(elem, ".git") {
				return nil, fmt.Errorf("%s: %s is in a Git repository (.git), which dpm cannot deploy", dir, e.Path)
			}
		}
	}
	blobs, err := readDirBlobs(dir, tree)
	if err != nil {
		return nil, err
	}
	return &Source{
		Dep:   name,
		Tree:  tree,
		blobs: blobs,
		Warnings: []string{
			fmt.Sprintf("%s is not in a Git work tree: its integrity is computed from the bytes on disk, without Git's line ending normalization", dir),
		},
	}, nil
}

// readDirBlobs reads the contents of the entries of tree from dir. A file
// that no longer matches its hash (changed after the tree was computed) is
// an error, so that the deployed bytes always match the integrity.
func readDirBlobs(dir string, tree gittree.Tree) (map[string][]byte, error) {
	blobs := make(map[string][]byte, len(tree.Entries))
	for _, e := range tree.Entries {
		p := filepath.Join(dir, filepath.FromSlash(e.Path))
		var data []byte
		if e.Mode == gittree.ModeSymlink {
			target, err := os.Readlink(p)
			if err != nil {
				return nil, err
			}
			data = []byte(filepath.ToSlash(target))
		} else {
			var err error
			data, err = os.ReadFile(p)
			if err != nil {
				return nil, err
			}
		}
		if gittree.BlobHash(tree.Algorithm, data) != e.Hash {
			return nil, fmt.Errorf("%s changed while it was read; run the command again", p)
		}
		blobs[e.Hash] = data
	}
	return blobs, nil
}
