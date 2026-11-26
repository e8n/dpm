package source

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/e8n/dpm/internal/gittree"
	"github.com/e8n/dpm/internal/manifest"
)

// fetchKey identifies one fetch: dependencies with the same repository and
// ref share it (implementation spec 2.2.1).
type fetchKey struct {
	url string
	ref string
}

// fetchGits fetches the Git dependencies deps[i] for i in idx into out[i].
//
// Each repository and ref is fetched once, in the order in which they first
// appear, into its own temporary repository that is removed afterwards
// (implementation spec 2.2.1: no cache, no checkout). The object format of
// a repository is looked up once.
func fetchGits(deps []manifest.Dep, idx []int, out []*Source) error {
	var keys []fetchKey
	groups := map[fetchKey][]int{}
	for _, i := range idx {
		k := fetchKey{url: deps[i].Git, ref: deps[i].Ref}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], i)
	}
	formats := map[string]gittree.Algorithm{}
	for _, k := range keys {
		if err := fetchGroup(deps, k, groups[k], formats, out); err != nil {
			return err
		}
	}
	return nil
}

// fetchGroup fetches the dependencies deps[i] for i in group, which all
// have the repository and ref of k. formats caches the object format of
// each repository.
func fetchGroup(deps []manifest.Dep, k fetchKey, group []int, formats map[string]gittree.Algorithm, out []*Source) error {
	names := make([]string, len(group))
	for n, i := range group {
		names[n] = fmt.Sprintf("%q", deps[i].Name)
	}
	label := "dependency " + names[0]
	if len(names) > 1 {
		label = "dependencies " + strings.Join(names, ", ")
	}

	tmp, err := os.MkdirTemp("", "dpm-fetch-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	a, ok := formats[k.url]
	if !ok {
		a, err = remoteFormat(tmp, k.url, k.ref)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		formats[k.url] = a
	}
	g := gittree.Git{Dir: tmp}
	commit, err := fetchCommit(g, a, k.url, k.ref)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}

	subdirs := make([]string, len(group))
	for n, i := range group {
		subdirs[n] = deps[i].Subdir
	}
	trees, err := gittree.ReadTrees(g, a, commit, subdirs)
	if err != nil {
		// Find the dependency whose directory is the problem.
		for _, i := range group {
			if _, err1 := gittree.ReadTrees(g, a, commit, []string{deps[i].Subdir}); err1 != nil {
				return depError(deps[i].Name, subdirError(deps[i], commit, err1))
			}
		}
		return fmt.Errorf("%s: %w", label, err)
	}
	var hashes []string
	for n, i := range group {
		if err := checkTree(trees[n], location(deps[i], commit)); err != nil {
			return depError(deps[i].Name, err)
		}
		hashes = append(hashes, entryHashes(trees[n].Entries)...)
	}
	blobs, err := readBlobs(g, hashes)
	if err != nil {
		return fmt.Errorf("%s: reading files fetched from %s: %w", label, k.url, err)
	}
	for n, i := range group {
		out[i] = &Source{Dep: deps[i].Name, Tree: trees[n], Commit: commit, blobs: blobs}
	}
	return nil
}

// location describes the directory of a Git dependency in messages.
func location(d manifest.Dep, commit string) string {
	sub := d.Subdir
	if sub == "" {
		sub = "the root"
	} else {
		sub = fmt.Sprintf("subdir %q", sub)
	}
	return fmt.Sprintf("%s of %s at %s", sub, d.Git, commit)
}

// subdirError describes an error of gittree.ReadTrees for the directory of
// d.
func subdirError(d manifest.Dep, commit string, err error) error {
	if errors.Is(err, gittree.ErrNotFound) {
		return fmt.Errorf("subdir %q does not exist in %s at %s", d.Subdir, d.Git, commit)
	}
	return fmt.Errorf("%s: %w", location(d, commit), err)
}

// remoteFormat returns the object format of the repository at url.
//
// "git fetch" fails when the object formats of the local and the remote
// repository differ, and "git init" needs the format before the fetch. A
// ref that is a full commit hash tells the format by its length. Otherwise
// it is read from the length of the hashes that "git ls-remote" prints for
// HEAD and ref; git 2.34 can run it outside a repository for both formats.
// When the remote has neither (an empty repository), SHA-1 is assumed and
// the fetch reports the error.
//
// dir is an empty directory to run git in.
func remoteFormat(dir, url, ref string) (gittree.Algorithm, error) {
	if isCommitHash(ref) {
		if len(ref) == gittree.SHA256.HexLen() {
			return gittree.SHA256, nil
		}
		return gittree.SHA1, nil
	}
	// Do not let git find a repository above dir: ls-remote would then use
	// that repository's object format.
	g := gittree.Git{Dir: dir, Env: []string{"GIT_CEILING_DIRECTORIES=" + filepath.Dir(dir)}}
	args := []string{"ls-remote", "--", url, "HEAD"}
	if ref != "" {
		args = append(args, ref)
	}
	out, err := g.Run(args...)
	if err != nil {
		return "", fmt.Errorf("reading the refs of %s: %w", url, err)
	}
	line, _, _ := strings.Cut(string(out), "\n")
	hash, _, _ := strings.Cut(line, "\t")
	switch len(hash) {
	case 0, gittree.SHA1.HexLen():
		return gittree.SHA1, nil
	case gittree.SHA256.HexLen():
		return gittree.SHA256, nil
	}
	return "", fmt.Errorf("unexpected output of git ls-remote for %s: %q", url, line)
}

