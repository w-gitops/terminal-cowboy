package launcher

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Terminal knows how to open a new window (or tab) running a shell script.
type Terminal struct {
	ID   string // wezterm|kitty|iterm2|ghostty
	Bin  string // resolved absolute path to the launcher binary
	kind terminalKind
}

type terminalKind int

const (
	kindWezterm terminalKind = iota
	kindKitty
	kindGhostty
	kindITerm2 // driven via osascript
)

// spec describes how to detect and invoke a terminal.
type spec struct {
	id      string
	kind    terminalKind
	probe   string   // primary binary name to look up on PATH
	fallbks []string // additional macOS app-bundle paths to try
}

var specs = []spec{
	{id: "wezterm", kind: kindWezterm, probe: "wezterm",
		fallbks: []string{"/Applications/WezTerm.app/Contents/MacOS/wezterm"}},
	{id: "kitty", kind: kindKitty, probe: "kitty",
		fallbks: []string{"/Applications/kitty.app/Contents/MacOS/kitty"}},
	{id: "ghostty", kind: kindGhostty, probe: "ghostty",
		fallbks: []string{"/Applications/Ghostty.app/Contents/MacOS/ghostty"}},
	{id: "iterm2", kind: kindITerm2, probe: "osascript"}, // needs macOS + iTerm installed
}

// detectionOrder returns the preferred terminal order for the host OS.
func detectionOrder() []string {
	if runtime.GOOS == "darwin" {
		return []string{"ghostty", "iterm2", "wezterm", "kitty"}
	}
	return []string{"wezterm", "kitty", "ghostty"}
}

// resolveBin finds the launcher binary for a spec, honoring an explicit override.
func (s spec) resolveBin(override string) (string, bool) {
	if override != "" {
		return override, true
	}
	if p, err := exec.LookPath(s.probe); err == nil {
		return p, true
	}
	for _, fb := range s.fallbks {
		if _, err := exec.LookPath(fb); err == nil {
			return fb, true
		}
	}
	return "", false
}

// iterm2Available reports whether iTerm2 itself is installed (osascript alone is
// not enough — it ships with macOS).
func iterm2Available() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	out, err := exec.Command("osascript", "-e",
		`id of application "iTerm2"`).Output()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return true
	}
	out, err = exec.Command("osascript", "-e", `id of application "iTerm"`).Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// Select picks a terminal: an explicit choice ("wezterm", …) or "auto"/"" to
// detect. weztermOverride is an optional path from config.
func Select(choice, weztermOverride string) (*Terminal, error) {
	choice = strings.ToLower(strings.TrimSpace(choice))

	try := func(id string) (*Terminal, bool) {
		for _, s := range specs {
			if s.id != id {
				continue
			}
			override := ""
			if id == "wezterm" {
				override = weztermOverride
			}
			if s.kind == kindITerm2 {
				if !iterm2Available() {
					return nil, false
				}
				bin, ok := s.resolveBin("")
				if !ok {
					return nil, false
				}
				return &Terminal{ID: s.id, Bin: bin, kind: s.kind}, true
			}
			bin, ok := s.resolveBin(override)
			if !ok {
				return nil, false
			}
			return &Terminal{ID: s.id, Bin: bin, kind: s.kind}, true
		}
		return nil, false
	}

	if choice != "" && choice != "auto" {
		t, ok := try(choice)
		if !ok {
			return nil, fmt.Errorf("terminal %q not found on this system", choice)
		}
		return t, nil
	}

	for _, id := range detectionOrder() {
		if t, ok := try(id); ok {
			return t, nil
		}
	}
	return nil, fmt.Errorf("no supported terminal found (tried %s)",
		strings.Join(detectionOrder(), ", "))
}

// Detect returns the IDs of all terminals available on this host.
func Detect() []string {
	var out []string
	for _, id := range detectionOrder() {
		if _, err := Select(id, ""); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// WindowOpts controls how the new window/tab is opened.
type WindowOpts struct {
	Shell  string // shell binary, e.g. "sh"; defaults to "sh" if empty
	Login  bool   // run it as a login shell (-l)
	NewTab bool   // spawn a new tab instead of a new window, where supported
	Cols   int    // initial columns (0 = terminal default)
	Rows   int    // initial rows (0 = terminal default)
}

// shellArgv builds [shell, -l?, -c, script].
func (o WindowOpts) shellArgv(script string) []string {
	sh := o.Shell
	if sh == "" {
		sh = "sh"
	}
	argv := []string{sh}
	if o.Login {
		argv = append(argv, "-l")
	}
	return append(argv, "-c", script)
}

// Argv builds the exec argv that opens a new window/tab running `script`.
func (t *Terminal) Argv(script string, o WindowOpts) []string {
	sh := o.shellArgv(script)
	switch t.kind {
	case kindWezterm:
		args := []string{t.Bin}
		if o.Cols > 0 {
			args = append(args, "--config", fmt.Sprintf("initial_cols=%d", o.Cols))
		}
		if o.Rows > 0 {
			args = append(args, "--config", fmt.Sprintf("initial_rows=%d", o.Rows))
		}
		args = append(args, "start")
		if o.NewTab {
			args = append(args, "--new-tab")
		}
		args = append(args, "--")
		return append(args, sh...)
	case kindKitty:
		// `kitty <cmd>` opens a new OS window running the command.
		args := []string{t.Bin}
		if o.Cols > 0 {
			args = append(args, "-o", fmt.Sprintf("initial_window_width=%dc", o.Cols))
		}
		if o.Rows > 0 {
			args = append(args, "-o", fmt.Sprintf("initial_window_height=%dc", o.Rows))
		}
		return append(args, sh...)
	case kindGhostty:
		args := []string{t.Bin}
		if o.Cols > 0 {
			args = append(args, fmt.Sprintf("--window-width=%d", o.Cols))
		}
		if o.Rows > 0 {
			args = append(args, fmt.Sprintf("--window-height=%d", o.Rows))
		}
		return append(args, append([]string{"-e"}, sh...)...)
	case kindITerm2:
		return iterm2Argv(script, o)
	}
	return nil
}

// iterm2Argv drives iTerm2 through AppleScript, since it has no direct exec CLI.
func iterm2Argv(script string, o WindowOpts) []string {
	target := "create window with default profile"
	sessionRef := "current session of current window"
	if o.NewTab {
		target = "tell current window to create tab with default profile"
		sessionRef = "current session of current tab of current window"
	}
	var size string
	if o.Cols > 0 {
		size += fmt.Sprintf("\n    set columns to %d", o.Cols)
	}
	if o.Rows > 0 {
		size += fmt.Sprintf("\n    set rows to %d", o.Rows)
	}
	as := fmt.Sprintf(`tell application "iTerm2"
  activate
  %s
  tell %s%s
    write text %s
  end tell
end tell`, target, sessionRef, size, appleQuote(script))
	return []string{"osascript", "-e", as}
}

// appleQuote wraps a string as an AppleScript string literal.
func appleQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
