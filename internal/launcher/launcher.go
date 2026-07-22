// Package launcher builds the command that runs a herdr session inside a new
// terminal window, injecting env vars and 1Password-backed credentials.
package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"terminal-cowboy/internal/config"
)

// nestingVars are the runtime env vars a multiplexer injects into a session's
// child processes to mark session context. Terminal Cowboy launches
// independent top-level sessions, so these are scrubbed — otherwise a launch
// from inside an existing herdr/tmux session trips the multiplexer's
// nested-session guard. Config vars (HERDR_CONFIG_PATH, HERDR_LOG) are left
// intact. Scrubbing the whole set on every runner is harmless: a fresh window
// never legitimately inherits them.
var nestingVars = []string{
	// herdr session context
	"HERDR_SESSION",
	"HERDR_CLIENT_SOCKET_PATH",
	"HERDR_SOCKET_PATH",
	"HERDR_PANE_ID",
	"HERDR_TAB_ID",
	"HERDR_WORKSPACE_ID",
	"HERDR_ACTIVE_PANE_ID",
	"HERDR_ACTIVE_TAB_ID",
	"HERDR_ACTIVE_WORKSPACE_ID",
	"HERDR_ACTIVE_PANE_CWD",
	"HERDR_REATTACH_COMMAND",
	// tmux (also covers sesh) session context
	"TMUX",
	"TMUX_PANE",
}

// Launcher spawns sessions into a chosen terminal.
type Launcher struct {
	Term       *Terminal // default terminal resolved from the global config
	termChoice string    // global terminal choice (for per-project re-selection)
	weztermBin string
	HerdrBin   string
	SeshBin    string
	TmuxBin    string
	OpBin      string
	NewTab     bool
	Shell      string
	Login      bool
	Cols       int
	Rows       int
}

