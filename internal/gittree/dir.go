package gittree

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// FileModeFunc returns the tree entry mode of the regular file at path (a
// slash path relative to the directory). executable reports whether the
// file has an execute bit on disk; it is always false on Windows.
type FileModeFunc func(path string, executable bool) Mode

// ModeFromDisk uses the execute bit on disk: ModeExec when it is set and
// ModeFile otherwise. Use it to hash a directory that is the source of a
// dependency.
func ModeFromDisk(_ string, executable bool) Mode {
	if executable {
		return ModeExec
	}
	return ModeFile
}

// ModesFrom returns a FileModeFunc that ignores the execute bit on disk and
// takes the mode of each regular file from the entry with the same path in
// entries, or ModeFile when there is no such regular file entry.
//
// Deployed files are compared with the integrity while ignoring the execute
// bit, which Windows does not keep (spec/2026/11/20261117-01 3.5). Hashing a
// deployed directory with the modes of the source's current entries gives
// the integrity when the contents are unchanged on any OS; a change of only
// the mode in the source is then reported against the source, not against
// the deployed files.
func ModesFrom(entries []Entry) FileModeFunc {
	modes := make(map[string]Mode, len(entries))
	for _, e := range entries {
		if e.Mode == ModeFile || e.Mode == ModeExec {
			modes[e.Path] = e.Mode
		}
	}
	return func(path string, _ bool) Mode {
		if m, ok := modes[path]; ok {
			return m
		}
		return ModeFile
	}
}

// DirTree computes the tree of the directory dir from the bytes on disk,
// without any line ending normalization. It is used for a source directory
// that is not in a Git work tree and for deployed directories.
//
//   - Empty directories are not part of the tree, as in Git.
//   - Symbolic links are not followed; each becomes a ModeSymlink entry whose
//     content is the link target with "/" as the separator. A link at dir
//     itself is followed.
//   - The mode of a regular file is decided by fileMode (ModeFromDisk when
//     nil).
//   - Other kinds of files (devices, sockets, ...) are an error.
func DirTree(a Algorithm, dir string, fileMode FileModeFunc) (Tree, error) {
	if fileMode == nil {
		fileMode = ModeFromDisk
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Tree{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return Tree{}, err
	}
	if !info.IsDir() {
		return Tree{}, fmt.Errorf("%s is not a directory", dir)
	}

	var entries []Entry
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		var data []byte
		var mode Mode
		switch t := d.Type(); {
		case t&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			// Git stores link targets with "/" (Git for Windows converts
			// "\" when it reads a link), and os.Symlink writes "\" on
			// Windows.
			data = []byte(filepath.ToSlash(target))
			mode = ModeSymlink
		case t.IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			data, err = os.ReadFile(p)
			if err != nil {
				return err
			}
			mode = fileMode(rel, info.Mode()&0o111 != 0)
			if mode != ModeFile && mode != ModeExec {
				return fmt.Errorf("%s: invalid mode %s for a regular file", p, mode)
			}
		default:
			return fmt.Errorf("%s: unsupported file type %s", p, t)
		}
		entries = append(entries, Entry{Path: rel, Mode: mode, Hash: BlobHash(a, data)})
		return nil
	})
	if err != nil {
		return Tree{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	h, err := TreeHash(a, entries)
	if err != nil {
		return Tree{}, err
	}
	return Tree{Algorithm: a, Hash: h, Entries: entries}, nil
}
