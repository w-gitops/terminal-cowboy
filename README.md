# 🤠 Terminal Cowboy

A self-contained Go web app that launches and connects to
[`herdr`](https://) terminal sessions. Point your browser at it, pick a
project, and it opens a new terminal window running
`herdr --session <name>` — with per-project environment variables and
1Password-backed credentials injected at launch.

Runs on **macOS (Apple Silicon)** and **Fedora / Linux (amd64 + arm64)** from a
single static binary. The web UI is embedded in the binary — nothing else to
deploy.

## What it does

- **Launch sessions** — each project `cd`s into its git folder and runs
  `herdr --session <name>` in a fresh terminal window.
- **Secondary windows** — open an independent second window on the same project
  (e.g. `barista-2`), reusing the folder, env and credentials but with a
  different herdr session name. The name is prefilled and editable.
- **Live status** — the UI shows which sessions are running (via
  `herdr session list --json`) with attach/stop controls.
- **Manage projects from the UI** — create, edit, and delete projects without
  touching config files.
- **1Password credentials** — an optional `.op.env` per project is loaded with
  `op run --env-file`, so `op://vault/item/field` references resolve at launch
  and secrets never sit decrypted on disk.

## Supported terminals

Auto-detected, with a config override (`terminal = "…"`):

| OS      | Terminals                          |
|---------|------------------------------------|
| Linux   | wezterm, kitty, ghostty            |
| macOS   | ghostty, iterm2, wezterm, kitty    |

iTerm2 has no exec CLI, so it is driven via AppleScript (`osascript`).

## Install / run

```sh
make run           # build + open the UI in your browser
# or
go build -o terminal-cowboy .
./terminal-cowboy --open
```

The server binds to `127.0.0.1:8787` by default and **only accepts loopback
connections** (launching terminals is a local, privileged action).

Cross-compile release binaries:

```sh
make cross         # -> dist/terminal-cowboy-{darwin-arm64,linux-amd64,linux-arm64}
```

## Configuration

Everything lives under `~/.config/terminal-cowboy/` (honors `XDG_CONFIG_HOME`).
The UI writes these files for you; you can also edit them by hand.

```
~/.config/terminal-cowboy/
  config.toml                 # global settings (optional)
  sessions/
    barista/
      session.toml            # project definition
      .op.env                 # 1Password refs (optional, chmod 600)
```

### config.toml (global)

```toml
addr     = "127.0.0.1"   # listen host
port     = 8787
terminal = "auto"        # auto | wezterm | kitty | iterm2 | ghostty
new_tab  = false         # open a new tab instead of a new window (where supported)

# Optional explicit binary paths (otherwise resolved from PATH):
# wezterm = "/Applications/WezTerm.app/Contents/MacOS/wezterm"
# herdr   = "/home/you/.local/bin/herdr"
# op      = "/usr/bin/op"
```

### sessions/<name>/session.toml (per project)

```toml
name        = "barista"              # herdr session name (defaults to folder name)
description = "Barista ordering agent"
cwd         = "~/git/barista"        # cd here before launching
herdr_args  = ["--remote-keybindings", "local"]  # appended to herdr --session <name>

[env]
ROLE      = "barista"
LOG_LEVEL = "debug"
```

### sessions/<name>/.op.env (per project, optional)

```
API_KEY = op://vault/barista/key
DB_URL  = op://vault/barista/db-url
```

## How a launch is assembled

For project `barista` (secondary window `barista-2`) the app runs, inside the
chosen terminal's new window:

```sh
sh -lc "cd ~/git/barista; exec \
  op run --env-file=.op.env -- \
  env ROLE=barista LOG_LEVEL=debug \
  herdr --session barista-2 --remote-keybindings local"
```

- `op run` is only added when a `.op.env` exists.
- Plain env vars use `/usr/bin/env` (portable across macOS and Linux).
- A login shell (`sh -lc`) ensures `herdr`/`op` on your PATH resolve.

## Requirements

`herdr`, and whichever terminal you use, on PATH. `op` (1Password CLI) signed in
only if a project uses `.op.env`.
