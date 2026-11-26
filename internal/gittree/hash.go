// Package gittree computes Git blob and tree hashes.
//
// The integrity of a dependency is the hash of the Git tree object that holds
// its files (written "git-sha1:<hex>" or "git-sha256:<hex>"). The hashes are
// computed here without git for directories on disk (dir.go), and with the
// help of git for directories inside a Git work tree, where line ending
// normalization and .gitattributes must be applied exactly as git does
// (repo.go). git.go runs the git commands.
package gittree

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Algorithm is a Git object format.
type Algorithm string

// Supported object formats. The values are the names that git uses
// ("git rev-parse --show-object-format").
const (
	SHA1   Algorithm = "sha1"
	SHA256 Algorithm = "sha256"
)

// ParseAlgorithm returns the algorithm with the given Git name.
func ParseAlgorithm(name string) (Algorithm, error) {
	switch a := Algorithm(name); a {
	case SHA1, SHA256:
		return a, nil
	}
	return "", fmt.Errorf("unsupported object format %q", name)
}

func (a Algorithm) newHash() hash.Hash {
	switch a {
	case SHA1:
		return sha1.New()
	case SHA256:
		return sha256.New()
	}
	panic(fmt.Sprintf("gittree: unsupported algorithm %q", string(a)))
}

// HexLen returns the length of a hash in hexadecimal digits.
func (a Algorithm) HexLen() int {
	return a.newHash().Size() * 2
}

// Mode is the mode of a Git tree entry.
type Mode uint32

// Tree entry modes.
const (
	ModeFile    Mode = 0o100644
	ModeExec    Mode = 0o100755
	ModeSymlink Mode = 0o120000
	ModeGitlink Mode = 0o160000
	ModeTree    Mode = 0o040000
)

// String returns the mode as Git writes it in a tree object ("40000" for a
// tree, without a leading zero).
func (m Mode) String() string {
	return strconv.FormatUint(uint64(m), 8)
}

// ParseMode parses an octal mode as printed by "git ls-tree" or
// "git ls-files -s".
func ParseMode(s string) (Mode, error) {
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid mode %q", s)
	}
	m := Mode(n)
	switch m {
	case ModeFile, ModeExec, ModeSymlink, ModeGitlink, ModeTree:
		return m, nil
	}
	return 0, fmt.Errorf("unsupported mode %q", s)
}

// Entry is a non-tree entry of a tree, listed with its full path.
type Entry struct {
	// Path is relative to the tree, with "/" as the separator.
	Path string
	// Mode is ModeFile, ModeExec, ModeSymlink or ModeGitlink.
	Mode Mode
	// Hash is the hexadecimal hash of the blob (the commit for ModeGitlink).
	Hash string
}

// Tree is a tree object with all the entries below it.
type Tree struct {
	Algorithm Algorithm
	// Hash is the hexadecimal hash of the tree object.
	Hash string
	// Entries lists the files below the tree, sorted by Path. Directories
	// are not listed; an empty tree has no entries.
	Entries []Entry
}

// Integrity returns the hash in the form recorded as integrity, such as
// "git-sha1:<hex>".
func (t Tree) Integrity() string {
	return FormatIntegrity(t.Algorithm, t.Hash)
}

// Content returns the hash of t with the execute bit ignored, in the form
// of Integrity: the tree whose regular files all have ModeFile, the other
// entries (symbolic links) kept as they are. It is recorded next to the
// integrity and compared with a deployed directory hashed with
// IgnoreExecBit, since Windows does not keep the execute bit
// (spec/2026/11/20261117-01 3.5).
func (t Tree) Content() (string, error) {
	entries := make([]Entry, len(t.Entries))
	for i, e := range t.Entries {
		if e.Mode == ModeExec {
			e.Mode = ModeFile
		}
		entries[i] = e
	}
	h, err := TreeHash(t.Algorithm, entries)
	if err != nil {
		return "", err
	}
	return FormatIntegrity(t.Algorithm, h), nil
}

// IgnoreExecBit is a FileModeFunc that gives ModeFile to every regular
// file. DirTree with it gives the Content of a directory.
func IgnoreExecBit(string, bool) Mode { return ModeFile }

const integrityPrefix = "git-"

// FormatIntegrity returns "git-<algorithm>:<hex>".
func FormatIntegrity(a Algorithm, hexHash string) string {
	return integrityPrefix + string(a) + ":" + hexHash
}

// ParseIntegrity parses a value written by FormatIntegrity.
func ParseIntegrity(s string) (Algorithm, string, error) {
	name, hexHash, ok := strings.Cut(strings.TrimPrefix(s, integrityPrefix), ":")
	if !ok || !strings.HasPrefix(s, integrityPrefix) {
		return "", "", fmt.Errorf("invalid integrity %q: expected git-<algorithm>:<hex>", s)
	}
	a, err := ParseAlgorithm(name)
	if err != nil {
		return "", "", fmt.Errorf("invalid integrity %q: %w", s, err)
	}
	if err := checkHex(a, hexHash); err != nil {
		return "", "", fmt.Errorf("invalid integrity %q: %w", s, err)
	}
	return a, hexHash, nil
}

