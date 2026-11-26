package manifest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setHome points the home directory at dir for the duration of the test.
func setHome(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	} else {
		t.Setenv("HOME", dir)
	}
}

// realTempDir returns t.TempDir() with symbolic links (and, on Windows,
// short names) resolved, so that it compares equal to paths from Load.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fromRoot builds an OS path from slash-separated elements under root.
func fromRoot(root, rel string) string {
	return filepath.Join(root, filepath.FromSlash(rel))
}

func TestLoadValid(t *testing.T) {
	type wantTarget struct{ raw, dir string } // dir is slash-separated, relative to the root
	type wantDep struct {
		name, path, pathAbs, git, subdir, ref string
		to                                    []wantTarget
	}
	tests := []struct {
		name     string
		manifest string
		local    string // dpm.local.toml; "" means no file
		wantVars map[string]string
		wantDeps []wantDep
	}{
		{
			name:     "empty manifest",
			manifest: "",
			wantVars: map[string]string{"claude": ".claude", "agents": ".agents"},
		},
		{
			name: "defaults and path dependency",
			manifest: `
[defaults]
to = ["${claude}/skills"]

[deps.commit]
path = "../hub/packages/commit"
`,
			wantDeps: []wantDep{{
				name: "commit", path: "../hub/packages/commit", pathAbs: "hub/packages/commit",
				to: []wantTarget{{"${claude}/skills", "project/.claude/skills"}},
			}},
		},
		{
			name: "git dependency with subdir and ref, several destinations",
			manifest: `
[deps.playwright-cli]
git = "https://github.com/microsoft/playwright-cli"
subdir = "skills/playwright-cli"
ref = "v0.1.14"
to = ["${claude}/skills", "${agents}/skills"]
`,
			wantDeps: []wantDep{{
				name: "playwright-cli", git: "https://github.com/microsoft/playwright-cli",
				subdir: "skills/playwright-cli", ref: "v0.1.14",
				to: []wantTarget{
					{"${claude}/skills", "project/.claude/skills"},
					{"${agents}/skills", "project/.agents/skills"},
				},
			}},
		},
		{
			name: "dependency to overrides defaults; deps are sorted by name",
			manifest: `
[defaults]
to = ["${claude}/skills"]

[deps.zeta]
path = "z"

[deps.e8n-bouncer]
path = "b"
to = ["${claude}/hooks"]
`,
			wantDeps: []wantDep{
				{name: "e8n-bouncer", path: "b", pathAbs: "project/b", to: []wantTarget{{"${claude}/hooks", "project/.claude/hooks"}}},
				{name: "zeta", path: "z", pathAbs: "project/z", to: []wantTarget{{"${claude}/skills", "project/.claude/skills"}}},
			},
		},
		{
			name: "variables in vars, path and to",
			manifest: `
[vars]
claude = "../.claude"
hub = "../workspace/hub"
project = "."

[deps.commit]
path = "${hub}/packages/commit"
to = ["${claude}/skills", "${project}/scripts", "${project}"]
`,
			wantVars: map[string]string{"claude": "../.claude", "agents": ".agents", "hub": "../workspace/hub", "project": "."},
			wantDeps: []wantDep{{
				name: "commit", path: "${hub}/packages/commit", pathAbs: "workspace/hub/packages/commit",
				to: []wantTarget{
					{"${claude}/skills", ".claude/skills"},
					{"${project}/scripts", "project/scripts"},
					{"${project}", "project"},
				},
			}},
		},
		{
			name: "home directory",
			manifest: `
[vars]
claude = "~/.claude"
hub = "~"

[deps.commit]
path = "${hub}/hub/commit"
to = ["${claude}/skills"]

[deps.other]
path = "~/other"
to = ["${claude}/skills"]
`,
			wantDeps: []wantDep{
				{name: "commit", path: "${hub}/hub/commit", pathAbs: "home/hub/commit", to: []wantTarget{{"${claude}/skills", "home/.claude/skills"}}},
				{name: "other", path: "~/other", pathAbs: "home/other", to: []wantTarget{{"${claude}/skills", "home/.claude/skills"}}},
			},
		},
		{
			name: "dpm.local.toml overrides declared and default variables",
			manifest: `
[vars]
hub = "../workspace/hub"

[deps.commit]
path = "${hub}/commit"
to = ["${claude}/skills"]
`,
			local: `
[vars]
hub = "../eclipse/hub"
claude = "~/.claude"
`,
			wantVars: map[string]string{"claude": "~/.claude", "agents": ".agents", "hub": "../eclipse/hub"},
			wantDeps: []wantDep{{
				name: "commit", path: "${hub}/commit", pathAbs: "eclipse/hub/commit",
				to: []wantTarget{{"${claude}/skills", "home/.claude/skills"}},
			}},
		},
		{
			name: "empty dpm.local.toml",
			manifest: `
[deps.commit]
path = "commit"
to = ["${claude}/skills"]
`,
			local: "# nothing here\n",
			wantDeps: []wantDep{{
				name: "commit", path: "commit", pathAbs: "project/commit",
				to: []wantTarget{{"${claude}/skills", "project/.claude/skills"}},
			}},
		},
		{
			name: "backslashes are separators on every OS",
			manifest: `
[vars]
claude = '..\.claude'
hub = '~\hub'

[deps.commit]
path = '${hub}\packages\commit'
to = ['${claude}\skills']
`,
			wantDeps: []wantDep{{
				name: "commit", path: `${hub}\packages\commit`, pathAbs: "home/hub/packages/commit",
				to: []wantTarget{{`${claude}\skills`, ".claude/skills"}},
			}},
		},
		{
			name: "dependency names with dots and unicode",
			manifest: `
[defaults]
to = ["${claude}/skills"]

[deps."v1.2"]
path = "a"

[deps."スキル"]
path = "b"
`,
			wantDeps: []wantDep{
				{name: "v1.2", path: "a", pathAbs: "project/a", to: []wantTarget{{"${claude}/skills", "project/.claude/skills"}}},
				{name: "スキル", path: "b", pathAbs: "project/b", to: []wantTarget{{"${claude}/skills", "project/.claude/skills"}}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := realTempDir(t)
			setHome(t, filepath.Join(root, "home"))
			file := filepath.Join(root, "project", FileName)
			writeFile(t, file, tt.manifest)
			if tt.local != "" {
				writeFile(t, filepath.Join(root, "project", LocalFileName), tt.local)
			}

			m, err := Load(file)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if m.File != file {
				t.Errorf("File = %q, want %q", m.File, file)
			}
			if want := filepath.Dir(file); m.Dir != want {
				t.Errorf("Dir = %q, want %q", m.Dir, want)
			}
			wantLocal := ""
			if tt.local != "" {
				wantLocal = filepath.Join(root, "project", LocalFileName)
			}
			if m.LocalFile != wantLocal {
				t.Errorf("LocalFile = %q, want %q", m.LocalFile, wantLocal)
			}
			for k, v := range tt.wantVars {
				if m.Vars[k] != v {
					t.Errorf("Vars[%q] = %q, want %q", k, m.Vars[k], v)
				}
			}
			if tt.wantVars != nil && len(m.Vars) != len(tt.wantVars) {
				t.Errorf("Vars = %v, want %v", m.Vars, tt.wantVars)
			}

			if len(m.Deps) != len(tt.wantDeps) {
				t.Fatalf("got %d deps, want %d: %+v", len(m.Deps), len(tt.wantDeps), m.Deps)
			}
			for i, w := range tt.wantDeps {
				d := m.Deps[i]
				if d.Name != w.name || d.Path != w.path || d.Git != w.git || d.Subdir != w.subdir || d.Ref != w.ref {
					t.Errorf("dep %d = %+v, want %+v", i, d, w)
				}
				if d.IsGit() != (w.git != "") {
					t.Errorf("dep %q: IsGit() = %v", d.Name, d.IsGit())
				}
				wantAbs := ""
				if w.pathAbs != "" {
					wantAbs = fromRoot(root, w.pathAbs)
				}
				if d.PathAbs != wantAbs {
					t.Errorf("dep %q: PathAbs = %q, want %q", d.Name, d.PathAbs, wantAbs)
				}
				if len(d.To) != len(w.to) {
					t.Fatalf("dep %q: got %d targets, want %d", d.Name, len(d.To), len(w.to))
				}
				for j, wt := range w.to {
					got := d.To[j]
					wantDir := fromRoot(root, wt.dir)
					if got.Raw != wt.raw || got.Dir != wantDir || got.Path != filepath.Join(wantDir, w.name) {
						t.Errorf("dep %q: target %d = %+v, want raw %q dir %q", d.Name, j, got, wt.raw, wantDir)
					}
				}
			}
		})
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		local    string
		wantErrs []string // substrings that must all appear in the error
	}{
		{
			name:     "syntax error",
			manifest: "[deps.commit\n",
			wantErrs: []string{"dpm.toml: toml:"},
		},
		{
			name:     "unknown top-level key",
			manifest: "[dep.commit]\npath = \"a\"\n",
			wantErrs: []string{"dpm.toml: unknown keys: dep.commit"},
		},
		{
			name:     "unknown key in a dependency",
			manifest: "[deps.commit]\npath = \"a\"\nto = [\"${claude}/skills\"]\nbranch = \"main\"\n",
			wantErrs: []string{"dpm.toml: unknown keys: deps.commit.branch"},
		},
		{
			name:     "unknown key in defaults",
			manifest: "[defaults]\nref = \"main\"\n",
			wantErrs: []string{"unknown keys: defaults.ref"},
		},
		{
			name:     "to is a string, not an array",
			manifest: "[deps.commit]\npath = \"a\"\nto = \"${claude}/skills\"\n",
			wantErrs: []string{"dpm.toml: toml:", "deps.commit.to"},
		},
		{
			name:     "non-string variable",
			manifest: "[vars]\nhub = 1\n",
			wantErrs: []string{"dpm.toml: toml:", "vars.hub"},
		},
		{
			name:     "to does not start with a variable",
			manifest: "[deps.commit]\npath = \"a\"\nto = [\".claude/skills\"]\n",
			wantErrs: []string{`dpm.toml: deps.commit: to: ".claude/skills": must start with a variable`},
		},
		{
			name:     "to is absolute",
			manifest: "[deps.commit]\npath = \"a\"\nto = [\"/home/x/.claude/skills\"]\n",
			wantErrs: []string{"deps.commit: to:", "must start with a variable"},
		},
		{
			name:     "to uses .. after the variable",
			manifest: "[deps.commit]\npath = \"a\"\nto = [\"${claude}/../skills\"]\n",
			wantErrs: []string{`deps.commit: to: "${claude}/../skills": ".." cannot be used`},
		},
		{
			name:     "to uses .. with a backslash",
			manifest: "[deps.commit]\npath = \"a\"\nto = ['${claude}\\..\\x']\n",
			wantErrs: []string{`".." cannot be used`},
		},
		{
			name:     "to has a variable glued to a name",
			manifest: "[deps.commit]\npath = \"a\"\nto = [\"${claude}skills\"]\n",
			wantErrs: []string{"deps.commit: to:", "must be followed by a path separator"},
		},
		{
			name:     "to uses an undefined variable",
			manifest: "[deps.commit]\npath = \"a\"\nto = [\"${codex}/skills\"]\n",
			wantErrs: []string{"deps.commit: to:", "undefined variable ${codex}"},
		},
		{
			name:     "to uses a second variable",
			manifest: "[vars]\nup = \"../..\"\n[deps.commit]\npath = \"a\"\nto = [\"${claude}/${up}\"]\n",
			wantErrs: []string{`deps.commit: to: "${claude}/${up}": only the first element of "to" can be a variable`},
		},
		{
			name:     "to has an unterminated variable",
			manifest: "[deps.commit]\npath = \"a\"\nto = [\"${claude/skills\"]\n",
			wantErrs: []string{"deps.commit: to:", `unterminated "${"`},
		},
		{
			name:     "to is empty",
			manifest: "[deps.commit]\npath = \"a\"\nto = []\n",
			wantErrs: []string{"deps.commit: to: must not be empty"},
		},
		{
			name:     "to is missing everywhere",
			manifest: "[deps.commit]\npath = \"a\"\n",
			wantErrs: []string{`dpm.toml: deps.commit: "to" is required`},
		},
		{
			name:     "same destination twice",
			manifest: "[deps.commit]\npath = \"a\"\nto = [\"${claude}/skills\", \"${claude}/./skills\"]\n",
			wantErrs: []string{"deps.commit: to:", "are the same destination"},
		},
		{
			name:     "invalid defaults.to is reported once",
			manifest: "[defaults]\nto = [\"skills\"]\n[deps.a]\npath = \"a\"\n[deps.b]\npath = \"b\"\n",
			wantErrs: []string{`dpm.toml: defaults.to: "skills": must start with a variable`},
		},
		{
			name:     "same destination twice in defaults.to",
			manifest: "[defaults]\nto = [\"${claude}/skills\", \"${claude}/./skills\"]\n[deps.a]\npath = \"a\"\n",
			wantErrs: []string{"dpm.toml: defaults.to:", "are the same destination"},
		},
		{
			name:     "path with the home of another user",
			manifest: "[deps.commit]\npath = \"~bob/hub\"\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{"deps.commit: path:", `only "~" and "~/" are supported`},
		},
		{
			name:     "defaults.to is empty",
			manifest: "[defaults]\nto = []\n",
			wantErrs: []string{"dpm.toml: defaults.to: must not be empty"},
		},
		{
			name:     "absolute variable with a slash",
			manifest: "[vars]\nclaude = \"/home/x/.claude\"\n",
			wantErrs: []string{`dpm.toml: vars.claude: "/home/x/.claude" is an absolute path`},
		},
		{
			name:     "absolute variable with a drive letter",
			manifest: "[vars]\nhub = 'C:\\eclipse\\w\\hub'\n",
			wantErrs: []string{"vars.hub:", "is an absolute path"},
		},
		{
			name:     "absolute variable with a UNC path",
			manifest: "[vars]\nhub = '\\\\server\\share'\n",
			wantErrs: []string{"vars.hub:", "is an absolute path"},
		},
		{
			name:     "variable refers to another variable",
			manifest: "[vars]\nhub = \"../hub\"\nskills = \"${hub}/skills\"\n",
			wantErrs: []string{"vars.skills:", "cannot refer to other variables"},
		},
		{
			name:     "empty variable",
			manifest: "[vars]\nproject = \"\"\n",
			wantErrs: []string{"vars.project: must not be empty"},
		},
		{
			name:     "home of another user",
			manifest: "[vars]\nhub = \"~bob/hub\"\n",
			wantErrs: []string{"vars.hub:", `only "~" and "~/" are supported`},
		},
		{
			name:     "path and git together",
			manifest: "[deps.commit]\npath = \"a\"\ngit = \"file:///repo\"\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{`dpm.toml: deps.commit: "path" and "git" cannot be used together`},
		},
		{
			name:     "neither path nor git",
			manifest: "[deps.commit]\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{`dpm.toml: deps.commit: either "path" or "git" is required`},
		},
		{
			name:     "subdir and ref without git",
			manifest: "[deps.commit]\npath = \"a\"\nsubdir = \"x\"\nref = \"v1\"\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{`deps.commit: "subdir" can only be used with "git"`, `deps.commit: "ref" can only be used with "git"`},
		},
		{
			name:     "empty path",
			manifest: "[deps.commit]\npath = \"\"\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{"deps.commit: path: must not be empty"},
		},
		{
			name:     "empty git",
			manifest: "[deps.commit]\ngit = \"\"\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{"deps.commit: git: must not be empty"},
		},
		{
			name:     "git with a local path",
			manifest: "[deps.commit]\ngit = \"../hub\"\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{`deps.commit: git: "../hub" is not a URL or an scp-like address`, `use "path" for a local directory`},
		},
		{
			name:     "absolute path",
			manifest: "[deps.commit]\npath = \"/opt/hub/commit\"\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{`deps.commit: path: "/opt/hub/commit" is an absolute path`},
		},
		{
			name:     "path with an undefined variable",
			manifest: "[deps.commit]\npath = \"${hub}/commit\"\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{"deps.commit: path:", "undefined variable ${hub}"},
		},
		{
			name:     "path with an empty variable name",
			manifest: "[deps.commit]\npath = \"a/${}/b\"\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{"deps.commit: path:", `empty variable name "${}"`},
		},
		{
			name:     "dependency name starting with an underscore",
			manifest: "[deps._local]\npath = \"a\"\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{`deps._local: the dependency name "_local" must not start with "_"`},
		},
		{
			name:     "dependency name with a slash",
			manifest: "[deps.\"a/b\"]\npath = \"a\"\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{"must not contain a path separator"},
		},
		{
			name:     "dependency name with a backslash",
			manifest: "[deps.'a\\b']\npath = \"a\"\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{"must not contain a path separator"},
		},
		{
			name:     "dependency name ..",
			manifest: "[deps.\"..\"]\npath = \"a\"\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{`the dependency name ".." is not allowed`},
		},
		{
			name:     "dependency name .",
			manifest: "[deps.\".\"]\npath = \"a\"\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{`the dependency name "." is not allowed`},
		},
		{
			name:     "empty dependency name",
			manifest: "[deps.\"\"]\npath = \"a\"\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{"the dependency name must not be empty"},
		},
		{
			name:     "several errors are reported together",
			manifest: "[deps.a]\npath = \"a\"\n[deps.b]\ngit = \"file:///r\"\npath = \"b\"\nto = [\"${claude}/x\"]\n",
			wantErrs: []string{`deps.a: "to" is required`, `deps.b: "path" and "git" cannot be used together`},
		},
		{
			name:     "dependency in dpm.local.toml",
			manifest: "",
			local:    "[deps.commit]\npath = \"a\"\n",
			wantErrs: []string{"dpm.local.toml: only [vars] is allowed (found: deps.commit)"},
		},
		{
			name:     "defaults in dpm.local.toml",
			manifest: "",
			local:    "[defaults]\nto = [\"${claude}/skills\"]\n",
			wantErrs: []string{"dpm.local.toml: only [vars] is allowed"},
		},
		{
			name:     "new variable in dpm.local.toml",
			manifest: "",
			local:    "[vars]\nhub = \"../hub\"\n",
			wantErrs: []string{"dpm.local.toml: vars.hub: the variable is not declared in dpm.toml"},
		},
		{
			name:     "absolute variable in dpm.local.toml",
			manifest: "[vars]\nhub = \"../hub\"\n",
			local:    "[vars]\nhub = \"/opt/hub\"\n",
			wantErrs: []string{`dpm.local.toml: vars.hub: "/opt/hub" is an absolute path`},
		},
		{
			name:     "syntax error in dpm.local.toml",
			manifest: "",
			local:    "[vars\n",
			wantErrs: []string{"dpm.local.toml: toml:"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := realTempDir(t)
			setHome(t, filepath.Join(root, "home"))
			file := filepath.Join(root, "project", FileName)
			writeFile(t, file, tt.manifest)
			if tt.local != "" {
				writeFile(t, filepath.Join(root, "project", LocalFileName), tt.local)
			}

			m, err := Load(file)
			if err == nil {
				t.Fatalf("Load succeeded, want an error: %+v", m)
			}
			if m != nil {
				t.Errorf("Load returned a manifest with an error")
			}
			for _, want := range tt.wantErrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to contain %q", err, want)
				}
			}
		})
	}
}

