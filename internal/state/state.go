// Package state reads and writes the record of the previous deployment,
// .dpm/state.toml next to the manifest (spec/2026/11/20261117-01 3.6).
//
// The record lists the directories that dpm placed. Paths are relative to
// the manifest's directory with "/" as the separator, so the record stays
// valid when the project is moved. The file is written deterministically:
// the entries are sorted and no dates or machine specific values are
// written.
package state

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/e8n/dpm/internal/fsutil"
	"github.com/e8n/dpm/internal/gittree"
)

// Names of the files.
const (
	DirName       = ".dpm"
	FileName      = "state.toml"
	gitignoreName = ".gitignore"
)

// Version is the only version of the file format this dpm knows.
const Version = 1

// header is written at the top of the file.
const header = "# Written by dpm. Do not edit.\n"

// gitignore keeps the whole .dpm directory out of Git without the user
// editing their own .gitignore.
const gitignore = "*\n"

// State is the content of .dpm/state.toml.
type State struct {
	// Placed lists the directories that dpm placed.
	Placed []Placed
}

// Placed is one directory that dpm placed.
type Placed struct {
	// Dep is the name of the dependency, which is also the last element of
	// Dir.
	Dep string `toml:"dep"`
	// Dir is the directory relative to the manifest's directory with "/" as
	// the separator, such as ".claude/skills/commit". It is absolute only
	// when no relative path exists (another drive on Windows).
	Dir string `toml:"dir"`
	// Integrity is the integrity of the contents when they were placed.
	Integrity string `toml:"integrity"`
	// Content is the hash of the same contents with the execute bit
	// ignored (gittree.Tree.Content), in the same algorithm as Integrity.
	// It is compared with the deployed directory (spec 3.5). It is empty
	// in records written before it was added.
	Content string `toml:"content,omitempty"`
}

// Path returns the directory of p as an absolute OS path.
func (p Placed) Path(manifestDir string) string {
	if fsutil.IsAbs(p.Dir) {
		return filepath.FromSlash(p.Dir)
	}
	return filepath.Join(manifestDir, filepath.FromSlash(p.Dir))
}

// RelDir returns the form of the OS path dir that is recorded in Dir: the
// path relative to manifestDir with "/" as the separator, or the absolute
// path with "/" when there is no relative path (another drive or a UNC path
// on Windows). The leading "//" of a UNC path is kept, so that Path gives
// the same directory back.
func RelDir(manifestDir, dir string) string {
	rel, err := filepath.Rel(manifestDir, dir)
	if err != nil {
		s := fsutil.ToSlash(dir)
		if isUNC(filepath.ToSlash(filepath.Clean(dir))) {
			// path.Clean, used by fsutil.ToSlash, reduces "//" to "/".
			s = "/" + s
		}
		return s
	}
	return fsutil.ToSlash(rel)
}

// isUNC reports whether the slash path p starts with exactly two slashes,
// as a UNC path such as "//server/share" does.
func isUNC(p string) bool {
	return len(p) > 2 && p[0] == '/' && p[1] == '/' && p[2] != '/'
}

// cleanDir is path.Clean that keeps the leading "//" of a UNC path.
func cleanDir(p string) string {
	if isUNC(p) {
		return "/" + path.Clean(p[1:])
	}
	return path.Clean(p)
}

// File returns the path of state.toml for the manifest in manifestDir.
func File(manifestDir string) string {
	return filepath.Join(manifestDir, DirName, FileName)
}

// rawState mirrors the file.
type rawState struct {
	Version *int     `toml:"version"`
	Placed  []Placed `toml:"placed"`
}

// Load reads the record of the manifest in manifestDir. A missing file is
// not an error: it gives an empty State, as on the first install.
func Load(manifestDir string) (*State, error) {
	file := File(manifestDir)
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return &State{}, nil
	}
	if err != nil {
		return nil, err
	}
	s, err := parse(data)
	if err != nil {
		// Deleting the file is safe: directories whose names match a
		// dependency are adopted again (spec 3.6).
		return nil, fmt.Errorf("%s: %w (deleting the file lets dpm start over)", file, err)
	}
	return s, nil
}

