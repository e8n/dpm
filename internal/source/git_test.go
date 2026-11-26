package source

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e8n/dpm/internal/fsutil"
	"github.com/e8n/dpm/internal/gittree"
	"github.com/e8n/dpm/internal/manifest"
)

// depSpec describes a dependency for deps.
type depSpec struct {
	name   string
	git    string
	subdir string
	ref    string
	path   string // absolute OS path of a path dependency
}

// deps returns manifest dependencies for the specs.
func deps(specs ...depSpec) []manifest.Dep {
	out := make([]manifest.Dep, len(specs))
	for i, s := range specs {
		out[i] = manifest.Dep{Name: s.name, Git: s.git, Subdir: s.subdir, Ref: s.ref, Path: s.path, PathAbs: s.path}
	}
	return out
}

// countFetches counts the fetches made during the test.
func countFetches(t *testing.T) *int {
	t.Helper()
	n := 0
	saved := onFetch
	onFetch = func(string, string) { n++ }
	t.Cleanup(func() { onFetch = saved })
	return &n
}

// remoteRepo is a repository with tags, a branch and several commits.
type remoteRepo struct {
	dir    string
	url    string
	first  string // commit of the tag v1 (not a tip of any branch)
	second string // commit of the tag v2 (the tip of the branch "dev")
	head   string // commit of HEAD (the default branch)
	algo   gittree.Algorithm
}

func newRemote(t *testing.T, a gittree.Algorithm) remoteRepo {
	t.Helper()
	dir := newRepo(t, a)
	r := remoteRepo{dir: dir, url: fileURL(dir), algo: a}
	writeFiles(t, dir, []file{
		{path: "README.md", data: "readme\n"},
		{path: "skills/a/SKILL.md", data: "a v1\n"},
		{path: "skills/a/run.sh", data: "#!/bin/sh\n", exec: true},
		{path: "skills/b/SKILL.md", data: "b v1\n"},
	})
	r.first = commitAll(t, dir)
	git(t, dir, "tag", "-a", "-m", "v1", "v1")
	writeFiles(t, dir, []file{{path: "skills/a/SKILL.md", data: "a v2\n"}})
	r.second = commitAll(t, dir)
	git(t, dir, "tag", "v2")
	git(t, dir, "branch", "dev")
	writeFiles(t, dir, []file{{path: "skills/a/SKILL.md", data: "a v3\n"}})
	r.head = commitAll(t, dir)
	return r
}

func TestFetchGitRefs(t *testing.T) {
	for _, a := range []gittree.Algorithm{gittree.SHA1, gittree.SHA256} {
		t.Run(string(a), func(t *testing.T) {
			r := newRemote(t, a)
			tests := []struct {
				name       string
				subdir     string
				ref        string
				wantCommit string
			}{
				{"annotated tag", "skills/a", "v1", r.first},
				{"lightweight tag", "skills/a", "v2", r.second},
				{"branch", "skills/a", "dev", r.second},
				{"omitted ref", "skills/a", "", r.head},
				{"commit hash", "skills/a", r.first, r.first},
				{"tip commit hash", "skills/a", r.head, r.head},
				{"root", "", "v1", r.first},
				{"nested subdir", "skills", "v2", r.second},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					s := fetchOne(t, depSpec{name: "x", git: r.url, subdir: tt.subdir, ref: tt.ref})
					if s.Commit != tt.wantCommit {
						t.Errorf("commit %s, want %s", s.Commit, tt.wantCommit)
					}
					want := gittree.FormatIntegrity(a, revParse(t, r.dir, tt.wantCommit+":"+tt.subdir))
					if got := s.Integrity(); got != want {
						t.Errorf("integrity %s, want %s", got, want)
					}
					if len(s.Warnings) != 0 {
						t.Errorf("warnings %q, want none", s.Warnings)
					}
					for _, e := range s.Tree.Entries {
						data, err := s.blob(e)
						if err != nil {
							t.Fatal(err)
						}
						if got := gittree.BlobHash(a, data); got != e.Hash {
							t.Errorf("%s: contents hash to %s, want %s", e.Path, got, e.Hash)
						}
					}
				})
			}
		})
	}
}

