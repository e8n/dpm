package manifest

import (
	"errors"
	"fmt"
	"strings"

	"github.com/e8n/dpm/internal/fsutil"
)

// validateVarValue checks a [vars] value: it must be a path relative to the
// manifest's directory or a path starting with "~", and it cannot refer to
// other variables.
func validateVarValue(v string) error {
	switch {
	case v == "":
		return errors.New(`must not be empty (use "." for the manifest's directory)`)
	case strings.Contains(v, "${"):
		return fmt.Errorf("%q: a variable cannot refer to other variables", v)
	case fsutil.IsAbs(v):
		return fmt.Errorf("%q is an absolute path; use a path relative to %s or a path starting with ~", v, FileName)
	}
	return checkHomeForm(v)
}

// checkHomeForm rejects forms such as "~user", which look like a home
// directory reference but are not one.
func checkHomeForm(p string) error {
	if strings.HasPrefix(p, "~") && !fsutil.HasHomePrefix(p) {
		return fmt.Errorf(`%q: only "~" and "~/" are supported for the home directory`, p)
	}
	return nil
}

// validateName checks a dependency name. The name becomes a directory name
// under each destination, so it must be a single path element. Names that
// start with "_" are reserved for local files that dpm never touches.
func validateName(name string) error {
	switch {
	case name == "":
		return errors.New("the dependency name must not be empty")
	case name == "." || name == "..":
		return fmt.Errorf("the dependency name %q is not allowed", name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("the dependency name %q must not contain a path separator", name)
	case strings.HasPrefix(name, "_"):
		return fmt.Errorf("the dependency name %q must not start with \"_\" (reserved for local files)", name)
	}
	return nil
}

// validateGit checks a "git" value: a URL ("<scheme>://...") or an
// scp-like address ("[user@]host:path"), as git itself reads them. Local
// paths are rejected; a local directory is written with "path" (spec
// 3.4). A host of one letter is not an scp-like address, so that a Windows
// path such as "C:\repo" or "C:/repo" is rejected as a local path.
func validateGit(v string) error {
	if v == "" {
		return errors.New("must not be empty")
	}
	if isURL(v) || isSCPLike(v) {
		return nil
	}
	return fmt.Errorf(`%q is not a URL or an scp-like address ([user@]host:path); use "path" for a local directory`, v)
}

// isURL reports whether v starts with "<scheme>://", where the scheme is a
// letter followed by letters, digits, "+", "-" or ".".
func isURL(v string) bool {
	scheme, _, ok := strings.Cut(v, "://")
	if !ok || scheme == "" || !isASCIILetter(scheme[0]) {
		return false
	}
	for i := 1; i < len(scheme); i++ {
		c := scheme[i]
		if !isASCIILetter(c) && !('0' <= c && c <= '9') && c != '+' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

// isSCPLike reports whether v is "[user@]host:path": a ":" before any
// "/", and a host of two characters or more.
func isSCPLike(v string) bool {
	before, _, ok := strings.Cut(v, ":")
	if !ok || strings.Contains(before, "/") {
		return false
	}
	host := before[strings.LastIndexByte(before, '@')+1:]
	return len(host) >= 2
}

func isASCIILetter(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// checkTo checks the form of a "to" entry: it starts with a declared
// variable, the variable is followed by a separator or nothing, and neither
// another variable nor ".." appears after it. The leading variable alone
// decides where dpm may write; only [vars] can point outside it.
func checkTo(to string, vars map[string]string) error {
	if !strings.HasPrefix(to, "${") {
		return errors.New("must start with a variable such as ${claude}")
	}
	end := strings.IndexByte(to, '}')
	if end < 0 {
		return errors.New(`unterminated "${"`)
	}
	name := to[2:end]
	if _, ok := vars[name]; !ok {
		return undefinedVarError(name)
	}
	rest := to[end+1:]
	if rest != "" && rest[0] != '/' && rest[0] != '\\' {
		return fmt.Errorf("the variable ${%s} must be followed by a path separator", name)
	}
	if strings.Contains(rest, "${") {
		return errors.New("only the first element of \"to\" can be a variable")
	}
	for _, elem := range strings.FieldsFunc(rest, isSeparator) {
		if elem == ".." {
			return errors.New(`".." cannot be used in "to"; declare a variable in [vars] instead`)
		}
	}
	return nil
}

func isSeparator(r rune) bool { return r == '/' || r == '\\' }

// expand replaces each ${name} in s with the value of the variable.
func expand(s string, vars map[string]string) (string, error) {
	var b strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			b.WriteString(s)
			return b.String(), nil
		}
		b.WriteString(s[:i])
		end := strings.IndexByte(s[i:], '}')
		if end < 0 {
			return "", errors.New(`unterminated "${"`)
		}
		name := s[i+2 : i+end]
		if name == "" {
			return "", errors.New(`empty variable name "${}"`)
		}
		v, ok := vars[name]
		if !ok {
			return "", undefinedVarError(name)
		}
		b.WriteString(v)
		s = s[i+end+1:]
	}
}

func undefinedVarError(name string) error {
	if name == "" {
		return errors.New(`empty variable name "${}"`)
	}
	return fmt.Errorf("undefined variable ${%s} (declare it in [vars])", name)
}
