// Package manifest reads dpm.toml and dpm.local.toml, expands variables,
// validates the result and resolves every path to an absolute OS path.
//
// All relative paths are resolved against the directory of the manifest's
// real location (symbolic links are followed), so a manifest that is reached
// through a link behaves as if it were opened at its target.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/e8n/dpm/internal/fsutil"
)

// File names.
const (
	FileName      = "dpm.toml"
	LocalFileName = "dpm.local.toml"
)

// DefaultVars are the variables that exist even when [vars] does not
// declare them.
var DefaultVars = map[string]string{
	"claude": ".claude",
	"agents": ".agents",
}

// Manifest is a loaded, validated and resolved dpm.toml.
type Manifest struct {
	// File is the absolute path of dpm.toml with symbolic links resolved.
	File string
	// Dir is the directory of File. Relative paths are resolved against it.
	Dir string
	// LocalFile is the absolute path of dpm.local.toml next to File, or ""
	// when there is none.
	LocalFile string
	// Vars holds the effective variable values as written (defaults, then
	// [vars] of dpm.toml, then [vars] of dpm.local.toml).
	Vars map[string]string
	// Deps lists the dependencies sorted by name.
	Deps []Dep
}

// Dep is one [deps.<name>] entry.
type Dep struct {
	// Name is the dependency name, which is also the name of the directory
	// that is deployed under each destination.
	Name string

	// Path is the "path" value as written (empty for a Git dependency).
	Path string
	// PathAbs is Path with variables and "~" expanded, as an absolute OS path.
	PathAbs string

	// Git is the repository URL (empty for a path dependency).
	Git string
	// Subdir is the directory inside the repository ("" for the root).
	Subdir string
	// Ref is the tag, branch or commit ("" for the default branch).
	Ref string

	// To lists the destinations in the order they are written.
	To []Target
}

// IsGit reports whether the dependency is fetched from a Git repository.
func (d Dep) IsGit() bool { return d.Git != "" }

// Target is one destination of a dependency.
type Target struct {
	// Raw is the "to" entry as written, such as "${claude}/skills".
	Raw string
	// Dir is Raw expanded to an absolute OS path (the parent directory).
	Dir string
	// Path is the directory that dpm owns: Dir joined with the dependency name.
	Path string
}

// rawManifest mirrors dpm.toml. Pointers tell "absent" from "empty".
type rawManifest struct {
	Vars     map[string]string `toml:"vars"`
	Defaults struct {
		To *[]string `toml:"to"`
	} `toml:"defaults"`
	Deps map[string]rawDep `toml:"deps"`
}

type rawDep struct {
	Path   *string   `toml:"path"`
	Git    *string   `toml:"git"`
	Subdir *string   `toml:"subdir"`
	Ref    *string   `toml:"ref"`
	To     *[]string `toml:"to"`
}

// rawLocal mirrors dpm.local.toml, which may contain only [vars].
type rawLocal struct {
	Vars map[string]string `toml:"vars"`
}

// Load reads the manifest at file and the dpm.local.toml next to its real
// location, then validates and resolves them. When the manifest does not
// exist, the returned error wraps fs.ErrNotExist.
func Load(file string) (*Manifest, error) {
	realPath, err := fsutil.RealPath(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", file, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	data, err := os.ReadFile(realPath)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	m := &Manifest{File: realPath, Dir: filepath.Dir(realPath)}

	var local []byte
	localFile := filepath.Join(m.Dir, LocalFileName)
	local, err = os.ReadFile(localFile)
	switch {
	case err == nil:
		m.LocalFile = localFile
	case errors.Is(err, fs.ErrNotExist):
		local = nil
	default:
		return nil, fmt.Errorf("%s: %w", LocalFileName, err)
	}

	if err := m.parse(data, local); err != nil {
		return nil, err
	}
	return m, nil
}

// parse decodes the manifest and the optional local file (nil when absent)
// and fills m. m.Dir must already be set.
func (m *Manifest) parse(data, local []byte) error {
	var raw rawManifest
	if err := decodeStrict(FileName, data, &raw, unknownKeysError); err != nil {
		return err
	}
	var rawL rawLocal
	if local != nil {
		if err := decodeStrict(LocalFileName, local, &rawL, localKeysError); err != nil {
			return err
		}
	}
	return m.build(&raw, &rawL)
}

// decodeStrict decodes data into v and reports keys that v does not know.
func decodeStrict(name string, data []byte, v any, unknown func(name string, keys []string) error) error {
	md, err := toml.NewDecoder(bytes.NewReader(data)).Decode(v)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return unknown(name, outermostKeys(und))
	}
	return nil
}

