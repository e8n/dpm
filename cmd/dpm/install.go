package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/e8n/dpm/internal/apmconv"
	"github.com/e8n/dpm/internal/deploy"
	"github.com/e8n/dpm/internal/manifest"
	"github.com/e8n/dpm/internal/source"
	"github.com/e8n/dpm/internal/state"
)

const installUsage = `Usage: dpm install [--json]

Deploy the dependencies listed in dpm.toml in the current directory.
When there is no dpm.toml but there is an apm.yml, dpm.toml is generated
from apm.yml first (apm.yml and apm.lock.yaml are left unchanged).

Options:
  --json     Print the result as JSON
`

// runInstall runs "dpm install" with the arguments after the command name.
func runInstall(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("install", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	jsonOut := fl.Bool("json", false, "")
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, installUsage)
			return exitOK
		}
		fmt.Fprintf(stderr, "dpm install: %v\n\n%s", err, installUsage)
		return exitUsage
	}
	if fl.NArg() > 0 {
		fmt.Fprintf(stderr, "dpm install: unexpected argument %q\n\n%s", fl.Arg(0), installUsage)
		return exitUsage
	}

	res := install()
	if *jsonOut {
		if err := writeJSON(stdout, res.report()); err != nil {
			fmt.Fprintf(stderr, "dpm: %v\n", err)
			return exitError
		}
	} else {
		res.writeText(stdout, stderr)
	}
	if res.err != nil {
		if *jsonOut {
			// The error is in the JSON too; it is repeated on stderr for
			// whoever watches the terminal.
			fmt.Fprintf(stderr, "dpm: %v\n", res.err)
		}
		return exitError
	}
	return exitOK
}

// installResult is the outcome of an install, for both output formats.
type installResult struct {
	// manifest is the path of dpm.toml, or "" when it was not read.
	manifest string
	// generatedFrom is the path of the apm.yml that dpm.toml was generated
	// from, or "" when dpm.toml was not generated.
	generatedFrom string
	// items are the actions carried out, in plan order. Empty when the
	// install failed: a failed install normally changes no destination
	// (see deploy.Staging.Apply for when restoring fails too).
	items []deploy.Item
	// sources are the fetched dependencies by name.
	sources map[string]*source.Source
	// warnings are in the order they were produced.
	warnings []warning
	err      error
}

// warning is a message for the user. dep is "" when it is not about one
// dependency.
type warning struct {
	dep     string
	message string
}

// install deploys the manifest in the current directory (spec 3.6, 3.7,
// 3.10.1): generate dpm.toml from apm.yml when needed, read dpm.toml, check
// git, decide the actions, fetch, stage, swap and record. Any error stops
// it, normally with the destinations as they were (see
// deploy.Staging.Apply for when restoring fails too).
func install() *installResult {
	res := &installResult{}
	generated, err := generateManifest()
	res.generatedFrom = generated
	if err != nil {
		res.err = err
		return res
	}
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
	// The plan comes before the fetch so that a manifest whose
	// destinations collide fails without contacting any repository.
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
	res.sources = make(map[string]*source.Source, len(sources))
	for _, src := range sources {
		res.sources[src.Dep] = src
		for _, w := range src.Warnings {
			res.warnings = append(res.warnings, warning{dep: src.Dep, message: w})
		}
	}
	applyWarnings, err := deploy.Install(plan, sources)
	for _, w := range applyWarnings {
		res.warnings = append(res.warnings, warning{message: w})
	}
	if err != nil {
		res.err = err
		return res
	}
	res.items = plan.Items
	return res
}

// generateManifest writes dpm.toml from apm.yml in the current directory
// when there is no dpm.toml and there is an apm.yml (spec 3.10.1). It
// returns the path of apm.yml when it wrote dpm.toml, and "" when it did
// nothing. When apm.yml cannot be converted, nothing is written.
func generateManifest() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	// Any dpm.toml, even a broken symbolic link, is left to loadManifest.
	if _, err := os.Lstat(filepath.Join(cwd, manifest.FileName)); !errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	apmFile := filepath.Join(cwd, apmconv.FileName)
	data, err := os.ReadFile(apmFile)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	out, err := apmconv.Convert(data)
	if err != nil {
		return "", fmt.Errorf("%w\nnothing was written; write %s by hand", err, manifest.FileName)
	}
	f, err := os.OpenFile(filepath.Join(cwd, manifest.FileName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	_, err = f.Write(out)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("cannot write %s: %w", manifest.FileName, err)
	}
	return apmFile, nil
}

// loadManifest reads dpm.toml in the current directory. When there is no
// dpm.toml but there is an apm.yml, the error says that install generates
// dpm.toml from it (spec 3.10.1); install itself has done so before.
func loadManifest() (*manifest.Manifest, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	m, err := manifest.Load(filepath.Join(cwd, manifest.FileName))
	if errors.Is(err, fs.ErrNotExist) {
		msg := fmt.Sprintf("%s was not found in %s", manifest.FileName, cwd)
		if hasAPMOnly(cwd) {
			msg += fmt.Sprintf(`; "dpm install" generates it from %s`, apmconv.FileName)
		}
		return nil, errors.New(msg)
	}
	return m, err
}

// hasAPMOnly reports whether dir has an apm.yml and nothing named dpm.toml
// (not even a broken symbolic link), which is when install generates
// dpm.toml.
func hasAPMOnly(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, manifest.FileName)); !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, apmconv.FileName))
	return err == nil
}
