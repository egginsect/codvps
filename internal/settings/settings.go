// Package settings stores operator-owned, non-secret codvps configuration.
package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/egginsect/codvps/internal/fsutil"
	"github.com/egginsect/codvps/internal/paths"
)

// Config contains paths, never credential values. Empty values retain the
// existing HOME/XDG and systemd behavior.
type Config struct {
	RepoRoot        string `json:"repo_root,omitempty"`
	GitHubConfigDir string `json:"github_config_dir,omitempty"`
	GitConfigGlobal string `json:"git_config_global,omitempty"`
	SessionMode     string `json:"session_mode,omitempty"`
}

const maxBytes = 64 << 10

func Path(layout *paths.Layout) string { return filepath.Join(layout.ConfigDir(), "settings.json") }

// Read fails closed for malformed, oversized or non-regular configuration.
func Read(layout *paths.Layout) (Config, error) {
	var c Config
	path := Path(layout)
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if !fi.Mode().IsRegular() {
		return c, fmt.Errorf("settings must be a regular file: %s", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return c, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return c, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(fi, opened) {
		return c, fmt.Errorf("settings changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return c, err
	}
	if len(data) > maxBytes {
		return c, fmt.Errorf("settings exceed %d bytes", maxBytes)
	}
	if !utf8.Valid(data) {
		return c, fmt.Errorf("settings are not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return c, fmt.Errorf("settings must be a JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return c, fmt.Errorf("invalid settings: %w", err)
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return c, fmt.Errorf("invalid or duplicate setting %q", token)
		}
		seen[key] = true
		var value *string
		switch key {
		case "repo_root":
			value = &c.RepoRoot
		case "github_config_dir":
			value = &c.GitHubConfigDir
		case "git_config_global":
			value = &c.GitConfigGlobal
		case "session_mode":
			value = &c.SessionMode
		default:
			return c, fmt.Errorf("unknown setting %q", key)
		}
		token, err = dec.Token()
		if err != nil {
			return c, fmt.Errorf("invalid settings: %w", err)
		}
		text, ok := token.(string)
		if !ok {
			return c, fmt.Errorf("setting %s must be a string", key)
		}
		*value = text
	}
	if token, err := dec.Token(); err != nil || token != json.Delim('}') {
		return c, fmt.Errorf("invalid settings object")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("settings must contain exactly one JSON object")
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

func (c Config) Validate() error {
	for _, item := range []struct{ key, value string }{
		{"repo_root", c.RepoRoot}, {"github_config_dir", c.GitHubConfigDir}, {"git_config_global", c.GitConfigGlobal},
	} {
		if item.value != "" && (!filepath.IsAbs(item.value) || filepath.Clean(item.value) != item.value || strings.IndexFunc(item.value, unicode.IsControl) >= 0) {
			return fmt.Errorf("%s must be a clean absolute path without control characters", item.key)
		}
	}
	if c.SessionMode != "" && c.SessionMode != "systemd" && c.SessionMode != "manual" {
		return fmt.Errorf("session_mode must be systemd or manual")
	}
	return nil
}

func Write(layout *paths.Layout, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(Path(layout), append(data, '\n'), 0600)
}

func RepositoryRoot(layout *paths.Layout) (string, error) {
	c, err := Read(layout)
	if err != nil {
		return "", err
	}
	if c.RepoRoot == "" {
		return layout.Home(), nil
	}
	return c.RepoRoot, nil
}

// Environment configures child commands without changing the caller's shell
// or reading/copying credentials. Explicit codvps settings override inherited
// GH_CONFIG_DIR/GIT_CONFIG_GLOBAL for these commands only.
func Environment(layout *paths.Layout) ([]string, error) {
	c, err := Read(layout)
	if err != nil {
		return nil, err
	}
	var env []string
	if c.GitHubConfigDir != "" {
		env = append(env, "GH_CONFIG_DIR="+c.GitHubConfigDir)
	}
	if c.GitConfigGlobal != "" {
		env = append(env, "GIT_CONFIG_GLOBAL="+c.GitConfigGlobal)
	}
	return env, nil
}
