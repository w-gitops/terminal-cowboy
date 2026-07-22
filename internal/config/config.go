// Package config loads Terminal Cowboy's global settings and the per-session
// definitions found under the sessions directory.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Global holds server-wide settings from config.toml.
type Global struct {
	Addr         string `toml:"addr"`           // listen address, default 127.0.0.1
	Port         int    `toml:"port"`           // listen port, default 8787
	Terminal     string `toml:"terminal"`       // wezterm|kitty|iterm2|ghostty|auto (default auto)
	WeztermBin   string `toml:"wezterm"`        // optional override path to wezterm
	HerdrBin     string `toml:"herdr"`          // optional override path to herdr
	OpBin        string `toml:"op"`             // optional override path to op
	SessionsDir  string `toml:"sessions_dir"`   // optional override for sessions dir
	NewTab       bool   `toml:"new_tab"`        // spawn a new tab instead of a new window
	Shell        string `toml:"shell"`          // shell that runs the launch script (default sh)
	NoLoginShell bool   `toml:"no_login_shell"` // don't use a login shell (default: login shell)
	Cols         int    `toml:"cols"`           // initial window columns (0 = terminal default)
	Rows         int    `toml:"rows"`           // initial window rows (0 = terminal default)
	// By default the initial herdr workspace is renamed to the session name
	// after launch (herdr otherwise labels it after the launch directory).
	NoWorkspaceLabel bool `toml:"no_workspace_label"`
}

// Session is one launchable backend, defined by sessions/<name>/session.toml.
type Session struct {
	Name        string            `toml:"name"`        // defaults to directory name
	Description string            `toml:"description"` // shown in the UI
	Cwd         string            `toml:"cwd"`         // working directory for the launch
	Runner      string            `toml:"runner"`      // herdr|sesh|tmux|shell (default herdr)
	RunnerCmd   string            `toml:"runner_cmd"`  // optional command to run (tmux/sesh)
	HerdrArgs   []string          `toml:"herdr_args"`  // extra args appended to `herdr --session <name>`
	Env         map[string]string `toml:"env"`         // plain env vars exported into the session

	// WorkspaceLabel overrides the herdr workspace label for a primary launch
	// (blank = use the session name). Free text — spaces/emoji allowed.
	WorkspaceLabel string `toml:"workspace_label"`

	// Structured herdr options (also expressible via HerdrArgs, but nicer in the UI).
	Remote            string `toml:"remote"`             // --remote <ssh-target>
	RemoteKeybindings string `toml:"remote_keybindings"` // local|server (only with remote)
	Handoff           bool   `toml:"handoff"`            // --handoff

	// Per-project overrides (empty/0 = use the global default).
	Terminal string `toml:"terminal"` // wezterm|kitty|iterm2|ghostty (override global)
	Cols     int    `toml:"cols"`     // window columns
	Rows     int    `toml:"rows"`     // window rows

	Dir      string `toml:"-"` // absolute path to the session directory
	OpEnv    string `toml:"-"` // absolute path to .op.env if present, else ""
	HasOpEnv bool   `toml:"-"`
}

// Runner identifiers.
const (
	RunnerHerdr = "herdr"
	RunnerSesh  = "sesh"
	RunnerTmux  = "tmux"
	RunnerShell = "shell"
)

// EffectiveRunner returns the session's runner, defaulting to herdr.
func (s Session) EffectiveRunner() string {
	if s.Runner == "" {
		return RunnerHerdr
	}
	return s.Runner
}

// Backend returns the status/control backend for a runner: "herdr", "tmux"
// (also serves sesh), or "" for runners with no persistent session (shell).
func Backend(runner string) string {
	switch runner {
	case RunnerSesh, RunnerTmux:
		return "tmux"
	case RunnerHerdr:
		return "herdr"
	default:
		return ""
	}
}

// Config is the fully-resolved configuration.
type Config struct {
	Global   Global
	Sessions []Session

	Root        string // ~/.config/terminal-cowboy
	SessionsDir string // resolved sessions directory
}

// Root returns the base config directory, honoring XDG_CONFIG_HOME.
func rootDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "terminal-cowboy"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "terminal-cowboy"), nil
}

// expand resolves a leading ~ and any $ENV references in a path.
func expand(p string) string {
	if p == "" {
		return p
	}
	p = os.ExpandEnv(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// Load reads config.toml (if present) and every sessions/<name>/session.toml.
func Load() (*Config, error) {
	root, err := rootDir()
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Root: root,
		Global: Global{
			Addr: "127.0.0.1",
			Port: 8787,
		},
	}

	cfgPath := filepath.Join(root, "config.toml")
	if _, err := os.Stat(cfgPath); err == nil {
		if _, err := toml.DecodeFile(cfgPath, &cfg.Global); err != nil {
			return nil, fmt.Errorf("parse %s: %w", cfgPath, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat %s: %w", cfgPath, err)
	}

	sessionsDir := cfg.Global.SessionsDir
	if sessionsDir == "" {
		sessionsDir = filepath.Join(root, "sessions")
	}
	cfg.SessionsDir = expand(sessionsDir)

	sessions, err := loadSessions(cfg.SessionsDir)
	if err != nil {
		return nil, err
	}
	cfg.Sessions = sessions
	return cfg, nil
}

// LogDir returns the directory where launch logs are written, creating it.
func (c *Config) LogDir() (string, error) {
	dir := filepath.Join(c.Root, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func loadSessions(dir string) ([]Session, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no sessions configured yet is not an error
		}
		return nil, fmt.Errorf("read sessions dir %s: %w", dir, err)
	}

	var sessions []Session
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sdir := filepath.Join(dir, e.Name())
		s := Session{Name: e.Name(), Dir: sdir}

		tomlPath := filepath.Join(sdir, "session.toml")
		if _, err := os.Stat(tomlPath); err == nil {
			if _, err := toml.DecodeFile(tomlPath, &s); err != nil {
				return nil, fmt.Errorf("parse %s: %w", tomlPath, err)
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat %s: %w", tomlPath, err)
		}
		if s.Name == "" {
			s.Name = e.Name()
		}
		s.Cwd = expand(s.Cwd)

		opPath := filepath.Join(sdir, ".op.env")
		if fi, err := os.Stat(opPath); err == nil && !fi.IsDir() {
			s.OpEnv = opPath
			s.HasOpEnv = true
		}

		sessions = append(sessions, s)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Name < sessions[j].Name
	})
	return sessions, nil
}
