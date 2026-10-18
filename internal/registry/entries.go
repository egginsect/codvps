package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/egginsect/codvps/internal/fsutil"
)

const maxRegistryBytes = 1 << 20

// Entry records one managed repository. An empty Path is a legacy entry:
// consumers must resolve it as HOME/Name, never against a configured repository
// root. Explicit paths remain fixed if the configured root changes.
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

// Read reads repository names, retaining the order stored in the registry.
// Use ReadEntries when the associated checkout paths are needed.
func Read(path string) ([]string, error) {
	entries, err := ReadEntries(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names, nil
}

// ReadEntries reads either a legacy name-per-line registry or a version-1
// JSON registry. Missing files represent empty registries, including on
// first use before the parent directory exists. Other filesystem errors are
// never treated as an empty registry.
func ReadEntries(path string) ([]Entry, error) {
	content, err := readRegistry(path)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(content) {
		return nil, fmt.Errorf("registry is not valid UTF-8")
	}
	var entries []Entry
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		entries, err = decodeEntries(content)
	} else {
		entries, err = decodeLegacyEntries(content)
	}
	if err != nil {
		return nil, fmt.Errorf("invalid registry: %w", err)
	}
	if err := validateEntries(entries); err != nil {
		return nil, fmt.Errorf("invalid registry: %w", err)
	}
	return entries, nil
}

func readRegistry(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := verifyMissingRegistry(path); err != nil {
				return nil, err
			}
			return nil, nil
		}
		return nil, fmt.Errorf("failed to inspect registry: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("registry must be a regular file, not a symlink or special file")
	}
	if before.Size() > maxRegistryBytes {
		return nil, fmt.Errorf("registry exceeds the maximum allowed size of %d bytes", maxRegistryBytes)
	}
	// Refuse a replacement symlink, and avoid blocking if the regular file
	// was exchanged for a FIFO between Lstat and OpenFile.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open registry: %w", err)
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to inspect opened registry: %w", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("registry changed while opening it or is not a regular file")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxRegistryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read registry: %w", err)
	}
	if len(content) > maxRegistryBytes {
		return nil, fmt.Errorf("registry exceeds the maximum allowed size of %d bytes", maxRegistryBytes)
	}
	return content, nil
}

// A genuinely absent first-run state directory is harmless. An ENOENT
// caused by a dangling parent symlink is not a new, empty registry.
func verifyMissingRegistry(path string) error {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				info, err = os.Stat(parent)
				if err != nil {
					return fmt.Errorf("failed to inspect registry directory: %w", err)
				}
			}
			if !info.IsDir() {
				return fmt.Errorf("registry parent is not a directory")
			}
			return nil
		}
		if !os.IsNotExist(err) || filepath.Dir(parent) == parent {
			return fmt.Errorf("failed to inspect registry directory: %w", err)
		}
	}
}

func decodeLegacyEntries(content []byte) ([]Entry, error) {
	entries := make([]Entry, 0)
	for _, line := range strings.Split(string(content), "\n") {
		// Keep support for CRLF and surrounding spaces, but never silently
		// turn control characters into a different repository name.
		line = strings.TrimSuffix(line, "\r")
		for _, r := range line {
			if unicode.IsControl(r) {
				return nil, fmt.Errorf("legacy registry contains a control character")
			}
		}
		line = strings.TrimSpace(line)
		if line != "" {
			entries = append(entries, Entry{Name: line})
		}
	}
	return entries, nil
}

// Decode the small schema explicitly: encoding/json's struct decoder accepts
// case-insensitive fields and duplicate keys, neither of which is safe for a
// file that determines which checkout an operation will act on.
func decodeEntries(content []byte) ([]Entry, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := expectDelimiter(decoder, '{'); err != nil {
		return nil, err
	}
	fields := make(map[string]bool)
	var entries []Entry
	for decoder.More() {
		key, err := readField(decoder, fields)
		if err != nil {
			return nil, err
		}
		switch key {
		case "version":
			version, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			if version != json.Number("1") {
				return nil, fmt.Errorf("unsupported registry version %v", version)
			}
		case "repositories":
			if err := expectDelimiter(decoder, '['); err != nil {
				return nil, err
			}
			entries = make([]Entry, 0)
			for decoder.More() {
				entry, err := decodeEntry(decoder)
				if err != nil {
					return nil, err
				}
				entries = append(entries, entry)
			}
			if err := expectDelimiter(decoder, ']'); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unknown registry field %q", key)
		}
	}
	if err := expectDelimiter(decoder, '}'); err != nil {
		return nil, err
	}
	if !fields["version"] || !fields["repositories"] {
		return nil, fmt.Errorf("registry requires version and repositories fields")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after registry JSON")
	}
	return entries, nil
}