// checkHex verifies that s is a lowercase hexadecimal hash of algorithm a.
func checkHex(a Algorithm, s string) error {
	if len(s) != a.HexLen() {
		return fmt.Errorf("%s hash must have %d hexadecimal digits, got %q", a, a.HexLen(), s)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return fmt.Errorf("invalid hexadecimal hash %q", s)
		}
	}
	return nil
}

// objectHash returns the hash of a Git object: "<type> <size>\x00<data>".
func objectHash(a Algorithm, typ string, data []byte) []byte {
	h := a.newHash()
	fmt.Fprintf(h, "%s %d\x00", typ, len(data))
	h.Write(data)
	return h.Sum(nil)
}

// BlobHash returns the hexadecimal hash of a blob with the given content,
// the same value as "git hash-object".
func BlobHash(a Algorithm, data []byte) string {
	return hex.EncodeToString(objectHash(a, "blob", data))
}

// TreeHash returns the hexadecimal hash of the tree that holds the given
// entries, the same value as "git write-tree" for an index with those
// entries. Intermediate trees are created from the paths, so a directory
// without entries is never part of the result. The entries do not have to
// be sorted.
func TreeHash(a Algorithm, entries []Entry) (string, error) {
	root := &dirNode{}
	for _, e := range entries {
		if err := root.add(a, e); err != nil {
			return "", err
		}
	}
	sum, err := root.hash(a)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum), nil
}

// dirNode is a directory under construction in TreeHash.
type dirNode struct {
	files map[string]Entry
	dirs  map[string]*dirNode
}

func (d *dirNode) add(a Algorithm, e Entry) error {
	switch e.Mode {
	case ModeFile, ModeExec, ModeSymlink, ModeGitlink:
	default:
		return fmt.Errorf("%s: unsupported mode %s", e.Path, e.Mode)
	}
	if err := checkHex(a, e.Hash); err != nil {
		return fmt.Errorf("%s: %w", e.Path, err)
	}
	if !validEntryPath(e.Path) {
		return fmt.Errorf("invalid path %q in tree", e.Path)
	}
	parts := strings.Split(e.Path, "/")
	cur := d
	for i, name := range parts[:len(parts)-1] {
		if _, ok := cur.files[name]; ok {
			return fmt.Errorf("%s is both a file and a directory", strings.Join(parts[:i+1], "/"))
		}
		next, ok := cur.dirs[name]
		if !ok {
			next = &dirNode{}
			if cur.dirs == nil {
				cur.dirs = map[string]*dirNode{}
			}
			cur.dirs[name] = next
		}
		cur = next
	}
	name := parts[len(parts)-1]
	if _, ok := cur.dirs[name]; ok {
		return fmt.Errorf("%s is both a file and a directory", e.Path)
	}
	if _, ok := cur.files[name]; ok {
		return fmt.Errorf("duplicate path %q in tree", e.Path)
	}
	if cur.files == nil {
		cur.files = map[string]Entry{}
	}
	cur.files[name] = e
	return nil
}

// validEntryPath reports whether p is a clean, relative slash path whose
// elements can be tree entry names.
func validEntryPath(p string) bool {
	if p == "" || strings.ContainsRune(p, 0) || path.Clean(p) != p || strings.HasPrefix(p, "/") {
		return false
	}
	for _, name := range strings.Split(p, "/") {
		if name == "." || name == ".." {
			return false
		}
	}
	return true
}

// treeItem is one entry of a single tree object.
type treeItem struct {
	name string
	mode Mode
	hash []byte
}

// hash returns the binary hash of the tree object for d.
func (d *dirNode) hash(a Algorithm) ([]byte, error) {
	items := make([]treeItem, 0, len(d.files)+len(d.dirs))
	for name, e := range d.files {
		sum, err := hex.DecodeString(e.Hash)
		if err != nil {
			return nil, err
		}
		items = append(items, treeItem{name, e.Mode, sum})
	}
	for name, sub := range d.dirs {
		sum, err := sub.hash(a)
		if err != nil {
			return nil, err
		}
		items = append(items, treeItem{name, ModeTree, sum})
	}
	sortTreeItems(items)
	var buf bytes.Buffer
	for _, it := range items {
		buf.WriteString(it.mode.String())
		buf.WriteByte(' ')
		buf.WriteString(it.name)
		buf.WriteByte(0)
		buf.Write(it.hash)
	}
	return objectHash(a, "tree", buf.Bytes()), nil
}

// sortTreeItems sorts the entries of a tree object in Git's order: by name
// as bytes, where the name of a tree is compared as if it ended with "/".
// For example "a.txt" comes before the directory "a", which comes before
// "a0".
func sortTreeItems(items []treeItem) {
	key := func(it treeItem) string {
		if it.mode == ModeTree {
			return it.name + "/"
		}
		return it.name
	}
	sort.Slice(items, func(i, j int) bool { return key(items[i]) < key(items[j]) })
}
