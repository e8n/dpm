package gittree

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrNotFound is returned (wrapped) by ReadTrees when a directory does not
// exist in the tree.
var ErrNotFound = errors.New("not found")

// literalPathspecs makes git treat every pathspec as a literal path, so that
// a directory name with "*" or ":" is not interpreted as a pattern or magic.
const literalPathspecs = "GIT_LITERAL_PATHSPECS=1"

// WorkTree computes the trees of directories in the Git work tree whose top
// level directory is top. Each subdir is a slash path relative to top (""
// for the top level) of an existing directory, spelled as git records it
// (for example the output of "git rev-parse --show-prefix" in the
// directory): on a case-insensitive file system a different spelling still
// exists on disk but matches nothing in git, and gives the empty tree. The
// results are in the order of subdirs.
//
// The result is the tree that "git add -A" followed by a commit would
// record for the directory, with the line ending normalization and
// .gitattributes of the repository applied (spec/2026/11/20261117-01 3.5):
//
//   - When none of the directories has changes against HEAD (modified,
//     deleted or untracked files; ignored files do not count), the trees
//     recorded in HEAD are used as they are.
//   - Otherwise a temporary index (GIT_INDEX_FILE) is filled with HEAD, the
//     directories are added with "git add -A", and the trees are read from
//     the result of "git write-tree". The repository's own index is not
//     touched. "git add" writes the blobs of changed files to the
//     repository's object database as loose objects. This cannot be avoided
//     because "git write-tree" needs the objects; they are unreachable and
//     are removed by git's garbage collection.
//
// A directory that has no files that git would record (for example one that
// is empty, only has ignored files or is ignored itself) gives the empty
// tree.
//
// All directories are handled with a fixed number of git commands, not one
// set of commands per directory.
func WorkTree(top string, subdirs []string) ([]Tree, error) {
	if len(subdirs) == 0 {
		return nil, nil
	}
	for _, s := range subdirs {
		if !validSubdir(s) {
			return nil, fmt.Errorf("invalid directory %q in the repository", s)
		}
		p := filepath.Join(top, filepath.FromSlash(s))
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", p)
		}
	}

	g := Git{Dir: top, Env: []string{literalPathspecs}}
	a, err := objectFormat(g)
	if err != nil {
		return nil, err
	}
	head, err := headCommit(g)
	if err != nil {
		return nil, err
	}
	pathspecs := toPathspecs(subdirs)

	treeish := head
	changed := head == ""
	if !changed {
		changed, err = hasChanges(g, pathspecs)
		if err != nil {
			return nil, err
		}
	}
	if changed {
		treeish, err = writeTempTree(g, head != "", pathspecs)
		if err != nil {
			return nil, err
		}
	}
	return readTrees(g, a, treeish, subdirs, true)
}

// validSubdir reports whether s is "" or a clean relative slash path.
func validSubdir(s string) bool {
	return s == "" || (validEntryPath(s) && !strings.ContainsAny(s, "\n\r"))
}

// toPathspecs returns the pathspecs that select subdirs ("." for the top
// level).
func toPathspecs(subdirs []string) []string {
	out := make([]string, len(subdirs))
	for i, s := range subdirs {
		if s == "" {
			s = "."
		}
		out[i] = s
	}
	return out
}

// objectFormat returns the object format of the repository.
func objectFormat(g Git) (Algorithm, error) {
	out, err := g.Run("rev-parse", "--show-object-format")
	if err != nil {
		return "", err
	}
	return ParseAlgorithm(strings.TrimSpace(string(out)))
}

