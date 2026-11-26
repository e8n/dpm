package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/e8n/dpm/internal/apmconv"
	"github.com/e8n/dpm/internal/check"
	"github.com/e8n/dpm/internal/deploy"
	"github.com/e8n/dpm/internal/manifest"
)

// apmLockFileName is the lock file of APM, which dpm never touches.
const apmLockFileName = "apm.lock.yaml"

// The --json output of install is one JSON object on stdout, written
// whether the install succeeds or fails:
//
//	{
//	  "command": "install",
//	  "ok": true,
//	  "manifest": "/home/me/project/dpm.toml",
//	  "generated_from": "/home/me/project/apm.yml",
//	  "results": [
//	    {"dep": "commit", "dir": ".claude/skills/commit", "action": "place",
//	     "integrity": "git-sha1:..."},
//	    {"dep": "playwright-cli", "dir": ".claude/skills/playwright-cli",
//	     "action": "replace", "integrity": "git-sha1:...", "commit": "..."},
//	    {"dep": "old", "dir": ".claude/skills/old", "action": "remove"}
//	  ],
//	  "warnings": [
//	    {"dep": "commit", "message": "..."},
//	    {"message": "..."}
//	  ],
//	  "error": null
//	}
//
//   - command: always "install".
//   - ok: true when the install succeeded. The exit code says the same.
//   - manifest: the absolute path of dpm.toml (symbolic links resolved);
//     omitted when it was not read.
//   - generated_from: the absolute path of apm.yml in the current
//     directory (symbolic links not resolved) when dpm.toml did not
//     exist and was generated from it by this install, before the
//     dependencies were installed. dpm.toml stays even when ok is false.
//     Omitted when dpm.toml was not generated.
//   - results: one entry per directory, in the order of the dependencies
//     (sorted by name) and of their "to" entries, then the directories
//     that were dropped, sorted by dir. Always empty when ok is false: a
//     failed install normally leaves every destination as it was. When
//     restoring the previous contents fails too, the destinations may be
//     left part way, and error says where the previous contents are kept
//     and how to move them back.
//   - results[].dep: the name of the dependency.
//   - results[].dir: the directory as recorded in .dpm/state.toml: relative
//     to the manifest's directory with "/" as the separator, or absolute
//     with "/" when no relative path exists (another drive on Windows).
//   - results[].action: "place" (did not exist), "replace" (placed by dpm
//     before), "adopt" (existed, not recorded, named after the dependency;
//     overwritten), "remove" (recorded but no longer in the manifest;
//     deleted) or "forget" (recorded but already gone, or handled by
//     another entry; only the record was dropped, the disk was not
//     touched).
//   - results[].integrity: the integrity of the deployed contents, now in
//     the record. Omitted for "remove" and "forget".
//   - results[].commit: the commit that the ref of a Git dependency
//     resolved to. Omitted for path dependencies, "remove" and "forget".
//   - warnings: messages for the user, possibly present when ok is false.
//     warnings[].dep is omitted when the message is not about one
//     dependency.
//   - error: the error message when ok is false, otherwise null.
//
// command, ok, results, warnings and error are always present; manifest,
// generated_from and the keys marked "omitted" above may be left out.
//
// Keys are added in later versions only when they do not change the
// meaning of existing ones.

// installReport is the --json output of install.
type installReport struct {
	Command       string          `json:"command"`
	OK            bool            `json:"ok"`
	Manifest      string          `json:"manifest,omitempty"`
	GeneratedFrom string          `json:"generated_from,omitempty"`
	Results       []resultReport  `json:"results"`
	Warnings      []warningReport `json:"warnings"`
	Error         *string         `json:"error"`
}

type resultReport struct {
	Dep       string `json:"dep"`
	Dir       string `json:"dir"`
	Action    string `json:"action"`
	Integrity string `json:"integrity,omitempty"`
	Commit    string `json:"commit,omitempty"`
}

type warningReport struct {
	Dep     string `json:"dep,omitempty"`
	Message string `json:"message"`
}