func parse(data []byte) (*State, error) {
	var raw rawState
	md, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&raw)
	if err != nil {
		return nil, err
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("unknown keys: %s", strings.Join(keys, ", "))
	}
	switch {
	case raw.Version == nil:
		return nil, errors.New("version is missing")
	case *raw.Version != Version:
		return nil, fmt.Errorf("unsupported version %d (this dpm reads version %d; was it written by a newer dpm?)", *raw.Version, Version)
	}
	seen := make(map[string]bool, len(raw.Placed))
	for i, p := range raw.Placed {
		if err := validate(p); err != nil {
			return nil, fmt.Errorf("placed[%d]: %w", i, err)
		}
		if seen[p.Dir] {
			return nil, fmt.Errorf("placed[%d]: %q is recorded twice", i, p.Dir)
		}
		seen[p.Dir] = true
	}
	return &State{Placed: raw.Placed}, nil
}

// validate checks one entry. dpm deletes recorded directories that are no
// longer in the manifest, so an entry must not point at anything other than
// a directory named after a dependency.
func validate(p Placed) error {
	switch {
	case p.Dep == "" || p.Dep == "." || p.Dep == ".." || strings.ContainsAny(p.Dep, `/\`):
		return fmt.Errorf("invalid dep %q", p.Dep)
	case p.Dir == "" || strings.Contains(p.Dir, `\`) || cleanDir(p.Dir) != p.Dir:
		return fmt.Errorf("invalid dir %q", p.Dir)
	case path.Base(p.Dir) != p.Dep:
		return fmt.Errorf("dir %q does not end with the dependency name %q", p.Dir, p.Dep)
	}
	a, _, err := gittree.ParseIntegrity(p.Integrity)
	if err != nil {
		return err
	}
	if p.Content == "" {
		// Written before content was added; check compares the deployed
		// directory with the integrity instead.
		return nil
	}
	ca, _, err := gittree.ParseIntegrity(p.Content)
	if err != nil {
		return fmt.Errorf("content: %w", err)
	}
	if ca != a {
		return fmt.Errorf("content %q does not use the algorithm of the integrity %q", p.Content, p.Integrity)
	}
	return nil
}

// Save writes the record of the manifest in manifestDir. It creates .dpm
// and .dpm/.gitignore when needed, and replaces state.toml through a
// temporary file so that an interrupted write does not leave a broken
// record.
func Save(manifestDir string, s *State) error {
	for i, p := range s.Placed {
		if err := validate(p); err != nil {
			return fmt.Errorf("cannot record placed[%d]: %w", i, err)
		}
	}
	dir := filepath.Join(manifestDir, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeGitignore(dir); err != nil {
		return err
	}
	data, err := Encode(s)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, FileName+".*.tmp")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		// os.Rename replaces an existing file on Windows too.
		err = os.Rename(tmp.Name(), filepath.Join(dir, FileName))
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// writeGitignore creates .gitignore in dir unless it exists.
func writeGitignore(dir string) error {
	f, err := os.OpenFile(filepath.Join(dir, gitignoreName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString(gitignore)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Encode returns the content of state.toml for s. The entries are sorted by
// Dir (then Dep), so the same State always gives the same bytes.
func Encode(s *State) ([]byte, error) {
	placed := append([]Placed(nil), s.Placed...)
	sort.Slice(placed, func(i, j int) bool {
		if placed[i].Dir != placed[j].Dir {
			return placed[i].Dir < placed[j].Dir
		}
		return placed[i].Dep < placed[j].Dep
	})
	var buf bytes.Buffer
	buf.WriteString(header)
	enc := toml.NewEncoder(&buf)
	enc.Indent = ""
	if err := enc.Encode(rawState{Version: ptr(Version), Placed: placed}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func ptr[T any](v T) *T { return &v }
