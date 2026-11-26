# dpm

dpm (dropin package manager) deploys "drop-in" files — directories that take
effect just by being placed somewhere, such as Claude Code skills — from local
directories and Git repositories into the places that use them.

- A manifest, `dpm.toml`, lists the dependencies and where to put them.
- `dpm install` copies each dependency into its destination directories,
  replaces what it placed before, and removes what is no longer listed.
- `dpm check` reports differences without changing anything.
- There is no lock file. What was placed is recorded in `.dpm/state.toml`
  next to the manifest, outside version control.
- Files are deployed exactly as Git stores them, so every machine (Windows,
  Linux, macOS) gets the same bytes whatever its line ending settings.

> **Status:** `0.x`. The formats of `dpm.toml`, `dpm.local.toml` and
> `.dpm/state.toml`, the `--json` output and the exit codes may still change.
> From `v1.0.0` on, they change only with a new major version. The
> human-readable output is not covered by this promise; scripts should use
> `--json`.

## Installation

Download the executable for your platform from the
[latest release](https://github.com/e8n/dpm/releases/latest) and put it on
your `PATH`:

| Platform | File |
|---|---|
| Windows (x64) | `dpm-windows-amd64.exe` |
| Linux (x64), including WSL | `dpm-linux-amd64` |
| macOS (Apple silicon) | `dpm-darwin-arm64` |

The release also contains `SHA256SUMS` for verifying the download.

For example, on Linux:

```sh
curl -fsSLO https://github.com/e8n/dpm/releases/latest/download/dpm-linux-amd64
curl -fsSLO https://github.com/e8n/dpm/releases/latest/download/SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
install -m 755 dpm-linux-amd64 ~/.local/bin/dpm
```

Alternatively, with Go installed:

```sh
go install github.com/e8n/dpm/cmd/dpm@latest
```

On WSL, use the Linux executable, also for projects under `/mnt/c/`.

### Requirements

- git 2.34 or later on `PATH`. dpm uses git to fetch dependencies and to
  hash files, and checks the version on startup.

## Quick start

Put a `dpm.toml` at the root of your project:

```toml
[defaults]
to = ["${claude}/skills"]

[deps.commit]
path = "../agent-packages/packages/commit"

[deps.playwright-cli]
git = "https://github.com/microsoft/playwright-cli"
subdir = "skills/playwright-cli"
ref = "v0.1.14"
```

Then run, in the directory of `dpm.toml`:

```sh
dpm install
```

This creates `.claude/skills/commit/` and `.claude/skills/playwright-cli/`,
and records them in `.dpm/state.toml`. Run `dpm install` again whenever the
sources or the manifest change.

## The manifest: dpm.toml

dpm reads `dpm.toml` in the current directory. When `dpm.toml` is a
symbolic link, every relative path is resolved against the directory of the
link's target. Unknown keys are errors, so typos are caught.

### [deps]

Each dependency is a table `[deps.<name>]`. The name is the name of the
directory created in each destination; it is independent of the name of the
source directory (a source `commit-win` can be deployed as `commit`).

A name must be a single path element (no `/` or `\`, not `.` or `..`) and
must not start with `_`. Directories whose names start with `_` are left for
your own files, and dpm never touches them.

A dependency is either a local directory or a directory in a Git repository.

```toml
[deps.commit]
path = "../agent-packages/packages/commit"

[deps.playwright-cli]
git = "https://github.com/microsoft/playwright-cli"
subdir = "skills/playwright-cli"
ref = "v0.1.14"
to = ["${claude}/skills", "${agents}/skills"]
```

| Key | Meaning |
|---|---|
| `path` | A local directory, relative to the directory of `dpm.toml` or starting with `~` (your home directory; `%USERPROFILE%` on Windows). Absolute paths are not allowed, so that the manifest works on every machine. Variables can be used anywhere in it (`"${hub}/packages/commit"`). |
| `git` | The Git repository, as given to `git fetch`: a URL (`https://`, `ssh://`, `file://`, ...) or an scp-like address (`git@github.com:owner/repo.git`). Local paths (`../repo`, `C:\repo`) are not accepted; use `path` for a local directory, which gives the same integrity once its changes are committed. Private repositories use your existing Git credentials. |
| `subdir` | The directory inside the repository, with `/` as the separator. Omitted: the root of the repository. Only with `git`. |
| `ref` | A tag, a branch or a full commit hash (abbreviated hashes are not accepted). Omitted: the default branch. Only with `git`. |
| `to` | The list of destinations. The dependency is deployed to `<destination>/<name>` for each of them. Omitted: `[defaults] to`. |

`path` and `git` cannot be used together.

**Local directories.** dpm uses the current contents of the directory,
committed or not. Inside a Git work tree, the files are taken as Git would
store them: files ignored by `.gitignore` are left out, and line endings
are normalized according to `core.autocrlf` and `.gitattributes`. Outside any
work tree, the files are taken byte for byte, and dpm prints a warning,
because the result then depends on the line endings of your checkout.

**Git repositories.** On every install, dpm fetches only the given `ref`
(shallowly) into a temporary directory and deletes it afterwards; there is no
cache. The `ref` is resolved on every install: with a tag or a commit every
machine gets the same contents, while a branch (or no `ref`) gives the
latest commit at the time of the install. Dependencies that share the same
`git` and `ref` are fetched once.

A dependency must contain at least one file. Submodules cannot be deployed.
Symbolic links are deployed as symbolic links. Files that Git marks
executable are written with the execute bit (except on Windows).

### [vars] and destinations

`to` must start with a variable. The variable says where dpm may write, and
`..` is not allowed in `to`; only a variable can point outside the project.

| Variable | Default | Used for |
|---|---|---|
| `claude` | `.claude` | Claude Code (`${claude}/skills`, `${claude}/hooks`) |
| `agents` | `.agents` | Codex (`${agents}/skills`) |

Declare more variables, or change the defaults, in `[vars]`:

```toml
[vars]
claude = "~/.claude"            # deploy to the user's skills instead of the project's
project = "."                   # the directory of dpm.toml
hub = "../agent-packages"

[deps.create-spec]
path = "${hub}/vscode/create-spec/create-spec"
to = ["${project}/scripts"]     # -> scripts/create-spec/
```

- A value is a path relative to the directory of `dpm.toml`, or a path
  starting with `~`. Absolute paths are not allowed.
- A value cannot refer to other variables.
- In `to`, only the first element can be a variable, and it must be followed
  by a path separator or nothing (`${claude}/skills`, `${project}`).

### [defaults]

```toml
[defaults]
to = ["${claude}/skills"]
```

`to` in `[defaults]` applies to every dependency that has no `to` of its own.
A dependency without `to` is an error when there is no default.

### dpm.local.toml

`dpm.local.toml`, next to `dpm.toml`, overrides values in `[vars]` for one
machine. Keep it out of version control (add it to `.gitignore`).

```toml
# dpm.local.toml (on Windows only)
[vars]
hub = "../../src/agent-packages"
```

It may contain only `[vars]`, and only variables that `dpm.toml` declares
(or the defaults `claude` and `agents`) can be overridden. Dependencies
cannot be added or removed here, so every machine deploys the same set.

## Commands

```sh
dpm install [--json]   # deploy the dependencies listed in dpm.toml
dpm check [--json]     # report differences without changing anything
dpm --version
```

Both commands work on `dpm.toml` in the current directory.

| Exit code | Meaning |
|---|---|
| 0 | Success (`check`: no differences) |
| 1 | Failure (`check`: also when there are differences) |
| 2 | Invalid command line |

### dpm install

`dpm install` builds every destination from the current contents of the
sources, then swaps them in:

```text
adopted  commit -> .claude/skills/commit (it was not placed by dpm; overwritten)
placed   playwright-cli -> .claude/skills/playwright-cli
removed  old-skill -> .claude/skills/old-skill
dpm.toml: 1 placed, 1 adopted, 1 removed
```

Warnings and errors go to stderr. How each directory is treated is described
in [Deployed directories](#deployed-directories).

### dpm check

`dpm check` compares the destinations with the record and with the current
contents of the sources. It reports:

| Kind | Meaning | What `dpm install` does |
|---|---|---|
| `missing` | A dependency is not deployed to one of its destinations. | Places it. |
| `modified` | The deployed files differ from what was deployed (edited by hand). The execute bit is ignored, because Windows does not keep it. | Overwrites the edits. |
| `outdated` | The source has changed since it was deployed. | Updates it. |
| `leftover` | A directory placed by dpm is no longer in the manifest. | Removes it. |
| `adoptable` | A directory not placed by dpm has the name of a dependency. | Overwrites it. |

```text
modified  commit -> .claude/skills/commit (edited since it was deployed)
outdated  commit -> .claude/skills/commit (the source has changed)
dpm.toml: 1 modified, 1 outdated; "dpm install" updates them, overwriting the local edits
```

A directory can have more than one difference. With no differences, `check`
prints `dpm.toml: up to date` and exits with 0. `check` fetches Git
dependencies to a temporary directory but changes nothing else.

### --json

With `--json`, both commands write one JSON object to stdout, whether they
succeed or fail. `command`, `ok`, `results`, `warnings` and `error` are
always present.

`dpm install --json`:

```json
{
  "command": "install",
  "ok": true,
  "manifest": "/home/me/project/dpm.toml",
  "generated_from": "/home/me/project/apm.yml",
  "results": [
    {"dep": "commit", "dir": ".claude/skills/commit", "action": "place",
     "integrity": "git-sha1:..."},
    {"dep": "playwright-cli", "dir": ".claude/skills/playwright-cli",
     "action": "replace", "integrity": "git-sha1:...", "commit": "..."},
    {"dep": "old", "dir": ".claude/skills/old", "action": "remove"}
  ],
  "warnings": [
    {"dep": "commit", "message": "..."},
    {"message": "..."}
  ],
  "error": null
}
```

| Key | Meaning |
|---|---|
| `command` | `"install"`. |
| `ok` | `true` when the install succeeded (the exit code says the same). |
| `manifest` | The absolute path of `dpm.toml` (symbolic links resolved). Omitted when it was not read. |
| `generated_from` | The absolute path of `apm.yml` when this install generated `dpm.toml` from it (see [Converting from apm.yml](#converting-from-apmyml)). Omitted otherwise. |
| `results` | One entry per directory: the dependencies sorted by name, each in the order of its `to`, then the removed directories sorted by `dir`. Always empty when `ok` is `false`. |
| `results[].dep` | The name of the dependency. |
| `results[].dir` | The directory, relative to the directory of `dpm.toml` with `/` as the separator (absolute when no relative path exists, such as another drive on Windows). |
| `results[].action` | `place` (did not exist), `replace` (placed by dpm before), `adopt` (existed, not placed by dpm, named after the dependency; overwritten), `remove` (placed by dpm, no longer in the manifest; deleted) or `forget` (in the record but already gone; only the record was updated). |
| `results[].integrity` | The [integrity](#integrity) of the deployed contents. Omitted for `remove` and `forget`. |
| `results[].commit` | For Git dependencies, the commit that `ref` resolved to. Omitted otherwise. |
| `warnings[]` | Messages for the user, possibly present when `ok` is `false`. `dep` is omitted when the message is not about one dependency. |
| `error` | The error message when `ok` is `false`, otherwise `null`. |

`dpm check --json` has the same form:

```json
{
  "command": "check",
  "ok": false,
  "manifest": "/home/me/project/dpm.toml",
  "results": [
    {"dep": "commit", "dir": ".claude/skills/commit", "kind": "modified",
     "integrity": "git-sha1:...", "deployed": "git-sha1:..."},
    {"dep": "commit", "dir": ".claude/skills/commit", "kind": "outdated",
     "integrity": "git-sha1:...", "source": "git-sha1:..."},
    {"dep": "browser", "dir": ".claude/skills/browser", "kind": "missing"}
  ],
  "warnings": [],
  "error": null
}
```

| Key | Meaning |
|---|---|
| `command` | `"check"`. |
| `ok` | `true` when there are no differences and no error (exit code 0). Differences alone leave `error` as `null`. |
| `manifest` | As for `install`. |
| `results` | One entry per difference, in the order of `install`'s results. Empty when `error` is not `null`. |
| `results[].dep`, `results[].dir` | As for `install`. |
| `results[].kind` | `missing`, `modified`, `outdated`, `leftover` or `adoptable` (see [dpm check](#dpm-check)). |
| `results[].integrity` | The integrity in `.dpm/state.toml`. Omitted when the directory is not recorded. |
| `results[].deployed` | For `modified`, the integrity of the deployed directory, with the execute bit of each file taken from the source so that it can be compared with `integrity` and `source`. Omitted when the destination is not a directory, and for the other kinds. |
| `results[].source` | For `outdated`, the current integrity of the source. |
| `results[].commit` | For `outdated` Git dependencies, the commit that `ref` resolved to. |
| `warnings`, `error` | As for `install`. |

New keys may be added in later versions when they do not change the meaning
of existing ones.

## Deployed directories

dpm owns one directory per dependency and destination, such as
`.claude/skills/commit/`. It never touches anything else in the destination:
directories starting with `_`, directories you placed by hand, and those of
other tools are left alone.

On every install, each directory is treated as follows:

| The directory | `dpm install` |
|---|---|
| does not exist | places it |
| was placed by dpm (it is in the record) | replaces it, overwriting any edits |
| was not placed by dpm, but has the name of a dependency | adopts it: overwrites it and says so |
| was not placed by dpm and is not named after a dependency | leaves it alone |
| was placed by dpm, but its dependency (or that `to` entry) is no longer in the manifest | removes it |

Adoption lets you take over directories placed by another tool (such as APM)
or committed to the repository: once the manifest names a dependency, the
directory of that name belongs to dpm.

### The record: .dpm/state.toml

What dpm placed is recorded in `.dpm/state.toml` next to `dpm.toml`, with the
paths relative to the manifest, so the project can be moved (a destination
with no relative path, on another drive or a UNC path on Windows, is
recorded as an absolute path). dpm also writes
`.dpm/.gitignore` containing `*`, so Git ignores the directory without
changes to your `.gitignore`. The record belongs to one machine and one work
tree; do not edit it or commit it.

```toml
# Written by dpm. Do not edit.
version = 1

[[placed]]
dep = "commit"
dir = ".claude/skills/commit"
integrity = "git-sha1:..."
content = "git-sha1:..."
```

`integrity` is the [integrity](#integrity) of what was deployed. `content` is
the same hash with every file taken as not executable; `check` compares it
with the deployed files, so that the execute bit, which Windows does not
keep, makes no difference. Records written by earlier versions of dpm have
no `content`; `check` then compares the deployed files with `integrity`,
taking the execute bit of each file from the source.

If the record is lost, installing still works, because the directories named
after dependencies are adopted. Only the clean-up needs it: a directory whose
dependency was removed from the manifest while the record was missing has to
be deleted by hand.

### Atomic installs

An install happens in two steps:

1. Every dependency is fetched and written to a staging directory,
   `.dpm-staging`, next to each destination (`.claude/.dpm-staging` for
   `.claude/skills`). If this fails, nothing has changed.
2. The directories are swapped in by renaming them, and the removed ones are
   deleted. If a rename fails (for example because a file is open on
   Windows), the directories already swapped are moved back. The record is
   written last.

The staging directory is removed afterwards. It contains a `.gitignore`, so a
leftover after a crash is ignored by Git, and the next install removes it.
In the rare case where moving back fails too, the error says where the
previous contents are kept.

### Integrity

The integrity of a deployed directory is the hash of the Git tree object of
its files, written `git-sha1:<hex>` (or `git-sha256:<hex>` for repositories
in the SHA-256 format). For a Git dependency it is the tree of
`<commit>:<subdir>`; for a local directory in a Git work tree it is the tree
Git would record, so it equals the value of the same directory once
committed. `install` records it and `check` compares it with the deployed
files and the sources; it is not used to decide what `install` does.

## Converting from apm.yml

When there is no `dpm.toml` but there is an `apm.yml` (the manifest of
[APM](https://github.com/microsoft/apm)), `dpm install` generates `dpm.toml`
from `apm.yml`, says so, and goes on to install it:

```text
generated dpm.toml from apm.yml (apm.yml and apm.lock.yaml are unchanged)
adopted  commit -> .claude/skills/commit (it was not placed by dpm; overwritten)
```

The directories that APM deployed are adopted. `apm.yml` and `apm.lock.yaml`
are never changed or deleted; remove them yourself once you have checked the
result. As soon as `dpm.toml` exists, `apm.yml` is ignored. `dpm check` never
converts: without `dpm.toml`, it fails and says that `dpm install` generates
it from `apm.yml`.

| apm.yml | dpm.toml |
|---|---|
| `targets: [claude]` | `[defaults] to = ["${claude}/skills"]` |
| a local path in `dependencies.apm`, such as `../hub/packages/commit` | `[deps.commit] path = "../hub/packages/commit"` |
| `github.com/<owner>/<repo>/<subdir>#<ref>` in `dependencies.apm` | `git = "https://github.com/<owner>/<repo>"`, `subdir = "<subdir>"`, `ref = "<ref>"` |

The name of each dependency is the last element of its path. The
dependencies keep the order of `apm.yml`, and the same `apm.yml` always gives
the same `dpm.toml`. `name`, `version`, `description`, `author`, `license`,
`includes: auto` and empty `scripts` are ignored.

Anything else cannot be converted: MCP servers, targets other than `claude`,
other hosts and other ways of writing a dependency, absolute paths,
non-empty `scripts`, and unknown keys. Then nothing is written, and every
such item is reported with its line:

```text
dpm: apm.yml cannot be converted to dpm.toml:
  line 7: dependency "gitlab.com/owner/repo": only local paths and github.com/<owner>/<repo>[/<subdir>][#<ref>] can be converted
  line 9: MCP server "io.github.example/server" is not supported
nothing was written; write dpm.toml by hand
```

## Limitations

- Do not deploy a dependency of the same name to the same destination from
  two manifests (for example from a project's `dpm.toml` and from one for
  your home directory). Each manifest has its own record and cannot see the
  other's, so the later install overwrites the earlier one. dpm does not
  detect this.
- Do not run two installs of the same manifest at the same time.
- Two dependencies whose destinations differ only in letter case, or where
  one lies inside the other, are rejected on every platform.
- Only local directories and Git repositories are supported as sources.
- Environment variables cannot be used in the manifest; use `dpm.local.toml`
  for values that differ between machines.
- There are no commands to edit the manifest; write it by hand.

## License

[MIT](LICENSE)
