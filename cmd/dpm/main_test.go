package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	old := version
	version = "v1.2.3"
	defer func() { version = old }()

	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr.String())
	}
	if got, want := stdout.String(), "dpm v1.2.3\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestRunUsage(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{name: "no arguments", args: nil, wantCode: exitUsage, wantErr: "Usage: dpm"},
		{name: "unknown command", args: []string{"frobnicate"}, wantCode: exitUsage, wantErr: `unknown command "frobnicate"`},
		{name: "help", args: []string{"--help"}, wantCode: exitOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantErr)
			}
		})
	}
}

func TestParseGitVersion(t *testing.T) {
	tests := []struct {
		in     string
		want   [3]int
		wantOK bool
	}{
		{in: "git version 2.34.1\n", want: [3]int{2, 34, 1}, wantOK: true},
		{in: "git version 2.47.1.windows.2\n", want: [3]int{2, 47, 1}, wantOK: true},
		{in: "git version 2.39.5 (Apple Git-154)\n", want: [3]int{2, 39, 5}, wantOK: true},
		{in: "git version 3.0\n", want: [3]int{3, 0, 0}, wantOK: true},
		{in: "something else\n", wantOK: false},
	}
	for _, tt := range tests {
		got, ok := parseGitVersion(tt.in)
		if ok != tt.wantOK || (ok && got != tt.want) {
			t.Errorf("parseGitVersion(%q) = %v, %v; want %v, %v", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestCheckGit(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		err     error
		wantErr string
	}{
		{name: "minimum", output: "git version 2.34.0\n"},
		{name: "newer", output: "git version 2.47.1.windows.2\n"},
		{name: "too old", output: "git version 2.33.8\n", wantErr: "git 2.33.8 is too old (git 2.34 or later is required)"},
		{name: "not found", err: errors.New("git was not found in PATH"), wantErr: "git was not found in PATH (git 2.34 or later is required)"},
		{name: "unparsable", output: "hello\n", wantErr: "cannot parse the git version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkGit(func() (string, error) { return tt.output, tt.err })
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestCheckGitInstalled(t *testing.T) {
	// Tests require git, the same as dpm itself.
	if err := checkGit(gitVersionOutput); err != nil {
		t.Fatal(err)
	}
}