// resolveBin returns override if set, else looks up name on PATH, else name
// (so it still resolves inside the launched login shell).
func resolveBin(override, name string) string {
	if override != "" {
		return override
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

// terminalFor resolves the terminal for a session, honoring a per-project
// override and falling back to the global default.
func (l *Launcher) terminalFor(s config.Session) (*Terminal, error) {
	if strings.TrimSpace(s.Terminal) != "" {
		return Select(s.Terminal, l.weztermBin)
	}
	return l.Term, nil
}

// New resolves the terminal and binary paths from config.
func New(cfg *config.Config) (*Launcher, error) {
	term, err := Select(cfg.Global.Terminal, cfg.Global.WeztermBin)
	if err != nil {
		return nil, err
	}
	return &Launcher{
		Term:       term,
		termChoice: cfg.Global.Terminal,
		weztermBin: cfg.Global.WeztermBin,
		HerdrBin:   resolveBin(cfg.Global.HerdrBin, "herdr"),
		SeshBin:    resolveBin("", "sesh"),
		TmuxBin:    resolveBin("", "tmux"),
		OpBin:      resolveBin(cfg.Global.OpBin, "op"),
		NewTab:     cfg.Global.NewTab,
		Shell:      cfg.Global.Shell,
		Login:      !cfg.Global.NoLoginShell,
		Cols:       cfg.Global.Cols,
		Rows:       cfg.Global.Rows,
	}, nil
}

// innerCommand builds the actual command run inside the window (no wrapping):
//
//	[op run --env-file=<openv> --] env K=V ... <runner command>
func (l *Launcher) innerCommand(s config.Session, sessionName string) string {
	var inner []string
	if s.HasOpEnv {
		inner = append(inner, shQuote(l.OpBin), "run",
			"--env-file="+shQuote(s.OpEnv), "--")
	}
	// env applies plain vars; /usr/bin/env is portable across macOS and Linux.
	inner = append(inner, "env")
	for _, kv := range sortedEnv(s.Env) {
		inner = append(inner, shQuote(kv))
	}
	inner = append(inner, l.runnerCommand(s, sessionName)...)
	return strings.Join(inner, " ")
}

// shellExpr is the shell used to run an optional per-session command; unquoted
// so $SHELL expands in-window.
const shellExpr = "\"${SHELL:-/bin/sh}\""

// runnerCommand returns the runner-specific argv (already shell-quoted).
func (l *Launcher) runnerCommand(s config.Session, name string) []string {
	switch s.EffectiveRunner() {
	case config.RunnerTmux:
		// Attach if the session exists, else create it.
		c := []string{shQuote(l.TmuxBin), "new-session", "-A", "-s", shQuote(name)}
		if s.RunnerCmd != "" {
			c = append(c, shellExpr, "-c", shQuote(s.RunnerCmd))
		}
		return c

	case config.RunnerSesh:
		// sesh is directory-oriented: connect to the project's cwd (falling back
		// to the name only when no cwd is set).
		target := s.Cwd
		if target == "" {
			target = name
		}
		c := []string{shQuote(l.SeshBin), "connect", shQuote(target)}
		if s.RunnerCmd != "" {
			c = append(c, "--command", shQuote(s.RunnerCmd))
		}
		return c

	case config.RunnerShell:
		// A plain interactive shell in the project's cwd with env/creds applied.
		return []string{shellExpr}

	default: // herdr
		c := []string{shQuote(l.HerdrBin)}
		if s.Remote != "" {
			c = append(c, "--remote", shQuote(s.Remote))
			if s.RemoteKeybindings != "" {
				c = append(c, "--remote-keybindings", shQuote(s.RemoteKeybindings))
			}
		}
		if s.Handoff {
			c = append(c, "--handoff")
		}
		c = append(c, "--session", shQuote(name))
		for _, a := range s.HerdrArgs {
			c = append(c, shQuote(a))
		}
		return c
	}
}

// Script builds the shell script run inside the new terminal window.
//
// herdrSession is the name passed to `herdr --session`; when empty it defaults
// to the project's own name. Secondary windows (e.g. "barista-2") reuse the
// project's cwd/env/credentials but pass a distinct herdr session name.
//
// The script is wrapped so that any failure (bad cwd, op not signed in, herdr
// error) is written to logPath AND the window is held open so the user can read
// it, instead of the window vanishing. A clean exit (rc 0) closes normally.
func (l *Launcher) Script(s config.Session, herdrSession, logPath string) string {
	if herdrSession == "" {
		herdrSession = s.Name
	}
	inner := l.innerCommand(s, herdrSession)
	log := shQuote(logPath)

	var b strings.Builder
	fmt.Fprintf(&b, "LOG=%s; ", log)
	// Scrub inherited herdr session context so a launch from inside an existing
	// herdr session starts a clean top-level session instead of tripping the
	// nested-session guard.
	fmt.Fprintf(&b, "unset %s; ", strings.Join(nestingVars, " "))
	// Friendly banner in the window.
	fmt.Fprintf(&b, "printf '\\033[33m🤠 terminal-cowboy\\033[0m  project=%s  session=%s\\n'; ",
		shBanner(s.Name), shBanner(herdrSession))
	if s.Cwd != "" {
		// A failed cd is the most common crash; report it and hold the window.
		fmt.Fprintf(&b,
			"cd %s || { msg=\"cannot cd to %s\"; printf '\\033[31mcowboy: %%s\\033[0m\\n' \"$msg\" | tee -a \"$LOG\"; printf 'press Enter to close…'; read _; exit 1; }; ",
			shQuote(s.Cwd), shBanner(s.Cwd))
	} else {
		// No cwd set: cd to $HOME so herdr names the workspace after the home
		// dir rather than inheriting (and leaking) terminal-cowboy's own cwd.
		b.WriteString("cd \"$HOME\" 2>/dev/null; ")
	}
	// Run the real command, capture its exit code, log start/end.
	fmt.Fprintf(&b, "printf '[%%s] launch %s\\n' \"$(date '+%%F %%T')\" >> \"$LOG\"; ", shBanner(herdrSession))
	fmt.Fprintf(&b, "%s; rc=$?; ", inner)
	b.WriteString("printf '[%s] exit rc=%s\\n' \"$(date '+%F %T')\" \"$rc\" >> \"$LOG\"; ")
	// Hold the window open on any non-zero exit so the error stays visible.
	b.WriteString("if [ \"$rc\" -ne 0 ]; then printf '\\n\\033[31mcowboy: session exited with code %s\\033[0m  (log: %s)\\npress Enter to close…' \"$rc\" \"$LOG\"; read _; fi")
	return b.String()
}

// Launch opens the project in a new terminal window/tab, honoring per-project
// terminal and window-size overrides. herdrSession overrides the herdr
// --session name (empty = the project's own name). Returns the terminal used.
func (l *Launcher) Launch(s config.Session, herdrSession, logDir string) (string, error) {
	name := herdrSession
	if name == "" {
		name = s.Name
	}
	logPath := filepath.Join(logDir, name+".log")

	term, err := l.terminalFor(s)
	if err != nil {
		appendLog(logPath, fmt.Sprintf("ERROR selecting terminal: %v", err))
		return "", err
	}

	opts := WindowOpts{
		Shell:  l.Shell,
		Login:  l.Login,
		NewTab: l.NewTab,
		Cols:   firstNonZero(s.Cols, l.Cols),
		Rows:   firstNonZero(s.Rows, l.Rows),
	}

	script := l.Script(s, herdrSession, logPath)
	argv := term.Argv(script, opts)
	if len(argv) == 0 {
		return "", fmt.Errorf("terminal %s produced no command", term.ID)
	}

	// Record the attempt server-side, so there is always a log even if the
	// window itself never opens (e.g. terminal binary is broken).
	l.record(logPath, s, name, term.ID, argv)

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = cleanEnv()
	cmd.Stdout = nil
	cmd.Stderr = nil
	// Detach the launched terminal into its own session/process group so it
	// outlives terminal-cowboy: Ctrl-C on the server (SIGINT to its process
	// group) and closing the server's terminal (SIGHUP to its session) must not
	// tear down windows we opened. Without this, a window-size override makes
	// wezterm spawn a GUI as our child, which then dies with us.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		appendLog(logPath, fmt.Sprintf("ERROR starting %s: %v", term.ID, err))
		return "", fmt.Errorf("start %s: %w", term.ID, err)
	}
	// Reap the launcher process (most exit immediately after spawning the window).
	go cmd.Wait()
	return term.ID, nil
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// record appends a launch header to the log describing exactly what was run.
func (l *Launcher) record(logPath string, s config.Session, herdrSession, termID string, argv []string) {
	var b strings.Builder
	b.WriteString("\n──────────────────────────────────────────────\n")
	fmt.Fprintf(&b, "launch project=%s session=%s terminal=%s\n", s.Name, herdrSession, termID)
	if s.Cwd != "" {
		fmt.Fprintf(&b, "cwd=%s\n", s.Cwd)
	}
	fmt.Fprintf(&b, "op_env=%v env_keys=%d\n", s.HasOpEnv, len(s.Env))
	fmt.Fprintf(&b, "argv=%q\n", argv)
	appendLog(logPath, b.String())
}

func appendLog(logPath, msg string) {
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(msg)
	if !strings.HasSuffix(msg, "\n") {
		_, _ = f.WriteString("\n")
	}
}

// shBanner makes a string safe to embed inside a single-quoted printf format.
func shBanner(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `'\''`)
	s = strings.ReplaceAll(s, `%`, `%%`)
	return s
}

// cleanEnv returns the current environment minus herdr's session-context vars.
func cleanEnv() []string {
	skip := make(map[string]bool, len(nestingVars))
	for _, v := range nestingVars {
		skip[v] = true
	}
	env := os.Environ()
	out := env[:0]
	for _, kv := range env {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if !skip[name] {
			out = append(out, kv)
		}
	}
	return out
}

func sortedEnv(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// shQuote single-quotes a string for safe use in a POSIX shell command.
func shQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
