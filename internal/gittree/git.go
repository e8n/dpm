package gittree

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Git runs git commands in a directory.
//
// How dpm runs git, so that the result does not depend on the user's git
// configuration in unexpected ways:
//
//   - Configuration that decides what git stores is NOT overridden. In
//     particular "-c core.autocrlf=..." is never passed: the line ending
//     normalization of a work tree is left to the repository's configuration,
//     its .gitattributes and its index, so that dpm computes the same tree as
//     "git add" followed by "git commit" would in that work tree.
//   - Output is read only in machine-readable forms (-z, --porcelain,
//     --batch-check) so that core.quotePath, colors and the locale do not
//     matter. Messages on standard error are used only in errors.
//   - Environment variables that point git at another repository or index
//     (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, ...) are removed from the
//     inherited environment, so that running dpm from a git hook or a shell
//     with those variables set does not change which repository is read.
//     So are the variables that change how pathspecs are interpreted
//     (GIT_GLOB_PATHSPECS, ...): dpm sets GIT_LITERAL_PATHSPECS itself, and
//     git refuses to combine it with the others. Variables in Env are added
//     after that, so Env may set GIT_INDEX_FILE.
//   - Commands that must not fail because of a setting that only produces
//     warnings for the user's own commits pass their own -c options (see
//     the callers).
type Git struct {
	// Dir is the working directory of the command.
	Dir string
	// Env lists additional environment variables as "KEY=VALUE".
	Env []string
}

// strippedEnv lists the environment variables that are not inherited: those
// that select the repository, the work tree or the index, and those that
// change the interpretation of pathspecs.
var strippedEnv = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR",
	"GIT_NAMESPACE",
	"GIT_PREFIX",
	"GIT_LITERAL_PATHSPECS",
	"GIT_GLOB_PATHSPECS",
	"GIT_NOGLOB_PATHSPECS",
	"GIT_ICASE_PATHSPECS",
}

// Error is returned when a git command cannot be started or exits with a
// non-zero status.
type Error struct {
	Args []string
	Dir  string
	// ExitCode is the exit status, or -1 when git did not run.
	ExitCode int
	// Stderr is the standard error of the command, trimmed.
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("git %s (in %s): %v", strings.Join(e.Args, " "), e.Dir, e.Err)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// Run runs git with args and returns its standard output.
func (g Git) Run(args ...string) ([]byte, error) {
	return g.RunInput(nil, args...)
}

// RunInput runs git with args, feeding stdin to it, and returns its
// standard output. A nil stdin means no input.
func (g Git) RunInput(stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.Dir
	cmd.Env = append(cleanEnv(os.Environ()), g.Env...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		e := &Error{
			Args:     args,
			Dir:      g.Dir,
			ExitCode: -1,
			Stderr:   strings.TrimSpace(stderr.String()),
			Err:      err,
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			e.ExitCode = exitErr.ExitCode()
		}
		return nil, e
	}
	return stdout.Bytes(), nil
}

// cleanEnv returns env without the variables in strippedEnv.
func cleanEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if !isStrippedEnv(key) {
			out = append(out, kv)
		}
	}
	return out
}

func isStrippedEnv(key string) bool {
	for _, k := range strippedEnv {
		// Environment variable names are case-insensitive on Windows;
		// git's names are upper case, so compare without case everywhere.
		if strings.EqualFold(key, k) {
			return true
		}
	}
	return false
}

// exitCode returns the exit status of a failed git command, or -1.
func exitCode(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.ExitCode
	}
	return -1
}
