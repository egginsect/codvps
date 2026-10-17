package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/settings"
)

// newRepoManager wires the production RepoManager over the shared head
// environment (see newHeadEnv), so repo add/remove attach and detach
// repositories through every provider's head.
func newRepoManager() (*repo.RepoManager, error) {
	layout, err := paths.New("")
	if err != nil {
		return nil, err
	}
	cfg, err := settings.Read(layout)
	if err != nil {
		return nil, err
	}
	if cfg.SessionMode == "manual" {
		env, err := settings.Environment(layout)
		if err != nil {
			return nil, err
		}
		return repo.NewRepoManager(layout, runner.NewExecRunner(append(env, "GIT_TERMINAL_PROMPT=0")...), nil), nil
	}
	env, err := newHeadEnv()
	if err != nil {
		return nil, err
	}
	return env.repoManager(), nil
}

// repoManager drives repository add/list/remove through every
// implemented provider's head, in provider order. A head marked
// RepoSelectedOnly is included only when its provider is selected.
func (env *headEnv) repoManager() *repo.RepoManager {
	var heads []repo.Head
	for _, p := range env.providers {
		if p.Planned != "" || p.Head == nil || p.Head.Repo == nil {
			continue
		}
		if p.Head.RepoSelectedOnly && !env.components.IsSelected(p.Name) {
			continue
		}
		heads = append(heads, p.Head.Repo(env.heads, env.layout, env.runner))
	}
	return repo.NewRepoManager(env.layout, env.git, heads)
}

// repoAddAt handles the repo add command.
func repoAddAt(url, dest string) error {
	manager, err := newRepoManager()
	if err != nil {
		return err
	}
	if err := manager.AddAt(url, dest); err != nil {
		return err
	}
	fmt.Printf("Repository added: %s\n", url)
	return nil
}

// repoList handles 'repo list' command
func repoList() error {
	manager, err := newRepoManager()
	if err != nil {
		return err
	}
	return manager.RenderList(os.Stdout)
}

// repoRemove handles 'repo remove' command
func repoRemove(name string) error {
	manager, err := newRepoManager()
	if err != nil {
		return err
	}
	result, err := manager.Remove(name)
	if err != nil {
		return err
	}
	fmt.Printf("Repository removed from registry: %s\n", name)
	for _, notice := range result.Notices {
		fmt.Println(notice)
	}
	return nil
}

// parseRepoAdd keeps destination parsing separate from provider/systemd setup.
func parseRepoAdd(args []string) (string, string, error) {
	usage := "codvps repo add <url> [--dest <absolute-path>]"
	if len(args) == 0 {
		return "", "", invalidUsage(usage)
	}
	if strings.HasPrefix(args[0], "-") {
		return "", "", fmt.Errorf("unknown repo add option: %s", args[0])
	}
	if len(args) == 1 {
		return args[0], "", nil
	}
	if len(args) != 3 || args[1] != "--dest" {
		return "", "", invalidUsage(usage)
	}
	if !filepath.IsAbs(args[2]) || filepath.Clean(args[2]) != args[2] || args[2] == string(filepath.Separator) || !utf8.ValidString(args[2]) || strings.IndexFunc(args[2], unicode.IsControl) >= 0 {
		return "", "", fmt.Errorf("--dest must be a clean absolute checkout path")
	}
	return args[0], args[2], nil
}
