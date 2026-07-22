// Package herdr queries the herdr CLI for live session state.
package herdr

import (
	"context"
	"encoding/json"
	"os/exec"
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
