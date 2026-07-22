// Package tmux queries and controls tmux sessions (which also back sesh).
package tmux

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Client shells out to the tmux binary.
type Client struct {
	Bin string
}

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "tmux"
}

// cleanEnv drops $TMUX/$TMUX_PANE so a query issued from inside tmux targets the
// running server rather than erroring about nested clients.
func cleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if name == "TMUX" || name == "TMUX_PANE" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// List returns the names of all running tmux sessions. A missing tmux server
// (no sessions) is not an error — it returns an empty slice.
func (c *Client) List(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.bin(), "list-sessions", "-F", "#{session_name}")
	cmd.Env = cleanEnv()
	out, err := cmd.Output()
	if err != nil {
		// tmux exits non-zero with "no server running" when nothing is up.
		if strings.Contains(strings.ToLower(string(exitStderr(err))), "no server") {
			return nil, nil
		}
		if len(out) == 0 {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// RunningSet returns a set of running session names.
func (c *Client) RunningSet(ctx context.Context) map[string]bool {
	names, _ := c.List(ctx)
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// Stop kills a tmux session by name.
func (c *Client) Stop(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin(), "kill-session", "-t", name)
	cmd.Env = cleanEnv()
	return cmd.Run()
}

func exitStderr(err error) []byte {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.Stderr
	}
	return nil
}