func TestLoadDefaultsErrorReportedOnce(t *testing.T) {
	tests := []struct {
		name, to, want string
	}{
		{"invalid entry", `["skills"]`, "must start with a variable"},
		{"same destination twice", `["${claude}/skills", "${claude}/./skills"]`, "are the same destination"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := realTempDir(t)
			file := filepath.Join(root, FileName)
			writeFile(t, file, "[defaults]\nto = "+tt.to+"\n[deps.a]\npath = \"a\"\n[deps.b]\npath = \"b\"\n")
			_, err := Load(file)
			if err == nil {
				t.Fatal("Load succeeded, want an error")
			}
			if n := strings.Count(err.Error(), tt.want); n != 1 {
				t.Errorf("error = %q, want %q exactly once (got %d)", err, tt.want, n)
			}
		})
	}
}

func TestLoadMissing(t *testing.T) {
	root := realTempDir(t)
	_, err := Load(filepath.Join(root, FileName))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want one that wraps fs.ErrNotExist", err)
	}
}

// TestLoadSymlink checks that paths are resolved against the directory of
// the manifest's real location, and that dpm.local.toml is read from there.
func TestLoadSymlink(t *testing.T) {
	root := realTempDir(t)
	realFile := filepath.Join(root, "repo", FileName)
	writeFile(t, realFile, `
[vars]
hub = "../hub"

[deps.commit]
path = "${hub}/commit"
to = ["${claude}/skills"]
`)
	writeFile(t, filepath.Join(root, "repo", LocalFileName), "[vars]\nhub = \"../other-hub\"\n")
	link := filepath.Join(root, "home", FileName)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realFile, link); err != nil {
		t.Skipf("cannot create a symbolic link (Windows may require a privilege): %v", err)
	}

	m, err := Load(link)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.File != realFile {
		t.Errorf("File = %q, want %q", m.File, realFile)
	}
	if want := filepath.Join(root, "other-hub", "commit"); m.Deps[0].PathAbs != want {
		t.Errorf("PathAbs = %q, want %q", m.Deps[0].PathAbs, want)
	}
	if want := filepath.Join(root, "repo", ".claude", "skills", "commit"); m.Deps[0].To[0].Path != want {
		t.Errorf("To[0].Path = %q, want %q", m.Deps[0].To[0].Path, want)
	}
}

