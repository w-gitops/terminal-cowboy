package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidateName ensures a session name is safe to use as a directory name and as
// a herdr --session argument.
func ValidateName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if !nameRe.MatchString(name) {
		return errors.New("name may only contain letters, numbers, '.', '_' and '-'")
	}
	if name == "." || name == ".." {
		return errors.New("invalid name")
	}
	return nil
}

// SaveSession writes sessions/<name>/session.toml. It creates the directory if
// needed. If opEnv is non-nil it (over)writes .op.env; an empty (but non-nil)
// value removes it.
func (c *Config) SaveSession(s Session, opEnv *string) error {
	if err := ValidateName(s.Name); err != nil {
		return err
	}
	sdir := filepath.Join(c.SessionsDir, s.Name)
	if err := os.MkdirAll(sdir, 0o700); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	// Only persist the user-facing fields; runtime fields are tagged toml:"-".
	var b strings.Builder
	enc := toml.NewEncoder(&b)
	if err := enc.Encode(s); err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	tomlPath := filepath.Join(sdir, "session.toml")
	if err := os.WriteFile(tomlPath, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tomlPath, err)
	}

	if opEnv != nil {
		opPath := filepath.Join(sdir, ".op.env")
		if strings.TrimSpace(*opEnv) == "" {
			if err := os.Remove(opPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove .op.env: %w", err)
			}
		} else {
			content := *opEnv
			if !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			if err := os.WriteFile(opPath, []byte(content), 0o600); err != nil {
				return fmt.Errorf("write .op.env: %w", err)
			}
		}
	}
	return nil
}

// SaveGlobal writes config.toml with the given global settings.
func (c *Config) SaveGlobal(g Global) error {
	if err := os.MkdirAll(c.Root, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(g); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	path := filepath.Join(c.Root, "config.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	c.Global = g
	return nil
}

// DeleteSession removes sessions/<name> and everything under it.
func (c *Config) DeleteSession(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	sdir := filepath.Join(c.SessionsDir, name)
	// Guard against escaping the sessions dir.
	if filepath.Dir(sdir) != filepath.Clean(c.SessionsDir) {
		return errors.New("invalid session path")
	}
	if err := os.RemoveAll(sdir); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// ReadOpEnv returns the raw contents of a session's .op.env, or "" if absent.
func (c *Config) ReadOpEnv(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(c.SessionsDir, name, ".op.env"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}
