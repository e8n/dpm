// Package fsutil provides path and file system helpers shared by dpm's
// packages: home directory expansion, symbolic link resolution, slash
// normalization for recorded paths, and portable absolute path detection.
//
// Two kinds of paths are kept apart:
//   - OS paths (package path/filepath) are used to access the file system.
//   - Slash paths (package path) are used for recording and comparing, so that
//     the same value is produced on Windows, Linux and macOS.
package fsutil

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// HomeDir returns the user's home directory.
//
// os.UserHomeDir reads %USERPROFILE% on Windows and $HOME elsewhere, which is
// what dpm expands "~" to, so no extra handling is needed.
func HomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine the home directory: %w", err)
	}
	return home, nil
}

// HasHomePrefix reports whether p is "~" or starts with "~/" or "~\".
// Both separators are accepted on every OS so that a manifest shared between
// environments is read the same way. Forms such as "~user" are not home
// references and return false.
func HasHomePrefix(p string) bool {
	if p == "~" {
		return true
	}
	return len(p) >= 2 && p[0] == '~' && (p[1] == '/' || p[1] == '\\')
}

// ExpandHome replaces a leading "~" (see HasHomePrefix) with the home
// directory and returns an OS path. Other paths are returned unchanged.
func ExpandHome(p string) (string, error) {
	if !HasHomePrefix(p) {
		return p, nil
	}
	home, err := HomeDir()
	if err != nil {
		return "", err
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, filepath.FromSlash(p[2:])), nil
}

// IsAbs reports whether p is an absolute path on any of the supported OSes:
// it starts with "/" or "\" (including UNC paths such as "\\server\share" and
// "//server/share"), or with a drive letter such as "C:". A drive-relative
// path such as "C:foo" is also treated as absolute because it does not
// depend only on the base directory. The result does not depend on the OS
// dpm runs on, so a manifest is validated the same way everywhere.
func IsAbs(p string) bool {
	if p == "" {
		return false
	}
	if p[0] == '/' || p[0] == '\\' {
		return true
	}
	if len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]) {
		return true
	}
	return filepath.IsAbs(p)
}

func isASCIILetter(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// RealPath returns the absolute path of p with all symbolic links resolved.
// The path must exist.
func RealPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

// ToSlash cleans the OS path p and returns it with "/" as the separator.
// Use it for paths that are recorded or compared.
func ToSlash(p string) string {
	return path.Clean(filepath.ToSlash(filepath.Clean(p)))
}

// SamePath reports whether the OS paths a and b refer to the same location
// after normalization with ToSlash. The comparison is case-insensitive on
// Windows, where the file system is case-insensitive. Symbolic links are not
// resolved; call RealPath first when that matters.
func SamePath(a, b string) bool {
	return sameSlashPath(ToSlash(a), ToSlash(b), runtime.GOOS == "windows")
}

func sameSlashPath(a, b string, foldCase bool) bool {
	if foldCase {
		return strings.EqualFold(a, b)
	}
	return a == b
}