func TestFetchGitOncePerRepositoryAndRef(t *testing.T) {
	r := newRemote(t, gittree.SHA1)
	n := countFetches(t)
	srcs, err := Fetch(deps(
		depSpec{name: "a", git: r.url, subdir: "skills/a", ref: "v1"},
		depSpec{name: "b", git: r.url, subdir: "skills/b", ref: "v1"},
		depSpec{name: "a2", git: r.url, subdir: "skills/a", ref: "dev"},
		depSpec{name: "root", git: r.url, ref: "v1"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if *n != 2 {
		t.Errorf("%d fetches, want 2 (v1 and dev)", *n)
	}
	wants := []struct{ name, rev string }{
		{"a", r.first + ":skills/a"},
		{"b", r.first + ":skills/b"},
		{"a2", r.second + ":skills/a"},
		{"root", r.first + ":"},
	}
	for i, w := range wants {
		if srcs[i].Dep != w.name {
			t.Errorf("result %d is %q, want %q", i, srcs[i].Dep, w.name)
		}
		if got, want := srcs[i].Tree.Hash, revParse(t, r.dir, w.rev); got != want {
			t.Errorf("%s: tree %s, want %s", w.name, got, want)
		}
	}
}

// TestFetchGitCommitFallback makes the server refuse to send a commit that
// is not the tip of a ref (protocol v0 without
// uploadpack.allowReachableSHA1InWant), so that the shallow fetch fails and
// the full fetch is used.
func TestFetchGitCommitFallback(t *testing.T) {
	r := newRemote(t, gittree.SHA1)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.version")
	t.Setenv("GIT_CONFIG_VALUE_0", "0")

	// The shallow fetch of the commit alone is refused.
	probe := realTempDir(t)
	git(t, probe, "init", "-q", "--bare")
	if _, err := (gittree.Git{Dir: probe}).Run("fetch", "-q", "--depth", "1", "--", r.url, r.first); err == nil {
		t.Fatal("the server sent a commit that is not a tip; the fallback is not tested")
	}

	n := countFetches(t)
	s := fetchOne(t, depSpec{name: "x", git: r.url, subdir: "skills/a", ref: r.first})
	if s.Commit != r.first {
		t.Errorf("commit %s, want %s", s.Commit, r.first)
	}
	if got, want := s.Tree.Hash, revParse(t, r.dir, r.first+":skills/a"); got != want {
		t.Errorf("tree %s, want %s", got, want)
	}
	if *n != 2 {
		t.Errorf("%d fetches, want 2 (the commit, then all branches and tags)", *n)
	}

	// A commit that does not exist is reported after the full fetch.
	*n = 0
	missing := "0123456789abcdef0123456789abcdef01234567"
	_, err := Fetch(deps(depSpec{name: "x", git: r.url, subdir: "skills/a", ref: missing}))
	wantErr(t, err, `"x"`, missing, "was not found")
	if *n != 2 {
		t.Errorf("%d fetches, want 2 (the commit, then all branches and tags)", *n)
	}
}

func TestIsAccessError(t *testing.T) {
	gitErr := func(stderr string) error {
		return &gittree.Error{Args: []string{"fetch"}, ExitCode: 128, Stderr: stderr, Err: errors.New("exit status 128")}
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"http authentication", gitErr("remote: Invalid username or password.\nfatal: Authentication failed for 'https://example.com/a.git/'"), true},
		{"no terminal for the username", gitErr("fatal: could not read Username for 'https://example.com': terminal prompts disabled"), true},
		{"no terminal for the password", gitErr("fatal: could not read Password for 'https://u@example.com': No such device or address"), true},
		{"ssh key", gitErr("git@example.com: Permission denied (publickey).\nfatal: Could not read from remote repository."), true},
		{"ssh host key", gitErr("Host key verification failed.\nfatal: Could not read from remote repository."), true},
		{"unknown host", gitErr("fatal: unable to access 'https://nohost.invalid/a.git/': Could not resolve host: nohost.invalid"), true},
		{"unknown ssh host", gitErr("ssh: Could not resolve hostname nohost.invalid: Name or service not known"), true},
		{"closed port", gitErr("fatal: unable to access 'http://127.0.0.1:1/a.git/': Failed to connect to 127.0.0.1 port 1: Connection refused"), true},
		{"ssh connection refused", gitErr("ssh: connect to host example.com port 22: Connection refused"), true},
		{"ssh timeout", gitErr("ssh: connect to host example.com port 22: Connection timed out"), true},
		{"unreachable network", gitErr("ssh: connect to host example.com port 22: Network is unreachable"), true},
		{"http error", gitErr("fatal: unable to access 'https://example.com/a.git/': The requested URL returned error: 403"), true},
		{"warning about a configuration file", gitErr("warning: unable to access '/home/u/.config/git/attributes': Permission denied\nerror: Server does not allow request for unadvertised object 0123456789abcdef0123456789abcdef01234567"), false},
		{"hash refused", gitErr("error: Server does not allow request for unadvertised object 0123456789abcdef0123456789abcdef01234567"), false},
		{"missing commit", gitErr("fatal: remote error: upload-pack: not our ref 0123456789abcdef0123456789abcdef01234567"), false},
		{"missing repository", gitErr("fatal: '/none' does not appear to be a git repository\nfatal: Could not read from remote repository."), false},
		{"translated message", gitErr("fatal: Authentifizierung fehlgeschlagen für 'https://example.com/a.git/'"), false},
		{"empty stderr", gitErr(""), false},
		{"not a git error", errors.New("Authentication failed"), false},
		{"wrapped", fmt.Errorf("fetching: %w", gitErr("fatal: Authentication failed for 'https://example.com/a.git/'")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAccessError(tt.err); got != tt.want {
				t.Errorf("isAccessError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// noCredentials makes git fail instead of asking for credentials: no
// credential helper is configured (TestMain), no askpass program is used and
// terminal prompts are disabled. A proxy is not used either. Messages are
// not translated, since only English messages are recognized.
func noCredentials(t *testing.T) {
	t.Helper()
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANGUAGE", "")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_ASKPASS", "")
	t.Setenv("SSH_ASKPASS", "")
	for _, k := range []string{"no_proxy", "NO_PROXY"} {
		t.Setenv(k, "*")
	}
}

// closedPortURL returns an http URL on the loopback interface where nothing
// listens.
func closedPortURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + l.Addr().String() + "/repo.git"
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return url
}

// TestFetchGitCommitNoFallbackOnAccessError checks that a commit hash is
// not fetched again with all branches and tags when the first fetch fails
// to authenticate or to connect.
func TestFetchGitCommitNoFallbackOnAccessError(t *testing.T) {
	noCredentials(t)
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="dpm test"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(auth.Close)

	hash := "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"authentication", auth.URL + "/repo.git", "could not read Username"},
		{"connection", closedPortURL(t), "unable to access"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := countFetches(t)
			_, err := Fetch(deps(depSpec{name: "x", git: tt.url, ref: hash}))
			wantErr(t, err, `"x"`, hash, tt.url, tt.want)
			if !isAccessError(err) {
				t.Errorf("isAccessError(%v) = false, want true", err)
			}
			if strings.Contains(err.Error(), "fetching all branches and tags") {
				t.Errorf("error %q, want no fallback", err)
			}
			if *n != 1 {
				t.Errorf("%d fetches, want 1", *n)
			}
		})
	}
}

func TestFetchGitErrors(t *testing.T) {
	r := newRemote(t, gittree.SHA1)

	// A repository with a submodule entry (a gitlink).
	sub := newRepo(t, gittree.SHA1)
	writeFiles(t, sub, []file{{path: "skills/s/SKILL.md", data: "s\n"}})
	commitAll(t, sub)
	git(t, sub, "update-index", "--add", "--cacheinfo", "160000,"+r.head+",skills/s/vendor")
	git(t, sub, "commit", "-q", "-m", "submodule")

	tests := []struct {
		name string
		dep  depSpec
		want []string
	}{
		{"missing ref", depSpec{name: "x", git: r.url, ref: "nope"}, []string{`"x"`, "nope"}},
		{"missing subdir", depSpec{name: "x", git: r.url, subdir: "skills/zzz", ref: "v1"}, []string{`"x"`, `subdir "skills/zzz" does not exist`}},
		{"subdir is a file", depSpec{name: "x", git: r.url, subdir: "README.md", ref: "v1"}, []string{`"x"`, "not a directory"}},
		{"subdir with ..", depSpec{name: "x", git: r.url, subdir: "../x"}, []string{`"x"`, "subdir"}},
		{"absolute subdir", depSpec{name: "x", git: r.url, subdir: "/skills"}, []string{`"x"`, "subdir"}},
		{"subdir with backslash", depSpec{name: "x", git: r.url, subdir: `skills\a`}, []string{`"x"`, "subdir"}},
		{"subdir with trailing slash", depSpec{name: "x", git: r.url, subdir: "skills/"}, []string{`"x"`, "subdir"}},
		{"refspec as ref", depSpec{name: "x", git: r.url, ref: "v1:refs/heads/x"}, []string{`"x"`, "ref"}},
		{"missing repository", depSpec{name: "x", git: fileURL(filepath.Join(realTempDir(t), "none"))}, []string{`"x"`}},
		{"submodule", depSpec{name: "x", git: fileURL(sub), subdir: "skills/s"}, []string{`"x"`, "vendor", "submodule"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Fetch(deps(tt.dep))
			wantErr(t, err, tt.want...)
		})
	}

	// The error names the dependency whose subdir is missing, even when the
	// fetch is shared.
	_, err := Fetch(deps(
		depSpec{name: "good", git: r.url, subdir: "skills/a", ref: "v1"},
		depSpec{name: "bad", git: r.url, subdir: "skills/zzz", ref: "v1"},
	))
	wantErr(t, err, `"bad"`, "skills/zzz")
}

// TestFetchGitRemovesTemporaryDirectories checks that nothing is left in
// the temporary directory after a fetch, whether it succeeds or fails.
func TestFetchGitRemovesTemporaryDirectories(t *testing.T) {
	r := newRemote(t, gittree.SHA1)
	tmp := realTempDir(t)
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, tmp)
	}
	if got := os.TempDir(); !fsutil.SamePath(got, tmp) {
		t.Fatalf("os.TempDir() = %s, want %s", got, tmp)
	}
	tests := []struct {
		name    string
		dep     depSpec
		wantErr bool
	}{
		{"success", depSpec{name: "x", git: r.url, subdir: "skills/a", ref: "v1"}, false},
		{"missing ref", depSpec{name: "x", git: r.url, ref: "nope"}, true},
		{"missing subdir", depSpec{name: "x", git: r.url, subdir: "none", ref: "v1"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Fetch(deps(tt.dep))
			if (err != nil) != tt.wantErr {
				t.Fatalf("error %v, want error: %v", err, tt.wantErr)
			}
			left, err := os.ReadDir(tmp)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range left {
				t.Errorf("left in the temporary directory: %s", e.Name())
			}
		})
	}
}
