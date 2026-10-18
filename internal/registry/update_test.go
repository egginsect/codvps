package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestUpdateEntriesSerializesConcurrentAdds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "registry")
	const workers = 32
	start := make(chan struct{})
	done := make(chan error, workers)
	var active atomic.Int32
	for i := 0; i < workers; i++ {
		go func(i int) {
			<-start
			done <- UpdateEntries(path, func(entries []Entry) ([]Entry, error) {
				if active.Add(1) != 1 {
					active.Add(-1)
					return nil, fmt.Errorf("callbacks ran concurrently")
				}
				defer active.Add(-1)
				// Give the other ready writers a chance to contend while
				// this callback still owns the read-modify-write transaction.
				runtime.Gosched()
				return append(entries, Entry{
					Name: fmt.Sprintf("repo-%02d", i),
					Path: fmt.Sprintf("/work/repo-%02d", i),
				}), nil
			})
		}(i)
	}
	close(start)
	for i := 0; i < workers; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]Entry, workers)
	for i := range want {
		want[i] = Entry{Name: fmt.Sprintf("repo-%02d", i), Path: fmt.Sprintf("/work/repo-%02d", i)}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("concurrent adds lost entries: got %#v, want %#v", got, want)
	}
}

func TestUpdateEntriesHoldsFlockDuringCallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry")
	err := UpdateEntries(path, func(entries []Entry) ([]Entry, error) {
		other, err := os.OpenFile(path+".lock", os.O_RDWR, 0)
		if err != nil {
			return nil, err
		}
		defer func() { _ = other.Close() }()
		err = syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			_ = syscall.Flock(int(other.Fd()), syscall.LOCK_UN)
			return nil, fmt.Errorf("callback does not hold the exclusive flock")
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("unexpected flock result: %w", err)
		}
		return append(entries, Entry{Name: "repo"}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegistryWritersShareLock(t *testing.T) {
	for _, writer := range []struct {
		name string
		run  func(string) error
		want []Entry
	}{
		{
			name: "Write reads paths after acquiring lock",
			run:  func(path string) error { return Write(path, []string{"keep", "new"}) },
			want: []Entry{{Name: "keep", Path: "/updated/keep"}, {Name: "new"}},
		},
		{
			name: "WriteEntries replaces only after acquiring lock",
			run:  func(path string) error { return WriteEntries(path, []Entry{{Name: "replacement"}}) },
			want: []Entry{{Name: "replacement"}},
		},
	} {
		t.Run(writer.name, func(t *testing.T) {
			path := registryFixture(t, "keep\n")
			holding := make(chan struct{})
			release := make(chan struct{})
			updateDone := make(chan error, 1)
			go func() {
				updateDone <- UpdateEntries(path, func(entries []Entry) ([]Entry, error) {
					close(holding)
					<-release
					entries[0].Path = "/updated/keep"
					return entries, nil
				})
			}()
			<-holding
			writerDone := make(chan error, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				writerDone <- writer.run(path)
			}()
			<-started
			select {
			case err := <-writerDone:
				close(release)
				<-updateDone
				t.Fatalf("writer completed while another transaction held the lock: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			close(release)
			if err := <-updateDone; err != nil {
				t.Fatal(err)
			}
			if err := <-writerDone; err != nil {
				t.Fatal(err)
			}
			got, err := ReadEntries(path)
			if err != nil || !reflect.DeepEqual(got, writer.want) {
				t.Fatalf("got %#v, %v; want %#v", got, err, writer.want)
			}
		})
	}
}

func TestUpdateEntriesNoOpEstablishesStableLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "registry")
	identity := func(entries []Entry) ([]Entry, error) { return entries, nil }
	if err := UpdateEntries(path, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("no-op created registry: %v", err)
	}
	before, err := os.Stat(path + ".lock")
	if err != nil || before.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode: %v, %v", before, err)
	}
	if err := UpdateEntries(path, func([]Entry) ([]Entry, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("nil/empty no-op created registry: %v", err)
	}
	if err := WriteEntries(path, []Entry{{Name: "repo", Path: "/work/repo"}}); err != nil {
		t.Fatal(err)
	}
	regBefore, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateEntries(path, identity); err != nil {
		t.Fatal(err)
	}
	regAfter, err := os.Stat(path)
	if err != nil || !os.SameFile(regBefore, regAfter) || regBefore.ModTime() != regAfter.ModTime() {
		t.Fatalf("no-op replaced registry: %v, %v", regAfter, err)
	}
	after, err := os.Stat(path + ".lock")
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("registry lock inode changed: %v, %v", after, err)
	}
}

func TestUpdateEntriesInPlaceMutationAndCallbackError(t *testing.T) {
	path := registryFixture(t, "repo\n")
	failure := errors.New("callback failed")
	err := UpdateEntries(path, func(entries []Entry) ([]Entry, error) {
		entries[0].Path = "/not-written"
		return entries, failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("callback error = %v, want %v", err, failure)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "repo\n" {
		t.Fatalf("callback error changed registry: %q, %v", data, err)
	}
	if err := UpdateEntries(path, func(entries []Entry) ([]Entry, error) {
		entries[0].Path = "/written"
		return entries, nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEntries(path)
	if err != nil || !reflect.DeepEqual(got, []Entry{{Name: "repo", Path: "/written"}}) {
		t.Fatalf("in-place update = %#v, %v", got, err)
	}
}

func TestRegistryRejectsUnsafeLocks(t *testing.T) {
	cases := map[string]func(string) error{
		"symlink": func(path string) error {
			return os.Symlink(filepath.Join(filepath.Dir(path), "registry"), path)
		},
		"dangling symlink": func(path string) error { return os.Symlink(path+"-missing", path) },
		"directory":        func(path string) error { return os.Mkdir(path, 0o700) },
		"fifo":             func(path string) error { return syscall.Mkfifo(path, 0o600) },
		"world readable":   func(path string) error { return os.WriteFile(path, nil, 0o644) },
		"owner readonly":   func(path string) error { return os.WriteFile(path, nil, 0o400) },
		"hardlink": func(path string) error {
			return os.Link(filepath.Join(filepath.Dir(path), "registry"), path)
		},
	}
	for name, create := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "registry")
			if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := create(path + ".lock"); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path + ".lock")
			if err != nil {
				t.Fatal(err)
			}
			called := false
			if err := UpdateEntries(path, func(entries []Entry) ([]Entry, error) {
				called = true
				return append(entries, Entry{Name: "new"}), nil
			}); err == nil || called {
				t.Fatalf("unsafe lock permitted callback: err=%v, called=%v", err, called)
			}
			if err := Write(path, []string{"new"}); err == nil {
				t.Fatal("Write accepted unsafe lock")
			}
			if err := WriteEntries(path, []Entry{{Name: "new"}}); err == nil {
				t.Fatal("WriteEntries accepted unsafe lock")
			}
			after, err := os.Lstat(path + ".lock")
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatalf("unsafe lock modified: %v, %v", after, err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "original\n" {
				t.Fatalf("unsafe lock changed registry: %q, %v", data, err)
			}
		})
	}
}

func TestRegistryLockRejectsForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing file ownership requires root")
	}
	path := registryFixture(t, "original\n")
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path+".lock", 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if err := WriteEntries(path, []Entry{{Name: "new"}}); err == nil {
		t.Fatal("accepted lock owned by another user")
	}
}

func TestRegistryNoOpInNonWritableParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	base := t.TempDir()
	path := filepath.Join(base, "registry")
	if err := WriteEntries(path, []Entry{{Name: "repo"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(base, 0o700) }()
	identity := func(entries []Entry) ([]Entry, error) { return entries, nil }
	if err := UpdateEntries(path, identity); err != nil {
		t.Fatalf("existing lock/no-op in non-writable parent: %v", err)
	}
	if err := UpdateEntries(path, func(entries []Entry) ([]Entry, error) {
		return append(entries, Entry{Name: "new"}), nil
	}); err == nil {
		t.Fatal("update in non-writable parent unexpectedly succeeded")
	}
	if err := UpdateEntries(path, identity); err != nil {
		t.Fatalf("no-op inspection after failed write: %v", err)
	}
}

func TestRegistryLockDetectsReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.lock")
	old, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	if err := os.WriteFile(path+"-new", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+"-new", path); err != nil {
		t.Fatal(err)
	}
	if err := validateLockFile(path, old); err == nil {
		t.Fatal("accepted replaced lock inode")
	}
}

func TestExplicitRegistryWritesCreateEmptyFile(t *testing.T) {
	for _, writer := range []struct {
		name string
		run  func(string) error
	}{
		{"Write", func(path string) error { return Write(path, nil) }},
		{"WriteEntries", func(path string) error { return WriteEntries(path, nil) }},
	} {
		t.Run(writer.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "new", "registry")
			if err := writer.run(path); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("explicit empty write did not create registry: %v", err)
			}
			if info.Size() != 0 || info.Mode().Perm() != 0o600 {
				t.Fatalf("empty registry size=%d, mode=%o", info.Size(), info.Mode().Perm())
			}
		})
	}
}

func TestExplicitRegistryWritesNormalizeMode(t *testing.T) {
	entries := []Entry{{Name: "repo", Path: "/work/repo"}}
	for _, writer := range []struct {
		name string
		run  func(string) error
	}{
		{"Write", func(path string) error { return Write(path, []string{"repo"}) }},
		{"WriteEntries", func(path string) error { return WriteEntries(path, entries) }},
	} {
		t.Run(writer.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "registry")
			if err := WriteEntries(path, entries); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := writer.run(path); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("unchanged explicit write retained mode %o", info.Mode().Perm())
			}
			got, err := ReadEntries(path)
			if err != nil || !reflect.DeepEqual(got, entries) {
				t.Fatalf("explicit write changed entries: %#v, %v", got, err)
			}
		})
	}
}
