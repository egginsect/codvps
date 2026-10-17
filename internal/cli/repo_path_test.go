package cli

import "testing"

func TestParseRepoAddDestination(t *testing.T) {
	for _, tc := range []struct {
		args      []string
		url, dest string
		bad       bool
	}{
		{args: []string{"https://github.com/a/repo.git"}, url: "https://github.com/a/repo.git"},
		{args: []string{"https://github.com/a/repo.git", "--dest", "/workspace/shared/repo with spaces"}, url: "https://github.com/a/repo.git", dest: "/workspace/shared/repo with spaces"},
		{args: nil, bad: true},
		{args: []string{"repo", "--dest"}, bad: true},
		{args: []string{"repo", "--dest", "relative"}, bad: true},
		{args: []string{"repo", "--dest", "/tmp/with\nnewline"}, bad: true},
		{args: []string{"repo", "--dest", "/"}, bad: true},
		{args: []string{"repo", "--dest", "/tmp/a/../b"}, bad: true},
		{args: []string{"repo", "--bogus", "/tmp/repo"}, bad: true},
		{args: []string{"repo", "--dest", "/tmp/repo", "--dest", "/tmp/other"}, bad: true},
	} {
		u, d, err := parseRepoAdd(tc.args)
		if (err != nil) != tc.bad || u != tc.url || d != tc.dest {
			t.Errorf("parseRepoAdd(%q)=%q,%q,%v", tc.args, u, d, err)
		}
	}
}
