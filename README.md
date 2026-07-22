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
- **Background sessions** — running herdr sessions with no project here (the
  `default` session, or extra windows like `barista-2`) are listed under
  *Background sessions* so you can see, attach to, or stop sessions Terminal
  Cowboy didn't launch. The `default` session is never offered for stopping.
- **1Password credentials** — an optional `.op.env` per project is loaded with
  `op run --env-file`, so `op://vault/item/field` references resolve at launch
  and secrets never sit decrypted on disk.

## Runners

Each project picks a **runner** — what it launches in the new window. The shared
machinery (cwd, env, `op` credentials, terminal, window size, detach, logging,
hold-open) is identical across all of them; only the inner command differs.

| runner | launches | running-state / Stop via |
|--------|----------|--------------------------|
| **herdr** (default) | `herdr --session <name>` (+ remote/handoff/args) | herdr API socket |
| **sesh** | `sesh connect <cwd>` (directory-oriented; + optional command) | tmux |
| **tmux** | `tmux new-session -A -s <name>` (+ optional command) | tmux |
| **shell** | a plain interactive shell — no multiplexer | — (ephemeral, no dot) |

- **Full parity:** herdr / sesh / tmux projects show a running dot and get
  **Attach** / **Stop**; sesh and tmux are both controlled through tmux
  (`tmux list-sessions` / `kill-session`). `shell` is fire-and-forget.
- **Background sessions** lists unmanaged sessions from *both* backends (running
  herdr sessions and running tmux sessions with no project), tagged by backend.
- **Nesting** is handled for all: the launch scrubs `HERDR_*` and `TMUX`/
  `TMUX_PANE`, so launching from inside a herdr or tmux session starts a clean
  top-level one.
- **herdr-only** options (remote, handoff, workspace label, extra args) show in
  the editor only when the runner is herdr; **sesh/tmux** get an optional
  "command to run".

## Supported terminals

Auto-detected, with a config override (`terminal = "…"`):

| OS      | Terminals                          |
|---------|------------------------------------|
| Linux   | wezterm, kitty, ghostty            |
| macOS   | ghostty, iterm2, wezterm, kitty    |

iTerm2 has no exec CLI, so it is driven via AppleScript (`osascript`).

## Install / run

Install the `tcow` / `tcowboy` launcher commands into `~/.local/bin`:

```sh
make install       # installs terminal-cowboy + tcow + tcowboy symlinks
tcow --open        # run and open the UI in your browser
```

Or run without installing:

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
cols     = 0             # default window columns (0 = terminal default)
rows     = 0             # default window rows
shell    = "sh"          # shell that runs the launch script (sh|bash|zsh|fish)
no_login_shell = false   # set true to skip the login shell (-l)

# Optional explicit binary paths (otherwise resolved from PATH):
# wezterm = "/Applications/WezTerm.app/Contents/MacOS/wezterm"
# herdr   = "/home/you/.local/bin/herdr"
# op      = "/usr/bin/op"
```

All of the above are editable from the **⚙ Settings** panel.

### sessions/<name>/session.toml (per project)

```toml
name        = "barista"              # herdr session name (defaults to folder name)
description = "Barista ordering agent"
cwd         = "~/git/barista"        # cd here before launching
herdr_args  = ["--default-config"]   # extra args appended to herdr --session <name>

# Optional per-project overrides (blank / 0 = use the global default):
terminal    = "wezterm"              # wezterm | kitty | iterm2 | ghostty
cols        = 100                    # window columns
rows        = 30                     # window rows

# Structured herdr options (nicer than raw herdr_args):
remote            = ""               # --remote <ssh-target> (blank = local)
remote_keybindings = "local"         # local | server (only used with remote)
handoff           = false            # --handoff

[env]
ROLE      = "barista"
LOG_LEVEL = "debug"
```

All of these are editable per project in the UI's project editor.

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

## Launched windows outlive the server

Terminal Cowboy is a launcher: windows it opens are started **detached, in their
own session/process group** (`setsid`), so exiting the server — Ctrl-C (SIGINT to
its process group) or closing its terminal (SIGHUP to its session) — never tears
them down. (A window-size override in particular makes wezterm spawn a GUI as a
child of the server; detaching keeps that window alive regardless.) The herdr
session itself always persists on the herdr server either way.

## Dotfiles / GNU stow

All config lives under `~/.config/terminal-cowboy/`, so it travels with a
dotfiles repo. With GNU stow, put it in a package and stow it into `$HOME`:

```
~/dotfiles/terminal-cowboy/.config/terminal-cowboy/
  config.toml
  sessions/<name>/{session.toml,.op.env}
```

```sh
cd ~/dotfiles && stow terminal-cowboy
```

Notes:

- **`.op.env` is safe to commit/stow** — it holds `op://vault/item/field`
  *references*, not secrets, which `op` resolves at launch. Do **not** put
  literal secrets there.
- **Exclude `logs/`** — it's ephemeral launch output. Add `logs/` to your
  dotfiles `.gitignore` (or don't stow it).
- Paths in `cwd` use `~`, so they resolve correctly on any machine.

## Logs & troubleshooting

When a launch fails (missing `cwd`, `op` not signed in, herdr error), the window
**stays open** showing the error and a `press Enter to close…` prompt, instead
of vanishing. Every launch is also recorded to a per-session log:

```
~/.config/terminal-cowboy/logs/<session>.log
```

Each entry records the project, herdr session, terminal, cwd, whether `.op.env`
was used, the exact `argv` that ran, and the final exit code. View them with the
**Logs** button on any project card, or read the files directly. Secondary
windows log under their own name (e.g. `barista-2.log`).

### Workspace naming

herdr labels a session's initial workspace after the directory it launches in.
By default Terminal Cowboy renames that workspace a moment after launch (via
`herdr workspace rename` on the session's own API socket), so the workspace
reflects the project/session rather than the directory.

- **Per project:** set a **Workspace label** in the project editor (free text —
  spaces/emoji allowed, e.g. `☕ Barista`). Blank falls back to the session name.
- **Secondary windows** (e.g. `barista-2`) keep the name you typed.
- **Global toggle:** `no_workspace_label = true` (Settings → "Rename the herdr
  workspace…") disables renaming entirely and keeps herdr's directory-based label.

### Nested herdr sessions

herdr disables nested sessions by default (`allow_nested = false`). Terminal
Cowboy launches **independent top-level** sessions, so it scrubs herdr's
session-context env vars (`HERDR_SESSION`, `HERDR_*_ID`, socket paths) from each
launch — both from the spawned process and via an `unset` in the launch script.
That means you can run the launcher from inside an existing herdr session
without tripping the nested-session guard.

## Settings

The **⚙ Settings** panel edits the global `config.toml`:

- **Web server address / port** — where the app listens. Changes take effect
  after a restart (the UI tells you).
- **Terminal** — `auto` or a specific detected terminal.
- **New tab vs new window** — how sessions open, where the terminal supports it.

## Requirements

`herdr`, and whichever terminal you use, on PATH. `op` (1Password CLI) signed in
only if a project uses `.op.env`.