// TestLoadRelativeFile checks that a relative manifest path is resolved
// against the current directory.
func TestLoadRelativeFile(t *testing.T) {
	root := realTempDir(t)
	writeFile(t, filepath.Join(root, FileName), "[deps.commit]\npath = \"a\"\nto = [\"${claude}/skills\"]\n")
	t.Chdir(root)

	m, err := Load(FileName)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Dir != root {
		t.Errorf("Dir = %q, want %q", m.Dir, root)
	}
	if want := filepath.Join(root, "a"); m.Deps[0].PathAbs != want {
		t.Errorf("PathAbs = %q, want %q", m.Deps[0].PathAbs, want)
	}
}

func TestValidateGit(t *testing.T) {
	tests := []struct {
		git string
		ok  bool
	}{
		{"https://github.com/microsoft/playwright-cli", true},
		{"http://example.com/repo.git", true},
		{"ssh://git@example.com:2222/repo.git", true},
		{"git://example.com/repo", true},
		{"file:///home/me/repo", true},
		{"file:///C:/work/repo", true},
		{"git+ssh://example.com/repo", true},
		{"git@github.com:owner/repo.git", true},
		{"github.com:owner/repo", true},
		{"me@host:repo", true},
		{"ab:repo", true},
		{"", false},
		{"../hub", false},
		{"hub", false},
		{"/home/me/repo", false},
		{"~/repo", false},
		{`C:\work\repo`, false},
		{"C:/work/repo", false},
		{"c:repo", false},
		{"git@c:repo", false},
		{`\\server\share\repo`, false},
		{"./a:b", false},
		{"dir/host:repo", false},
		{"://example.com/repo", false},
	}
	for _, tt := range tests {
		err := validateGit(tt.git)
		if (err == nil) != tt.ok {
			t.Errorf("validateGit(%q) = %v, want ok %v", tt.git, err, tt.ok)
		}
	}
}
