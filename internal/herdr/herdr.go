// Package herdr queries the herdr CLI for live session state.
package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Session mirrors one entry from `herdr session list --json`.
type Session struct {
	Name       string `json:"name"`
	Running    bool   `json:"running"`
	Default    bool   `json:"default"`
	SessionDir string `json:"session_dir"`
	SocketPath string `json:"socket_path"`
}

type listOutput struct {
	Sessions []Session `json:"sessions"`
}

// Client shells out to the herdr binary.
type Client struct {
	Bin string
}

// List returns the running/known herdr sessions. A non-nil error means herdr
// could not be queried (not installed, server down); callers treat that as
// "state unknown" rather than fatal.
func (c *Client) List(ctx context.Context) ([]Session, error) {
	bin := c.Bin
	if bin == "" {
		bin = "herdr"
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, bin, "session", "list", "--json").Output()
	if err != nil {
		return nil, err
	}
	var lo listOutput
	if err := json.Unmarshal(out, &lo); err != nil {
		return nil, err
	}
	return lo.Sessions, nil
}

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "herdr"
}

// socketEnv returns the process environment with any herdr socket-selecting
// vars removed and HERDR_SOCKET_PATH pinned to sock, so a `herdr` subcommand
// targets exactly that session's API socket.
func socketEnv(sock string) []string {
	var out []string
	for _, kv := range os.Environ() {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if name == "HERDR_SOCKET_PATH" || name == "HERDR_CLIENT_SOCKET_PATH" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HERDR_SOCKET_PATH="+sock)
}

type wsListOutput struct {
	Result struct {
		Workspaces []struct {
			WorkspaceID string `json:"workspace_id"`
			Focused     bool   `json:"focused"`
			Number      int    `json:"number"`
			Label       string `json:"label"`
		} `json:"workspaces"`
	} `json:"result"`
}

// LabelInitialWorkspace waits (until ctx is done) for sessionName to be running,
// then renames its initial/focused workspace to label. Best-effort: it returns
// an error for logging but callers treat failure as non-fatal.
func (c *Client) LabelInitialWorkspace(ctx context.Context, sessionName, label string) error {
	// 1. Wait for the session to be running and expose a socket.
	var sock string
	for sock == "" {
		if sessions, err := c.List(ctx); err == nil {
			for _, s := range sessions {
				if s.Name == sessionName && s.Running && s.SocketPath != "" {
					sock = s.SocketPath
					break
				}
			}
		}
		if sock != "" {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("session %q did not come up in time", sessionName)
		case <-time.After(400 * time.Millisecond):
		}
	}

	env := socketEnv(sock)

	// 2. Find the focused (initial) workspace id.
	listCmd := exec.CommandContext(ctx, c.bin(), "workspace", "list")
	listCmd.Env = env
	out, err := listCmd.Output()
	if err != nil {
		return fmt.Errorf("workspace list: %w", err)
	}
	var wl wsListOutput
	if err := json.Unmarshal(out, &wl); err != nil {
		return fmt.Errorf("parse workspace list: %w", err)
	}
	if len(wl.Result.Workspaces) == 0 {
		return fmt.Errorf("no workspaces for session %q", sessionName)
	}
	wsID := wl.Result.Workspaces[0].WorkspaceID
	for _, ws := range wl.Result.Workspaces {
		if ws.Focused {
			wsID = ws.WorkspaceID
			break
		}
	}

	// 3. Rename it.
	renameCmd := exec.CommandContext(ctx, c.bin(), "workspace", "rename", wsID, label)
	renameCmd.Env = env
	if err := renameCmd.Run(); err != nil {
		return fmt.Errorf("workspace rename: %w", err)
	}
	return nil
}

// Stop stops a running herdr session by name.
func (c *Client) Stop(ctx context.Context, name string) error {
	bin := c.Bin
	if bin == "" {
		bin = "herdr"
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, bin, "session", "stop", name).Run()
}

// RunningSet returns a map of session name -> running for quick lookups.
func RunningSet(sessions []Session) map[string]bool {
	m := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		m[s.Name] = s.Running
	}
	return m
}
