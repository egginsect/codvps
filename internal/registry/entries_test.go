package registry

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func registryFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repositories")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadEntriesLegacy(t *testing.T) {
	path := registryFixture(t, "repo-b\r\n\n repo-a \r\n")
	t.Setenv("HOME", "/different/home")
	got, err := ReadEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{{Name: "repo-b"}, {Name: "repo-a"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadEntries = %#v, want %#v", got, want)
	}
}

func TestEntriesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new-parent", "registry")
	original := []Entry{
		{Name: "z-legacy"},
		{Name: "quoted", Path: "/mnt/with spaces/a\"b"},
		{Name: "alpha", Path: "/srv/開發/project"},
	}
	before := append([]Entry(nil), original...)
	if err := WriteEntries(path, original); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, before) {
		t.Fatal("WriteEntries mutated the caller's entries")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("{\n  \"version\": 1,")) {
		t.Fatalf("registry is not versioned JSON: %s", data)
	}
	got, err := ReadEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{original[2], original[1], original[0]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadEntries = %#v, want %#v", got, want)
	}
	names, err := Read(path)
	if err != nil || !reflect.DeepEqual(names, []string{"alpha", "quoted", "z-legacy"}) {
		t.Fatalf("Read = %v, %v", names, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("registry mode = %v, %v", info, err)
	}
	if err := WriteEntries(path, original); err != nil {
		t.Fatal(err)
	}
	rewritten, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, rewritten) {
		t.Fatalf("nondeterministic output: %s (%v)", rewritten, err)
	}
}

func TestWritePreservesExplicitPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry")
	if err := WriteEntries(path, []Entry{
		{Name: "keep", Path: "/srv/repositories/keep"},
		{Name: "remove", Path: "/other/remove"},
		{Name: "legacy"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []string{"new", "keep", "legacy", "keep"}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEntries(path)
	want := []Entry{{Name: "keep", Path: "/srv/repositories/keep"}, {Name: "legacy"}, {Name: "new"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadEntries = %#v, %v; want %#v", got, err, want)
	}
	if err := Write(path, []string{"new", "legacy"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "legacy\nnew\n" {
		t.Fatalf("all legacy registry = %q, %v", data, err)
	}
}

func TestReadEntriesRejectsInvalidRegistry(t *testing.T) {
	cases := map[string]string{
		"legacy invalid name":      "good\n../bad\n",
		"legacy duplicate":         "repo\nrepo\n",
		"legacy control":           "repo\x00\n",
		"legacy control padding":   "\trepo\n",
		"legacy carriage return":   "re\rpo\n",
		"invalid utf8":             "repo\xff\n",
		"missing version":          `{"repositories":[]}`,
		"missing repositories":     `{"version":1}`,
		"unsupported version":      `{"version":2,"repositories":[]}`,
		"string version":           `{"version":"1","repositories":[]}`,
		"null version":             `{"version":null,"repositories":[]}`,
		"fractional version":       `{"version":1.0,"repositories":[]}`,
		"unknown top-level field":  `{"version":1,"repositories":[],"extra":true}`,
		"case insensitive field":   `{"Version":1,"repositories":[]}`,
		"duplicate version":        `{"version":1,"version":1,"repositories":[]}`,
		"duplicate repositories":   `{"version":1,"repositories":[],"repositories":[]}`,
		"duplicate escaped key":    `{"version":1,"\u0076ersion":1,"repositories":[]}`,
		"null repositories":        `{"version":1,"repositories":null}`,
		"repositories object":      `{"version":1,"repositories":{}}`,
		"null entry":               `{"version":1,"repositories":[null]}`,
		"string entry":             `{"version":1,"repositories":["repo"]}`,
		"empty entry":              `{"version":1,"repositories":[{}]}`,
		"unknown entry field":      `{"version":1,"repositories":[{"name":"repo","extra":1}]}`,
		"null name":                `{"version":1,"repositories":[{"name":null}]}`,
		"number name":              `{"version":1,"repositories":[{"name":123}]}`,
		"invalid name":             `{"version":1,"repositories":[{"name":"../repo"}]}`,
		"null path":                `{"version":1,"repositories":[{"name":"repo","path":null}]}`,
		"relative path":            `{"version":1,"repositories":[{"name":"repo","path":"repo"}]}`,
		"unclean path":             `{"version":1,"repositories":[{"name":"repo","path":"/srv/../repo"}]}`,
		"control path":             `{"version":1,"repositories":[{"name":"repo","path":"/srv/re\npo"}]}`,
		"nul path":                 `{"version":1,"repositories":[{"name":"repo","path":"/srv/re\u0000po"}]}`,
		"delete control path":      `{"version":1,"repositories":[{"name":"repo","path":"/srv/re\u007fpo"}]}`,
		"duplicate name field":     `{"version":1,"repositories":[{"name":"first","name":"other"}]}`,
		"duplicate path field":     `{"version":1,"repositories":[{"name":"repo","path":"/one","path":"/two"}]}`,
		"duplicate repository":     `{"version":1,"repositories":[{"name":"repo"},{"name":"repo","path":"/other"}]}`,
		"trailing object":          `{"version":1,"repositories":[]} {}`,
		"trailing junk":            `{"version":1,"repositories":[]} junk`,
		"trailing comma":           `{"version":1,"repositories":[],}`,
		"invalid whitespace start": "\v{\"version\":1,\"repositories\":[]}",
		"invalid whitespace end":   "{\"version\":1,\"repositories\":[]}\v",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := registryFixture(t, content)
			if entries, err := ReadEntries(path); err == nil {
				t.Fatalf("ReadEntries accepted invalid registry: %#v", entries)
			}
			if err := Write(path, []string{"new"}); err == nil {
				t.Fatal("Write overwrote invalid registry")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != content {
				t.Fatalf("invalid registry changed: %q, %v", data, err)
			}
		})
	}
}

func TestReadEntriesAcceptsEmptyAndOptionalPath(t *testing.T) {
	for _, content := range []string{"", "\n \n", `{"version":1,"repositories":[]}`, ` {"repositories":[],"version":1} `} {
		got, err := ReadEntries(registryFixture(t, content))
		if err != nil || len(got) != 0 {
			t.Fatalf("empty registry = %#v, %v", got, err)
		}
	}
	got, err := ReadEntries(registryFixture(t, `{"version":1,"repositories":[{"name":"legacy","path":""}]}`))
	if err != nil || !reflect.DeepEqual(got, []Entry{{Name: "legacy"}}) {
		t.Fatalf("empty path = %#v, %v", got, err)
	}
}

func TestWriteEntriesRejectsInvalidEntries(t *testing.T) {
	cases := map[string][]Entry{
		"duplicate names": {{Name: "repo", Path: "/one"}, {Name: "repo", Path: "/two"}},
		"invalid name":    {{Name: "../repo"}},
		"empty name":      {{Path: "/one"}},
		"relative path":   {{Name: "repo", Path: "relative"}},
		"unclean path":    {{Name: "repo", Path: "/a/../b"}},
		"trailing slash":  {{Name: "repo", Path: "/repo/"}},
		"control path":    {{Name: "repo", Path: "/re\tpo"}},
		"unicode control": {{Name: "repo", Path: "/re\u0085po"}},
		"invalid utf8":    {{Name: "repo", Path: "/re\xffpo"}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			path := registryFixture(t, "existing\n")
			if err := WriteEntries(path, entries); err == nil {
				t.Fatal("WriteEntries accepted invalid entries")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "existing\n" {
				t.Fatalf("existing registry changed: %q, %v", data, err)
			}
		})
	}
}

func TestRegistrySizeBound(t *testing.T) {
	exactName := strings.Repeat("a", maxRegistryBytes-1)
	path := registryFixture(t, exactName+"\n")
	if got, err := ReadEntries(path); err != nil || len(got) != 1 {
		t.Fatalf("exact size bound: %d entries, %v", len(got), err)
	}
	if err := WriteEntries(path, []Entry{{Name: exactName}}); err != nil {
		t.Fatalf("write exact size bound: %v", err)
	}
	if err := WriteEntries(path, []Entry{{Name: exactName + "b"}}); err == nil {
		t.Fatal("WriteEntries accepted oversized legacy registry")
	}
	if err := WriteEntries(path, []Entry{{Name: "repo", Path: "/" + exactName}}); err == nil {
		t.Fatal("WriteEntries accepted oversized JSON registry")
	}
	if err := os.WriteFile(path, []byte(exactName+"bc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEntries(path); err == nil {
		t.Fatal("ReadEntries accepted oversized registry")
	}
}

func TestRegistryRejectsSpecialFiles(t *testing.T) {
	base := t.TempDir()
	regular := filepath.Join(base, "regular")
	if err := os.WriteFile(regular, []byte("repo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(base, "broken")
	if err := os.Symlink(filepath.Join(base, "missing"), broken); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(base, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{base, link, broken, fifo} {
		if _, err := ReadEntries(path); err == nil {
			t.Errorf("ReadEntries accepted special file %s", path)
		}
		if err := WriteEntries(path, []Entry{{Name: "new"}}); err == nil {
			t.Errorf("WriteEntries replaced special file %s", path)
		}
	}
	data, err := os.ReadFile(regular)
	if err != nil || string(data) != "repo\n" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
}

func TestReadMissingStateAndBrokenParents(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "new", "state", "registry")
	if got, err := ReadEntries(path); err != nil || len(got) != 0 {
		t.Fatalf("first-run registry = %#v, %v", got, err)
	}
	if err := Write(path, []string{"new"}); err != nil {
		t.Fatalf("first-run Write: %v", err)
	}
	broken := filepath.Join(base, "broken")
	if err := os.Symlink(filepath.Join(base, "absent"), broken); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEntries(filepath.Join(broken, "nested", "registry")); err == nil {
		t.Fatal("ReadEntries treated dangling parent symlink as empty registry")
	}
	if _, err := ReadEntries(filepath.Join(path, "registry")); err == nil {
		t.Fatal("ReadEntries treated non-directory parent as empty registry")
	}
}

func TestReadEntriesPropagatesPermissionErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	base := t.TempDir()
	private := filepath.Join(base, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(private, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(private, 0o700) }()
	if _, err := ReadEntries(filepath.Join(private, "registry")); err == nil {
		t.Fatal("ReadEntries treated inaccessible parent as empty registry")
	}
}