// headCommit returns the commit of HEAD, or "" when HEAD is unborn (a
// repository without commits).
func headCommit(g Git) (string, error) {
	out, err := g.Run("rev-parse", "--verify", "-q", "HEAD^{commit}")
	if err != nil {
		// With -q, a missing object is reported only by exit status 1.
		if exitCode(err) == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// hasChanges reports whether the work tree or the index has changes against
// HEAD in the paths selected by pathspecs, including untracked files and
// excluding ignored files.
func hasChanges(g Git, pathspecs []string) (bool, error) {
	args := []string{
		// Do not refresh and write the repository's index.
		"--no-optional-locks",
		"status", "--porcelain", "-z",
		"--untracked-files=all", "--ignore-submodules=none", "--no-renames",
		"--",
	}
	out, err := g.Run(append(args, pathspecs...)...)
	if err != nil {
		return false, err
	}
	return len(out) > 0, nil
}

// writeTempTree adds the paths selected by pathspecs to a temporary index
// that starts from HEAD (or empty when fromHead is false), and returns the
// tree written from it.
func writeTempTree(g Git, fromHead bool, pathspecs []string) (string, error) {
	tmp, err := os.MkdirTemp("", "dpm-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	idx := Git{Dir: g.Dir, Env: append(append([]string(nil), g.Env...), "GIT_INDEX_FILE="+filepath.Join(tmp, "index"))}

	readArgs := []string{"read-tree", "--empty"}
	if fromHead {
		readArgs = []string{"read-tree", "HEAD"}
	}
	if _, err := idx.Run(readArgs...); err != nil {
		return "", err
	}
	// The result is that of "git add -A -- <pathspecs>", done in two steps.
	// "git add -A" (and "git add" of a file by name) fails when a pathspec
	// names a directory that matches an ignore pattern, even one with
	// tracked files, or a tracked file below it. Whether a directory could be
	// read would then depend on whether other directories had changes.
	// Instead:
	//   - "git add -u" records the changes and deletions of tracked files and
	//     never checks ignore patterns. It is given only the directories that
	//     have tracked files (from HEAD, "git ls-files --cached"), because
	//     newer versions of git fail when a pathspec of "git add -u" matches
	//     nothing, and "git add -u" without pathspecs would record the whole
	//     work tree.
	//   - The untracked files that are not ignored are listed with "git
	//     ls-files --others --exclude-standard", which uses the same ignore
	//     rules as "git add", and added by name. git compares every file
	//     with every pathspec, so this takes time in proportion to the
	//     square of the number of untracked files; there are few in the
	//     directories of a dependency.
	//
	// core.safecrlf only makes the user's own "git add" warn or stop when a
	// conversion is not reversible. It does not change what is stored, and
	// this add is not the user's, so it must not stop here.
	add := []string{"-c", "core.safecrlf=false", "add"}

	tracked, err := idx.Run(append([]string{"ls-files", "-z", "--cached", "--"}, pathspecs...)...)
	if err != nil {
		return "", err
	}
	if withFiles := pathspecsWithFiles(pathspecs, tracked); len(withFiles) > 0 {
		args := append(append(append([]string(nil), add...), "-u", "--"), withFiles...)
		if _, err := idx.Run(args...); err != nil {
			return "", err
		}
	}

	others, err := idx.Run(append([]string{"ls-files", "-z", "--others", "--exclude-standard", "--"}, pathspecs...)...)
	if err != nil {
		return "", err
	}
	if len(others) > 0 {
		args := append(append([]string(nil), add...), "--pathspec-from-file=-", "--pathspec-file-nul")
		if _, err := idx.RunInput(bytes.NewReader(others), args...); err != nil {
			return "", err
		}
	}
	out, err := idx.Run("write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// pathspecsWithFiles returns the pathspecs (as made by toPathspecs) that
// select at least one of files, a NUL-separated list of paths relative to
// the top level.
func pathspecsWithFiles(pathspecs []string, files []byte) []string {
	var paths []string
	for _, f := range strings.Split(string(files), "\x00") {
		if f != "" {
			paths = append(paths, f)
		}
	}
	var out []string
	for _, ps := range pathspecs {
		for _, f := range paths {
			if ps == "." || f == ps || strings.HasPrefix(f, ps+"/") {
				out = append(out, ps)
				break
			}
		}
	}
	return out
}

// ReadTrees returns the trees of the directories subdirs (slash paths, ""
// for the root) in treeish, a commit or a tree of the repository that g
// runs in, with their entries. a is the object format of the repository.
// The results are in the order of subdirs. A directory that does not exist
// in treeish is an error that wraps ErrNotFound.
//
// The hashes are the values of "git rev-parse <treeish>:<subdir>"; they are
// read for all directories with one "git cat-file --batch-check", and the
// entries with one "git ls-tree -r".
func ReadTrees(g Git, a Algorithm, treeish string, subdirs []string) ([]Tree, error) {
	return readTrees(g, a, treeish, subdirs, false)
}

// readTrees implements ReadTrees. When emptyIfMissing is true, a directory
// that does not exist in treeish gives the empty tree instead of an error.
func readTrees(g Git, a Algorithm, treeish string, subdirs []string, emptyIfMissing bool) ([]Tree, error) {
	if len(subdirs) == 0 {
		return nil, nil
	}
	var in bytes.Buffer
	for _, s := range subdirs {
		if !validSubdir(s) {
			return nil, fmt.Errorf("invalid directory %q in the repository", s)
		}
		fmt.Fprintf(&in, "%s:%s\n", treeish, s)
	}
	out, err := g.RunInput(&in, "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(subdirs) {
		return nil, fmt.Errorf("unexpected output of git cat-file --batch-check: %q", out)
	}
	trees := make([]Tree, len(subdirs))
	var present []string
	for i, line := range lines {
		name := treeish + ":" + subdirs[i]
		// A missing object is reported as the input line followed by
		// " missing"; the name may contain spaces.
		if line == name+" missing" {
			if !emptyIfMissing {
				return nil, fmt.Errorf("%q in %s: %w", subdirs[i], treeish, ErrNotFound)
			}
			h, err := TreeHash(a, nil)
			if err != nil {
				return nil, err
			}
			trees[i] = Tree{Algorithm: a, Hash: h}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("unexpected output of git cat-file --batch-check for %s: %q", name, line)
		}
		if fields[1] != "tree" {
			return nil, fmt.Errorf("%q in %s is not a directory", subdirs[i], treeish)
		}
		if err := checkHex(a, fields[0]); err != nil {
			return nil, fmt.Errorf("git cat-file --batch-check for %s: %w", name, err)
		}
		trees[i] = Tree{Algorithm: a, Hash: fields[0]}
		present = append(present, subdirs[i])
	}
	if len(present) == 0 {
		return trees, nil
	}

	all, err := lsTree(g, treeish, present)
	if err != nil {
		return nil, err
	}
	for i, s := range subdirs {
		prefix := ""
		if s != "" {
			prefix = s + "/"
		}
		for _, e := range all {
			if rel, ok := strings.CutPrefix(e.Path, prefix); ok {
				e.Path = rel
				trees[i].Entries = append(trees[i].Entries, e)
			}
		}
	}
	return trees, nil
}

// lsTree lists the entries below subdirs in treeish recursively, with paths
// relative to the root of treeish.
func lsTree(g Git, treeish string, subdirs []string) ([]Entry, error) {
	args := []string{"ls-tree", "-r", "-z", "--full-tree", treeish}
	whole := false
	for _, s := range subdirs {
		if s == "" {
			whole = true
		}
	}
	if !whole {
		args = append(args, "--")
		args = append(args, subdirs...)
	}
	out, err := g.Run(args...)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, rec := range strings.Split(string(out), "\x00") {
		if rec == "" {
			continue
		}
		// <mode> SP <type> SP <object> TAB <path>
		meta, p, ok := strings.Cut(rec, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("unexpected output of git ls-tree: %q", rec)
		}
		mode, err := ParseMode(fields[0])
		if err != nil {
			return nil, fmt.Errorf("git ls-tree: %s: %w", p, err)
		}
		if mode == ModeTree || path.Clean(p) != p {
			return nil, fmt.Errorf("unexpected entry in git ls-tree -r: %q", rec)
		}
		entries = append(entries, Entry{Path: p, Mode: mode, Hash: fields[2]})
	}
	return entries, nil
}
