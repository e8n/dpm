// Package source fetches the contents of dependencies and computes their
// integrity (spec/2026/11/20261117-01 3.4 and 3.5).
//
// A dependency is either a local directory (path.go) or a directory in a Git
// repository (git.go). Fetch reads every dependency into memory: the tree
// that gives its integrity, and the bytes of every blob, which are the bytes
// that are deployed. Nothing is written to the destinations here; WriteTo
// writes a fetched dependency into a directory that the caller prepares.
package source

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/e8n/dpm/internal/gittree"
	"github.com/e8n/dpm/internal/manifest"
)

// Source is the fetched contents of one dependency.
type Source struct {
	// Dep is the name of the dependency.
	Dep string
	// Tree is the tree of the contents. Its hash gives the integrity and
	// its entries list the files (path, mode and blob hash).
	Tree gittree.Tree
	// Commit is the commit that the ref of a Git dependency resolved to
	// ("" for a path dependency).
	Commit string
	// Warnings are messages about the dependency for the user, in English.
	// They do not include the name of the dependency.
	Warnings []string

	// blobs holds the contents of the blobs by hash.
	blobs map[string][]byte
}

// Integrity returns the integrity of the contents, such as
// "git-sha1:<hex>".
func (s *Source) Integrity() string {
	return s.Tree.Integrity()
}