// outermostKeys returns the keys whose parent table is not itself in keys,
// so that an unknown table is reported once instead of with every key in it.
func outermostKeys(keys []toml.Key) []string {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k.String()] = true
	}
	var out []string
	for _, k := range keys {
		if len(k) > 1 && set[k[:len(k)-1].String()] {
			continue
		}
		out = append(out, k.String())
	}
	return out
}

func unknownKeysError(name string, keys []string) error {
	return fmt.Errorf("%s: unknown keys: %s", name, strings.Join(keys, ", "))
}

func localKeysError(name string, keys []string) error {
	return fmt.Errorf("%s: only [vars] is allowed (found: %s)", name, strings.Join(keys, ", "))
}

// build validates the decoded files and fills m.
func (m *Manifest) build(raw *rawManifest, local *rawLocal) error {
	var errs []error

	// Variables: defaults, then dpm.toml, then dpm.local.toml.
	m.Vars = make(map[string]string, len(DefaultVars)+len(raw.Vars))
	for k, v := range DefaultVars {
		m.Vars[k] = v
	}
	for _, k := range sortedKeys(raw.Vars) {
		v := raw.Vars[k]
		if err := validateVarValue(v); err != nil {
			errs = append(errs, fmt.Errorf("%s: vars.%s: %w", FileName, k, err))
			continue
		}
		m.Vars[k] = v
	}
	for _, k := range sortedKeys(local.Vars) {
		v := local.Vars[k]
		_, declared := raw.Vars[k]
		if _, ok := DefaultVars[k]; !ok && !declared {
			errs = append(errs, fmt.Errorf("%s: vars.%s: the variable is not declared in %s; %s can only override values", LocalFileName, k, FileName, LocalFileName))
			continue
		}
		if err := validateVarValue(v); err != nil {
			errs = append(errs, fmt.Errorf("%s: vars.%s: %w", LocalFileName, k, err))
			continue
		}
		m.Vars[k] = v
	}
	if len(errs) > 0 {
		// Paths cannot be resolved reliably with broken variables.
		return errors.Join(errs...)
	}

	vars := m.Vars
	// Errors in [defaults] are reported once, under defaults.to, and not
	// again for every dependency that uses it.
	var defaultTo []Target
	hasDefaultTo := raw.Defaults.To != nil
	if hasDefaultTo {
		var toErrs []error
		defaultTo, toErrs = m.resolveTo(*raw.Defaults.To, vars)
		for _, e := range toErrs {
			errs = append(errs, fmt.Errorf("%s: defaults.%w", FileName, e))
		}
	}

	for _, name := range sortedKeys(raw.Deps) {
		dep, depErrs := m.buildDep(name, raw.Deps[name], vars, defaultTo, hasDefaultTo)
		for _, e := range depErrs {
			errs = append(errs, fmt.Errorf("%s: deps.%s: %w", FileName, name, e))
		}
		if len(depErrs) == 0 {
			m.Deps = append(m.Deps, dep)
		}
	}
	return errors.Join(errs...)
}