func decodeEntry(decoder *json.Decoder) (Entry, error) {
	var entry Entry
	if err := expectDelimiter(decoder, '{'); err != nil {
		return entry, err
	}
	fields := make(map[string]bool)
	for decoder.More() {
		key, err := readField(decoder, fields)
		if err != nil {
			return entry, err
		}
		if key != "name" && key != "path" {
			return entry, fmt.Errorf("unknown repository field %q", key)
		}
		token, err := decoder.Token()
		if err != nil {
			return entry, err
		}
		value, ok := token.(string)
		if !ok {
			return entry, fmt.Errorf("repository field %q must be a string", key)
		}
		if key == "name" {
			entry.Name = value
		} else {
			entry.Path = value
		}
	}
	if err := expectDelimiter(decoder, '}'); err != nil {
		return entry, err
	}
	if !fields["name"] {
		return entry, fmt.Errorf("repository entry requires a name")
	}
	return entry, nil
}

func readField(decoder *json.Decoder, seen map[string]bool) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	key, ok := token.(string)
	if !ok {
		return "", fmt.Errorf("expected a JSON field name")
	}
	if seen[key] {
		return "", fmt.Errorf("duplicate JSON field %q", key)
	}
	seen[key] = true
	return key, nil
}

func expectDelimiter(decoder *json.Decoder, want json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != want {
		return fmt.Errorf("expected JSON delimiter %q", want)
	}
	return nil
}

func validateEntries(entries []Entry) error {
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if err := ValidateName(entry.Name); err != nil {
			return err
		}
		if seen[entry.Name] {
			return fmt.Errorf("duplicate repository name %q", entry.Name)
		}
		seen[entry.Name] = true
		if entry.Path == "" {
			continue
		}
		if !utf8.ValidString(entry.Path) || strings.IndexFunc(entry.Path, unicode.IsControl) >= 0 {
			return fmt.Errorf("repository %q path contains invalid UTF-8 or a control character", entry.Name)
		}
		if !filepath.IsAbs(entry.Path) || filepath.Clean(entry.Path) != entry.Path {
			return fmt.Errorf("repository %q path must be an absolute, clean path", entry.Name)
		}
	}
	return nil
}

// Write updates repository names without losing existing explicit paths.
// New names remain legacy HOME/name entries. Names are sorted and deduplicated.
func Write(path string, names []string) error {
	return withRegistryLock(path, func() error {
		existing, err := ReadEntries(path)
		if err != nil {
			return err
		}
		byName := make(map[string]Entry, len(existing))
		for _, entry := range existing {
			byName[entry.Name] = entry
		}
		seen := make(map[string]bool, len(names))
		entries := make([]Entry, 0, len(names))
		for _, name := range names {
			if seen[name] {
				continue
			}
			seen[name] = true
			entry, ok := byName[name]
			if !ok {
				entry = Entry{Name: name}
			}
			entries = append(entries, entry)
		}
		return writeEntries(path, entries)
	})
}

// WriteEntries validates and atomically replaces the registry with sorted
// records and mode 0600. All-legacy entries retain the name-per-line format;
// any explicit checkout path selects the version-1 JSON format.
func WriteEntries(path string, entries []Entry) error {
	return withRegistryLock(path, func() error {
		return writeEntries(path, entries)
	})
}

// writeEntries requires the caller to hold the stable registry lock.
func writeEntries(path string, entries []Entry) error {
	if err := validateEntries(entries); err != nil {
		return fmt.Errorf("invalid registry: %w", err)
	}
	ordered := append([]Entry(nil), entries...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	hasPaths := false
	for _, entry := range ordered {
		if entry.Path != "" {
			hasPaths = true
			break
		}
	}
	var content []byte
	if hasPaths {
		document := struct {
			Version      int     `json:"version"`
			Repositories []Entry `json:"repositories"`
		}{Version: 1, Repositories: ordered}
		var err error
		content, err = json.MarshalIndent(document, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to encode registry: %w", err)
		}
		content = append(content, '\n')
	} else {
		var builder strings.Builder
		for _, entry := range ordered {
			builder.WriteString(entry.Name)
			builder.WriteByte('\n')
		}
		content = []byte(builder.String())
	}
	if len(content) > maxRegistryBytes {
		return fmt.Errorf("registry exceeds the maximum allowed size of %d bytes", maxRegistryBytes)
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("registry must be a regular file, not a symlink or special file")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to inspect registry: %w", err)
	}
	return fsutil.AtomicWrite(path, content, 0o600)
}
