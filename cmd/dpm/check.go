package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/e8n/dpm/internal/check"
	"github.com/e8n/dpm/internal/deploy"
	"github.com/e8n/dpm/internal/source"
	"github.com/e8n/dpm/internal/state"
)

const checkUsage = `Usage: dpm check [--json]

Report differences between dpm.toml in the current directory, the deployed
directories and the current contents of the sources. Nothing is changed.
The exit code is 1 when there are differences.

Options:
  --json     Print the result as JSON
`

// runCheck runs "dpm check" with the arguments after the command name.
func runCheck(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("check", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	jsonOut := fl.Bool("json", false, "")
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, checkUsage)
			return exitOK
		}
		fmt.Fprintf(stderr, "dpm check: %v\n\n%s", err, checkUsage)
		return exitUsage
	}
	if fl.NArg() > 0 {
		fmt.Fprintf(stderr, "dpm check: unexpected argument %q\n\n%s", fl.Arg(0), checkUsage)
		return exitUsage
	}

	res := runChecks()
	if *jsonOut {
		if err := writeJSON(stdout, res.report()); err != nil {
			fmt.Fprintf(stderr, "dpm: %v\n", err)
			return exitError
		}
	} else {
		res.writeText(stdout, stderr)
	}
	switch {
	case res.err != nil:
		if *jsonOut {
			fmt.Fprintf(stderr, "dpm: %v\n", res.err)
		}
		return exitError
	case len(res.diffs) > 0:
		return exitError
	}
	return exitOK
}

// checkResult is the outcome of a check, for both output formats.
type checkResult struct {
	// manifest is the path of dpm.toml, or "" when it was not read.
	manifest string
	// diffs are the differences in plan order. Empty when the check
	// failed.
	diffs []check.Difference
	// warnings are in the order they were produced.
	warnings []warning
	err      error
}

// runChecks compares the manifest in the current directory with the
// deployed directories and the sources (spec 3.7): read dpm.toml, check
// git, decide what install would do, fetch, and compare. Nothing is
// written; the fetch uses temporary directories only.
func runChecks() *checkResult {
	res := &checkResult{}
	m, err := loadManifest()
	if err != nil {
		res.err = err
		return res
	}
	res.manifest = m.File
	if err := checkGit(gitVersionOutput); err != nil {
		res.err = err
		return res
	}
	st, err := state.Load(m.Dir)
	if err != nil {
		res.err = err
		return res
	}
	plan, err := deploy.NewPlan(m.Dir, m.Deps, st, deploy.OSDisk{})
	if err != nil {
		res.err = err
		return res
	}
	sources, err := source.Fetch(m.Deps)
	if err != nil {
		res.err = err
		return res
	}
	for _, src := range sources {
		for _, w := range src.Warnings {
			res.warnings = append(res.warnings, warning{dep: src.Dep, message: w})
		}
	}
	diffs, err := check.Run(plan, sources)
	if err != nil {
		res.err = err
		return res
	}
	res.diffs = diffs
	return res
}