// report returns the --json form of r.
func (r *installResult) report() installReport {
	out := installReport{
		Command:       "install",
		OK:            r.err == nil,
		Manifest:      r.manifest,
		GeneratedFrom: r.generatedFrom,
		Results:       []resultReport{},
		Warnings:      []warningReport{},
	}
	for _, it := range r.items {
		rr := resultReport{Dep: it.Dep, Dir: it.Dir, Action: it.Action.String()}
		if it.Action.Deploys() {
			if src := r.sources[it.Dep]; src != nil {
				rr.Integrity = src.Integrity()
				rr.Commit = src.Commit
			}
		}
		out.Results = append(out.Results, rr)
	}
	for _, w := range r.warnings {
		out.Warnings = append(out.Warnings, warningReport{Dep: w.dep, Message: w.message})
	}
	if r.err != nil {
		msg := r.err.Error()
		out.Error = &msg
	}
	return out
}

// writeJSON writes v as indented JSON followed by a newline.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// pastTense gives the verb printed for each action that changes the disk.
// Forget is not printed: only the record changes.
var pastTense = map[deploy.Action]string{
	deploy.Place:   "placed",
	deploy.Replace: "replaced",
	deploy.Adopt:   "adopted",
	deploy.Remove:  "removed",
}

// writeText prints r for people: a line when dpm.toml was generated, one
// line per directory that changed and a summary on stdout, warnings and
// the error on stderr.
//
//	generated dpm.toml from apm.yml (apm.yml and apm.lock.yaml are unchanged)
//	placed   commit -> .claude/skills/commit
//	adopted  review -> .claude/skills/review (it was not placed by dpm; overwritten)
//	removed  old -> .claude/skills/old
//	dpm.toml: 1 placed, 1 adopted, 1 removed
func (r *installResult) writeText(stdout, stderr io.Writer) {
	if r.generatedFrom != "" {
		fmt.Fprintf(stdout, "generated %s from %s (%s and %s are unchanged)\n",
			manifest.FileName, apmconv.FileName, apmconv.FileName, apmLockFileName)
	}
	writeWarnings(stderr, r.warnings)
	if r.err != nil {
		fmt.Fprintf(stderr, "dpm: %v\n", r.err)
		return
	}
	counts := map[deploy.Action]int{}
	for _, it := range r.items {
		verb, ok := pastTense[it.Action]
		if !ok {
			continue
		}
		counts[it.Action]++
		note := ""
		if it.Action == deploy.Adopt {
			note = " (it was not placed by dpm; overwritten)"
		}
		fmt.Fprintf(stdout, "%-8s %s -> %s%s\n", verb, it.Dep, it.Dir, note)
	}
	var parts []string
	for _, a := range []deploy.Action{deploy.Place, deploy.Replace, deploy.Adopt, deploy.Remove} {
		if counts[a] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[a], pastTense[a]))
		}
	}
	if len(parts) == 0 {
		fmt.Fprintln(stdout, "dpm.toml: nothing to install")
		return
	}
	fmt.Fprintf(stdout, "dpm.toml: %s\n", strings.Join(parts, ", "))
}

// writeWarnings prints warnings on stderr, naming the dependency when
// there is one.
func writeWarnings(stderr io.Writer, warnings []warning) {
	for _, w := range warnings {
		if w.dep != "" {
			fmt.Fprintf(stderr, "warning: dependency %q: %s\n", w.dep, w.message)
		} else {
			fmt.Fprintf(stderr, "warning: %s\n", w.message)
		}
	}
}

// The --json output of check has the same form as that of install:
//
//	{
//	  "command": "check",
//	  "ok": false,
//	  "manifest": "/home/me/project/dpm.toml",
//	  "results": [
//	    {"dep": "commit", "dir": ".claude/skills/commit", "kind": "modified",
//	     "integrity": "git-sha1:...", "deployed": "git-sha1:..."},
//	    {"dep": "commit", "dir": ".claude/skills/commit", "kind": "outdated",
//	     "integrity": "git-sha1:...", "source": "git-sha1:..."},
//	    {"dep": "browser", "dir": ".claude/skills/browser", "kind": "missing"}
//	  ],
//	  "warnings": [],
//	  "error": null
//	}
//
//   - command: always "check".
//   - ok: true when the check found no differences and no error. The exit
//     code says the same (0, or 1 otherwise). Differences alone leave
//     error null.
//   - manifest: as for install.
//   - results: one entry per difference, in the order of install's
//     results. A directory has more than one entry when it has more than
//     one difference ("modified" and "outdated"). Empty when there are no
//     differences, and always empty when error is not null.
//   - results[].dep, results[].dir: as for install.
//   - results[].kind: "missing" (a destination does not exist; install
//     places it), "modified" (the deployed contents differ from the
//     recorded integrity, ignoring the execute bit), "outdated" (the
//     current contents of the source differ from the recorded integrity;
//     install updates the destination), "leftover" (recorded but no longer
//     in the manifest; install removes it) or "adoptable" (exists, not
//     recorded, named after the dependency; install overwrites it).
//   - results[].integrity: the integrity in .dpm/state.toml. Omitted when
//     the directory is not recorded.
//   - results[].deployed: for "modified", the integrity computed from the
//     deployed directory with the algorithm of the record. Omitted when the
//     destination is not a directory, and for the other kinds.
//   - results[].source: for "outdated", the current integrity of the
//     source. Omitted for the other kinds.
//   - results[].commit: for "outdated" Git dependencies, the commit that
//     the ref resolved to. Omitted otherwise.
//   - warnings, error: as for install.
//
// command, ok, results, warnings and error are always present.

