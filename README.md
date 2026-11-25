# dpm

dpm (dropin package manager) deploys "drop-in" files — directories that take
effect just by being placed somewhere, such as Claude Code skills — from local
directories and Git repositories into the places that use them.

> **Status:** under development. The current release (`v0.0.x`) only provides
> `dpm --version`. `install` and `check` arrive in `v0.1.0`.

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

### Requirements

- git 2.34 or later on `PATH`. dpm uses git to fetch dependencies and checks
  the version on startup.

## Usage

```sh
dpm install   # deploy the dependencies listed in dpm.toml
dpm check     # report differences without changing anything
```

Both commands accept `--json` for machine-readable output.

<!-- TODO: document dpm.toml ([vars], [defaults], [deps]), dpm.local.toml,
     install/check, adoption and cleanup of deployed directories, conversion
     from apm.yml, and limitations. -->

## License

[MIT](LICENSE)
