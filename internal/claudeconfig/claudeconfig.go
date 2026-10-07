// Package claudeconfig edits the two keys codvps owns inside the Claude
// CLI's own configuration file, ~/.claude.json:
//
//   - remoteDialogSeen (top level): the operator has accepted Remote
//     Control consent. Seeded by `codvps login claude` and whenever the
//     Claude head attaches a repository.
//   - projects["<abs path>"].hasTrustDialogAccepted: the operator trusts
//     that workspace. Without it `claude remote-control` exits with
//     "Workspace not trusted" and systemd restarts it forever. Seeded for
//     every registered repository when the Claude head is enabled -- the
//     operator running `codvps head enable claude` is the consent act, and
//     codvps never opens an interactive Claude session to obtain it.
//
// Both keys are undocumented Claude CLI state that the reference verified
// empirically. Everything else in the file belongs to the Claude CLI and is
// preserved byte-for-byte in value and in key order; only the indentation is
// normalized (two spaces, trailing newline, matching the reference's
// json.dump(indent=2)). Writes are atomic (temp file + rename), so a crash
// can never leave the operator with a truncated ~/.claude.json.
package claudeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/egginsect/codvps/internal/fsutil"
)

// FileName is the Claude CLI config file name under the operator's home.
const FileName = ".claude.json"

// maxConfigBytes bounds how much of ~/.claude.json is read. The Claude CLI
// keeps per-project history there, so it can be large, but an unbounded
// read of a file in the operator's home is still a defect.
const maxConfigBytes = 64 << 20

// Path returns ~/.claude.json for the given home directory.
func Path(home string) string {
	return filepath.Join(home, FileName)
}

// SeedRemoteConsent sets remoteDialogSeen=true in ~/.claude.json, creating
// the file if it does not exist yet.
func SeedRemoteConsent(home string) error {
	return update(Path(home), func(path string, cfg *object) (bool, error) {
		return cfg.set("remoteDialogSeen", json.RawMessage("true")), nil
	})
}

// SeedWorkspaceTrust marks every given absolute workspace path as trusted
// (projects[path].hasTrustDialogAccepted=true) in one read-modify-write of
// ~/.claude.json. It is a no-op for an empty list, like the reference's
// variadic seed_workspace_trust.
func SeedWorkspaceTrust(home string, workspaces []string) error {
	if len(workspaces) == 0 {
		return nil
	}
	for _, ws := range workspaces {
		if !filepath.IsAbs(ws) {
			return fmt.Errorf("workspace trust requires an absolute path, got %q", ws)
		}
	}
	return update(Path(home), func(path string, cfg *object) (bool, error) {
		projects := &object{}
		if raw, ok := cfg.get("projects"); ok {
			if err := projects.unmarshal(raw); err != nil {
				return false, fmt.Errorf("%s .projects must be an object", path)
			}
		}
		changedProjects := false
		for _, ws := range workspaces {
			project := &object{}
			if raw, ok := projects.get(ws); ok {
				if err := project.unmarshal(raw); err != nil {
					return false, fmt.Errorf("%s .projects[%q] must be an object", path, ws)
				}
			}
			if !project.set("hasTrustDialogAccepted", json.RawMessage("true")) {
				continue
			}
			encoded, err := project.marshal()
			if err != nil {
				return false, err
			}
			projects.set(ws, encoded)
			changedProjects = true
		}
		if !changedProjects {
			return false, nil
		}
		encoded, err := projects.marshal()
		if err != nil {
			return false, err
		}
		cfg.set("projects", encoded)
		return true, nil
	})
}

// RemoteConsentSeeded reports whether ~/.claude.json records Remote
// Control consent (remoteDialogSeen is literally true), the reference's
// remote_consent_is_seeded. A missing or unreadable file, or one that is
// not a JSON object, is "not seeded" rather than an error, as in the
// reference, since doctor only reports the state.
func RemoteConsentSeeded(home string) bool {
	data, err := readBounded(Path(home))
	if err != nil {
		return false
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false
	}
	return string(cfg["remoteDialogSeen"]) == "true"
}

// WorkspaceTrusted reports whether ~/.claude.json marks workspace as
// trusted, using the same rule the Claude CLI applies: the entry must exist
// and hasTrustDialogAccepted must be literally true. A missing file is
// "not trusted", not an error.
func WorkspaceTrusted(home, workspace string) (bool, error) {
	path := Path(home)
	data, err := readBounded(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	var cfg struct {
		Projects map[string]struct {
			HasTrustDialogAccepted json.RawMessage `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	entry, ok := cfg.Projects[workspace]
	return ok && string(entry.HasTrustDialogAccepted) == "true", nil
}

// update performs one read-modify-write of the config file at path. mutate
// reports whether it changed anything; an unchanged file is not rewritten.
// A symlinked ~/.claude.json is written through to its target (as the
// reference's plain open("w") would), but atomically.
func update(path string, mutate func(path string, cfg *object) (bool, error)) error {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to resolve %s: %w", path, err)
	}

	mode := os.FileMode(0o600)
	cfg := &object{}
	data, err := readBounded(target)
	switch {
	case err == nil:
		if err := cfg.unmarshal(data); err != nil {
			return fmt.Errorf("%s must contain a JSON object: %w", path, err)
		}
		fi, statErr := os.Stat(target)
		if statErr != nil {
			return fmt.Errorf("failed to stat %s: %w", path, statErr)
		}
		mode = fi.Mode().Perm()
	case errors.Is(err, os.ErrNotExist):
	default:
		return err
	}

	changed, err := mutate(path, cfg)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	compact, err := cfg.marshal()
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return fmt.Errorf("failed to format %s: %w", path, err)
	}
	out.WriteByte('\n')
	if err := fsutil.AtomicWrite(target, out.Bytes(), mode); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }() // read-only handle; a close error cannot lose data
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes; refusing to rewrite it", path, maxConfigBytes)
	}
	return data, nil
}

// object is a JSON object that keeps its members' order and raw values, so
// rewriting two keys never reorders or reformats the values of the rest.
type object struct {
	members []member
}

type member struct {
	key   string
	value json.RawMessage
}

func (o *object) unmarshal(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return errors.New("not a JSON object")
	}
	o.members = nil
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return errors.New("object key is not a string")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
		o.members = append(o.members, member{key: key, value: value})
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the JSON object")
	}
	return nil
}

// get returns the value JSON parsers would see for key: the last duplicate
// wins, as it does for the Claude CLI's own parser.
func (o *object) get(key string) (json.RawMessage, bool) {
	for i := len(o.members) - 1; i >= 0; i-- {
		if o.members[i].key == key {
			return o.members[i].value, true
		}
	}
	return nil, false
}

// set stores value under key (replacing the effective, last occurrence in
// place, or appending) and reports whether the stored value changed.
func (o *object) set(key string, value json.RawMessage) bool {
	for i := len(o.members) - 1; i >= 0; i-- {
		if o.members[i].key == key {
			if bytes.Equal(o.members[i].value, value) {
				return false
			}
			o.members[i].value = value
			return true
		}
	}
	o.members = append(o.members, member{key: key, value: value})
	return true
}

func (o *object) marshal() (json.RawMessage, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o.members {
		if i > 0 {
			b.WriteByte(',')
		}
		// An Encoder with HTML escaping off, so a key containing <, > or &
		// round-trips unchanged instead of becoming < and friends.
		var key bytes.Buffer
		enc := json.NewEncoder(&key)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(m.key); err != nil {
			return nil, fmt.Errorf("failed to encode key %q: %w", m.key, err)
		}
		b.Write(bytes.TrimRight(key.Bytes(), "\n"))
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