// checkReport is the --json output of check.
type checkReport struct {
	Command  string          `json:"command"`
	OK       bool            `json:"ok"`
	Manifest string          `json:"manifest,omitempty"`
	Results  []diffReport    `json:"results"`
	Warnings []warningReport `json:"warnings"`
	Error    *string         `json:"error"`
}

type diffReport struct {
	Dep       string `json:"dep"`
	Dir       string `json:"dir"`
	Kind      string `json:"kind"`
	Integrity string `json:"integrity,omitempty"`
	Deployed  string `json:"deployed,omitempty"`
	Source    string `json:"source,omitempty"`
	Commit    string `json:"commit,omitempty"`
}

// report returns the --json form of r.
func (r *checkResult) report() checkReport {
	out := checkReport{
		Command:  "check",
		OK:       r.err == nil && len(r.diffs) == 0,
		Manifest: r.manifest,
		Results:  []diffReport{},
		Warnings: []warningReport{},
	}
	for _, d := range r.diffs {
		dr := diffReport{
			Dep:      d.Item.Dep,
			Dir:      d.Item.Dir,
			Kind:     d.Kind.String(),
			Deployed: d.Deployed,
			Source:   d.Source,
			Commit:   d.Commit,
		}
		if d.Item.Record != nil {
			dr.Integrity = d.Item.Record.Integrity
		}
		out.Results = append(out.Results, dr)
	}
	for _, w := range r.warnings {
		out.Warnings = append(out.Warnings, warningReport{Dep: w.dep, Message: w.message})
	}
	if r.err != nil {
		msg := r.err.Error()
		out.Error = &msg
	}
	return out
}

// diffNotes explains each kind of difference in the text output.
var diffNotes = map[check.Kind]string{
	check.Missing:   "not deployed",
	check.Modified:  "edited since it was deployed",
	check.Outdated:  "the source has changed",
	check.Leftover:  "no longer in dpm.toml",
	check.Adoptable: "not placed by dpm",
}

// writeText prints r for people: one line per difference and a summary on
// stdout, warnings and the error on stderr.
//
//	modified  commit -> .claude/skills/commit (edited since it was deployed)
//	outdated  commit -> .claude/skills/commit (the source has changed)
//	leftover  old -> .claude/skills/old (no longer in dpm.toml)
//	dpm.toml: 1 modified, 1 outdated, 1 leftover; "dpm install" updates them, overwriting the local edits
func (r *checkResult) writeText(stdout, stderr io.Writer) {
	writeWarnings(stderr, r.warnings)
	if r.err != nil {
		fmt.Fprintf(stderr, "dpm: %v\n", r.err)
		return
	}
	counts := map[check.Kind]int{}
	for _, d := range r.diffs {
		counts[d.Kind]++
		fmt.Fprintf(stdout, "%-9s %s -> %s (%s)\n", d.Kind, d.Item.Dep, d.Item.Dir, diffNotes[d.Kind])
	}
	var parts []string
	for _, k := range []check.Kind{check.Missing, check.Modified, check.Outdated, check.Leftover, check.Adoptable} {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
		}
	}
	if len(parts) == 0 {
		fmt.Fprintln(stdout, "dpm.toml: up to date")
		return
	}
	note := ""
	if counts[check.Modified] > 0 {
		note = ", overwriting the local edits"
	}
	fmt.Fprintf(stdout, "dpm.toml: %s; \"dpm install\" updates them%s\n", strings.Join(parts, ", "), note)
}
