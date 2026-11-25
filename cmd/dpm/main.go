// Command dpm deploys drop-in files (such as Claude Code skills) from
// local directories and Git repositories into the places that use them.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
)

// version is set at build time with -ldflags "-X main.version=vX.Y.Z".
var version = ""

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// minGitVersion is the oldest git that dpm supports.
var minGitVersion = [3]int{2, 34, 0}

const usage = `Usage: dpm <command> [options]

Commands:
  install    Deploy the dependencies listed in dpm.toml
  check      Report differences between dpm.toml and the deployed files

Options:
  --version  Print the version and exit
  --help     Print this help and exit
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes dpm with the given arguments and returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "--version", "-version", "version":
		fmt.Fprintf(stdout, "dpm %s\n", currentVersion())
		return exitOK
	case "--help", "-help", "-h", "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "install", "check":
		if err := checkGit(gitVersionOutput); err != nil {
			fmt.Fprintf(stderr, "dpm: %v\n", err)
			return exitError
		}
		fmt.Fprintf(stderr, "dpm: %s is not implemented yet\n", args[0])
		return exitError
	default:
		fmt.Fprintf(stderr, "dpm: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}

// currentVersion returns the version embedded at build time, or the module
// version when installed with "go install".
func currentVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}

// gitVersionOutput runs "git --version" and returns its output.
func gitVersionOutput() (string, error) {
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("git was not found in PATH")
		}
		return "", fmt.Errorf("git --version failed: %w", err)
	}
	return string(out), nil
}

// checkGit verifies that git is available and new enough.
func checkGit(versionOutput func() (string, error)) error {
	required := fmt.Sprintf("git %d.%d or later is required", minGitVersion[0], minGitVersion[1])
	out, err := versionOutput()
	if err != nil {
		return fmt.Errorf("%v (%s)", err, required)
	}
	v, ok := parseGitVersion(out)
	if !ok {
		return fmt.Errorf("cannot parse the git version from %q (%s)", strings.TrimSpace(out), required)
	}
	if compareVersion(v, minGitVersion) < 0 {
		return fmt.Errorf("git %d.%d.%d is too old (%s)", v[0], v[1], v[2], required)
	}
	return nil
}

var gitVersionPattern = regexp.MustCompile(`git version (\d+)\.(\d+)(?:\.(\d+))?`)

// parseGitVersion extracts the version from the output of "git --version",
// such as "git version 2.34.1" or "git version 2.47.1.windows.2".
func parseGitVersion(s string) ([3]int, bool) {
	var v [3]int
	m := gitVersionPattern.FindStringSubmatch(s)
	if m == nil {
		return v, false
	}
	for i := range 3 {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func compareVersion(a, b [3]int) int {
	for i := range 3 {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