// isCommitHash reports whether ref is a full SHA-1 or SHA-256 commit hash
// in lowercase hexadecimal.
func isCommitHash(ref string) bool {
	if len(ref) != gittree.SHA1.HexLen() && len(ref) != gittree.SHA256.HexLen() {
		return false
	}
	for _, c := range ref {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// onFetch is called with the repository and ref before every "git fetch",
// including the fetch of all branches and tags that fetchCommit falls back
// to. Tests use it to count fetches.
var onFetch = func(url, ref string) {}

// accessErrorMessages lists messages that git prints on standard error when
// it cannot authenticate to or connect to a remote repository (from git
// itself, curl and ssh). Fetching again in another way fails the same way.
var accessErrorMessages = []string{
	// Authentication.
	"Authentication failed",
	"could not read Username",
	"could not read Password",
	"terminal prompts disabled",
	"Permission denied (publickey",
	"Host key verification failed",
	// Connection.
	"Could not resolve host", // also matches ssh's "Could not resolve hostname"
	"Failed to connect",
	"Connection refused",
	"Connection timed out",
	"Network is unreachable",
	// curl's failures. Only the fatal error: git also warns "warning:
	// unable to access '<file>'" about configuration files it cannot read.
	"fatal: unable to access '",
}

// isAccessError reports whether err is a git command that failed because
// the remote repository could not be authenticated to or connected to,
// judged by the messages in accessErrorMessages.
//
// Only English messages are recognized. When git prints translated
// messages (LC_ALL, LANG and so on select another language), or a message
// is not in the list, the error is not recognized and the caller treats it
// like any other failure: fetchCommit then retries, which fails again and
// reports both errors. The result is not wrong, only the error repeats.
// The same happens for failures that git reports only as "Could not read
// from remote repository" (an ssh user without access to the repository,
// for example): that message is also printed for a missing repository, so
// it is not in the list.
func isAccessError(err error) bool {
	var gitErr *gittree.Error
	if !errors.As(err, &gitErr) {
		return false
	}
	for _, m := range accessErrorMessages {
		if strings.Contains(gitErr.Stderr, m) {
			return true
		}
	}
	return false
}

// fetchCommit creates an empty bare repository with object format a in the
// directory of g, fetches ref ("" for the remote's HEAD) from url with
// depth 1, and returns the commit that ref resolved to.
//
// A server may refuse to send a commit by its hash (protocol v0 without
// uploadpack.allowReachableSHA1InWant, for example). When ref is a commit
// hash and the shallow fetch fails, all branches and tags are fetched with
// their history instead and the commit is looked up among them. This is not
// done when the shallow fetch failed to authenticate or to connect
// (isAccessError): the second fetch would fail the same way, and ask for
// the credentials again.
func fetchCommit(g gittree.Git, a gittree.Algorithm, url, ref string) (string, error) {
	// No templates: nothing such as hooks is copied into the repository.
	if _, err := g.Run("init", "-q", "--bare", "--template=", "--object-format="+string(a), "."); err != nil {
		return "", err
	}
	rev := ref
	if rev == "" {
		rev = "HEAD"
	}
	onFetch(url, ref)
	_, err := g.Run("fetch", "-q", "--depth", "1", "--no-tags", "--", url, rev)
	if err == nil {
		commit, err := verifyCommit(g, "FETCH_HEAD")
		if err != nil {
			return "", fmt.Errorf("%s of %s: %w", rev, url, err)
		}
		return commit, nil
	}
	if !isCommitHash(ref) || isAccessError(err) {
		return "", fmt.Errorf("fetching %s from %s: %w", rev, url, err)
	}
	shallowErr := err
	onFetch(url, ref)
	if _, err := g.Run("fetch", "-q", "--no-tags", "--", url,
		"+refs/heads/*:refs/dpm/heads/*", "+refs/tags/*:refs/dpm/tags/*"); err != nil {
		return "", fmt.Errorf("fetching %s from %s: %w (fetching all branches and tags instead also failed: %v)", ref, url, shallowErr, err)
	}
	commit, err := verifyCommit(g, ref)
	if err != nil {
		return "", fmt.Errorf("commit %s was not found in the branches and tags of %s (fetching it directly failed: %v)", ref, url, shallowErr)
	}
	return commit, nil
}

// verifyCommit returns the commit that rev points to.
func verifyCommit(g gittree.Git, rev string) (string, error) {
	out, err := g.Run("rev-parse", "--verify", "-q", rev+"^{commit}")
	if err != nil {
		return "", errors.New("does not point to a commit")
	}
	return strings.TrimSpace(string(out)), nil
}