// Fetch fetches the contents of deps. The results are in the order of deps.
//
// Path dependencies in the same Git work tree are hashed together, and Git
// dependencies with the same repository and ref are fetched once. The first
// error stops the fetch; it names the dependency.
func Fetch(deps []manifest.Dep) ([]*Source, error) {
	for _, d := range deps {
		if err := validateDep(d); err != nil {
			return nil, depError(d.Name, err)
		}
	}
	out := make([]*Source, len(deps))
	var pathIdx, gitIdx []int
	for i, d := range deps {
		if d.IsGit() {
			gitIdx = append(gitIdx, i)
		} else {
			pathIdx = append(pathIdx, i)
		}
	}
	if err := fetchPaths(deps, pathIdx, out); err != nil {
		return nil, err
	}
	if err := fetchGits(deps, gitIdx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// depError wraps err with the name of the dependency.
func depError(name string, err error) error {
	return fmt.Errorf("dependency %q: %w", name, err)
}

// validateDep checks the values of a dependency that the manifest package
// leaves to this package.
func validateDep(d manifest.Dep) error {
	if !d.IsGit() {
		return nil
	}
	if !validSubdir(d.Subdir) {
		return fmt.Errorf("subdir: %q must be a relative path inside the repository with \"/\" as the separator", d.Subdir)
	}
	if !validRef(d.Ref) {
		return fmt.Errorf("ref: %q is not a valid tag, branch or commit", d.Ref)
	}
	return nil
}

// validSubdir reports whether s is "" (the root of the repository) or a
// clean relative slash path without "." or "..". "\" is rejected on every
// OS: it is most likely meant as a separator.
func validSubdir(s string) bool {
	return s == "" || (validRelPath(s) && !strings.Contains(s, `\`))
}

// validRelPath reports whether p is a relative slash path whose elements
// are not empty, "." or "..".
func validRelPath(p string) bool {
	if p == "" || strings.ContainsAny(p, "\x00\n\r") {
		return false
	}
	for _, elem := range strings.Split(p, "/") {
		switch elem {
		case "", ".", "..":
			return false
		}
	}
	return true
}

// validFilePath reports whether the slash path p of a file can be written
// under a directory: validRelPath, and on Windows also no element that
// Windows would not write as a plain file of that name (see windowsName).
func validFilePath(p string) bool {
	if !validRelPath(p) {
		return false
	}
	if runtime.GOOS == "windows" {
		for _, elem := range strings.Split(p, "/") {
			if !windowsName(elem) {
				return false
			}
		}
	}
	return true
}

// windowsReserved lists the device names of Windows, which refer to the
// device whatever the directory and the extension (the common ones; names
// such as "CONIN$" or "COM¹" are not expected in a dependency).
var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// windowsName reports whether elem can be written as a file of that name on
// Windows: it has no "\" or ":" (a separator, or a drive or stream name),
// is not a device name (with or without an extension) and does not end in
// "." or " ", which Windows removes.
func windowsName(elem string) bool {
	if strings.ContainsAny(elem, `\:`) || strings.HasSuffix(elem, ".") || strings.HasSuffix(elem, " ") {
		return false
	}
	base, _, _ := strings.Cut(elem, ".")
	return !windowsReserved[strings.ToUpper(strings.TrimRight(base, " "))]
}

// validRef reports whether ref can be passed to git fetch as the name of
// one ref or commit: not a refspec with a destination (":"), a pattern
// ("*") or a forced update ("+"). "" means the default branch.
func validRef(ref string) bool {
	return !strings.ContainsAny(ref, ":* \t\n\r\x00") && !strings.HasPrefix(ref, "+")
}

// checkTree rejects trees that cannot be deployed: one that has a submodule
// (a gitlink entry) and one without files.
func checkTree(t gittree.Tree, where string) error {
	for _, e := range t.Entries {
		if e.Mode == gittree.ModeGitlink {
			return fmt.Errorf("%s: %s is a submodule (or a nested Git repository), which dpm cannot deploy", where, e.Path)
		}
	}
	if len(t.Entries) == 0 {
		return fmt.Errorf("%s has no files to deploy", where)
	}
	return nil
}

// WriteTo writes the files of the dependency under dir, creating dir and
// the directories in it when needed. The files must not exist yet.
//
// Each file gets the bytes of its blob as they are: no line ending
// conversion is applied (spec/2026/11/20261117-01 3.5). Executable files
// are created with mode 0755 and other files with 0644 (both reduced by the
// umask). Symbolic links are created as links to the target stored in the
// blob, after all regular files, so that no file is written through a link
// even on a case-insensitive file system. A submodule is an error.
func (s *Source) WriteTo(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var links []gittree.Entry
	for _, e := range s.Tree.Entries {
		if !validFilePath(e.Path) {
			return fmt.Errorf("invalid path %q in the files of %s", e.Path, s.Dep)
		}
		switch e.Mode {
		case gittree.ModeFile, gittree.ModeExec:
		case gittree.ModeSymlink:
			links = append(links, e)
			continue
		case gittree.ModeGitlink:
			return fmt.Errorf("%s: %s is a submodule, which dpm cannot deploy", s.Dep, e.Path)
		default:
			return fmt.Errorf("%s: %s has an unsupported mode %s", s.Dep, e.Path, e.Mode)
		}
		data, err := s.blob(e)
		if err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if e.Mode == gittree.ModeExec {
			perm = 0o755
		}
		if err := writeNewFile(filepath.Join(dir, filepath.FromSlash(e.Path)), data, perm); err != nil {
			return err
		}
	}
	for _, e := range links {
		data, err := s.blob(e)
		if err != nil {
			return err
		}
		if err := noLinkInParents(dir, e.Path); err != nil {
			return fmt.Errorf("%s: %w", s.Dep, err)
		}
		p := filepath.Join(dir, filepath.FromSlash(e.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		// os.Symlink converts "/" in the target to "\" on Windows.
		if err := os.Symlink(string(data), p); err != nil {
			return err
		}
	}
	return nil
}

// noLinkInParents checks that no parent directory of the slash path rel
// under dir is a symbolic link. Links are created after the regular files,
// but one link can still be the parent of another on a case-insensitive
// file system ("L" a link and "l/y" a link), and creating "l/y" would then
// create it wherever "L" points to.
func noLinkInParents(dir, rel string) error {
	elems := strings.Split(rel, "/")
	p := dir
	for _, elem := range elems[:len(elems)-1] {
		p = filepath.Join(p, elem)
		info, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			// The rest is created by MkdirAll as directories.
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("cannot create %s: %s is a symbolic link (names that differ only in letter case?)", rel, p)
		}
	}
	return nil
}

// blob returns the contents of the blob of e.
func (s *Source) blob(e gittree.Entry) ([]byte, error) {
	data, ok := s.blobs[e.Hash]
	if !ok {
		return nil, fmt.Errorf("%s: the contents of %s (%s) were not read", s.Dep, e.Path, e.Hash)
	}
	return data, nil
}

// writeNewFile creates the file p, which must not exist, with data.
func writeNewFile(p string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readBlobs reads the blobs with the given hashes from the repository that
// g runs in, with one "git cat-file --batch".
func readBlobs(g gittree.Git, hashes []string) (map[string][]byte, error) {
	blobs := make(map[string][]byte, len(hashes))
	var in bytes.Buffer
	var order []string
	for _, h := range hashes {
		if _, ok := blobs[h]; ok {
			continue
		}
		blobs[h] = nil
		order = append(order, h)
		in.WriteString(h + "\n")
	}
	if len(order) == 0 {
		return blobs, nil
	}
	out, err := g.RunInput(&in, "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	// Each object is "<hash> <type> <size>\n<contents>\n"; a missing one is
	// "<hash> missing\n".
	for _, h := range order {
		header, rest, ok := bytes.Cut(out, []byte("\n"))
		if !ok {
			return nil, errors.New("unexpected end of the output of git cat-file --batch")
		}
		fields := strings.Fields(string(header))
		if len(fields) != 3 || fields[0] != h {
			return nil, fmt.Errorf("git cat-file --batch: cannot read blob %s: %q", h, header)
		}
		if fields[1] != "blob" {
			return nil, fmt.Errorf("git cat-file --batch: %s is a %s, not a blob", h, fields[1])
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 || len(rest) < size+1 || rest[size] != '\n' {
			return nil, fmt.Errorf("git cat-file --batch: unexpected output for %s", h)
		}
		blobs[h] = rest[:size:size]
		out = rest[size+1:]
	}
	return blobs, nil
}

// entryHashes returns the hashes of the blobs of the entries.
func entryHashes(entries []gittree.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Hash)
	}
	return out
}