// resolve turns a relative or "~" path into an absolute OS path. Both "/"
// and "\" are separators on every OS, as in validation, so that a manifest
// written on Windows resolves to the same place on Linux and macOS.
func (m *Manifest) resolve(p string) (string, error) {
	p = strings.ReplaceAll(p, `\`, "/")
	if fsutil.HasHomePrefix(p) {
		return fsutil.ExpandHome(p)
	}
	return filepath.Join(m.Dir, filepath.FromSlash(p)), nil
}

// buildDep validates one dependency and resolves its paths.
func (m *Manifest) buildDep(name string, r rawDep, vars map[string]string, defaultTo []Target, hasDefaultTo bool) (Dep, []error) {
	var errs []error
	dep := Dep{Name: name}
	if err := validateName(name); err != nil {
		errs = append(errs, err)
	}

	switch {
	case r.Path != nil && r.Git != nil:
		errs = append(errs, errors.New(`"path" and "git" cannot be used together`))
	case r.Path == nil && r.Git == nil:
		errs = append(errs, errors.New(`either "path" or "git" is required`))
	case r.Path != nil:
		dep.Path = *r.Path
		if r.Subdir != nil {
			errs = append(errs, errors.New(`"subdir" can only be used with "git"`))
		}
		if r.Ref != nil {
			errs = append(errs, errors.New(`"ref" can only be used with "git"`))
		}
		p, err := m.expandPath(dep.Path, vars)
		if err != nil {
			errs = append(errs, fmt.Errorf("path: %w", err))
		}
		dep.PathAbs = p
	default:
		dep.Git = *r.Git
		if err := validateGit(dep.Git); err != nil {
			errs = append(errs, fmt.Errorf("git: %w", err))
		}
		if r.Subdir != nil {
			dep.Subdir = *r.Subdir
		}
		if r.Ref != nil {
			dep.Ref = *r.Ref
		}
	}

	to := defaultTo
	switch {
	case r.To != nil:
		var toErrs []error
		to, toErrs = m.resolveTo(*r.To, vars)
		errs = append(errs, toErrs...)
	case !hasDefaultTo:
		errs = append(errs, errors.New(`"to" is required (set it here or in [defaults])`))
	}
	for _, t := range to {
		t.Path = filepath.Join(t.Dir, name)
		dep.To = append(dep.To, t)
	}
	return dep, errs
}

// resolveTo validates and resolves a "to" list. The returned targets have
// Raw and Dir set; Path is left for the caller, which knows the dependency
// name. The errors start with "to: ".
func (m *Manifest) resolveTo(list []string, vars map[string]string) ([]Target, []error) {
	var errs []error
	if len(list) == 0 {
		errs = append(errs, errors.New("to: must not be empty"))
	}
	var targets []Target
	for _, raw := range list {
		dir, err := m.expandTo(raw, vars)
		if err != nil {
			errs = append(errs, fmt.Errorf("to: %w", err))
			continue
		}
		for _, prev := range targets {
			if fsutil.SamePath(prev.Dir, dir) {
				errs = append(errs, fmt.Errorf("to: %q and %q are the same destination", prev.Raw, raw))
			}
		}
		targets = append(targets, Target{Raw: raw, Dir: dir})
	}
	return targets, errs
}

// expandPath expands variables in a "path" value and resolves it.
func (m *Manifest) expandPath(p string, vars map[string]string) (string, error) {
	if p == "" {
		return "", errors.New("must not be empty")
	}
	if fsutil.IsAbs(p) {
		return "", fmt.Errorf("%q is an absolute path; use a path relative to %s, a path starting with ~, or a variable", p, FileName)
	}
	if err := checkHomeForm(p); err != nil {
		return "", err
	}
	s, err := expand(p, vars)
	if err != nil {
		return "", fmt.Errorf("%q: %w", p, err)
	}
	return m.resolve(s)
}

// expandTo validates a "to" entry, expands its variables and resolves it.
func (m *Manifest) expandTo(to string, vars map[string]string) (string, error) {
	if err := checkTo(to, vars); err != nil {
		return "", fmt.Errorf("%q: %w", to, err)
	}
	s, err := expand(to, vars)
	if err != nil {
		return "", fmt.Errorf("%q: %w", to, err)
	}
	return m.resolve(s)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
