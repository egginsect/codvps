package cli

import (
	"fmt"
	"os"

	"github.com/egginsect/codvps/internal/repo"
)

// newRepoManager wires the production RepoManager over the shared head
// environment (see newHeadEnv), so repo add/remove attach and detach
// repositories through every provider's head.
func newRepoManager() (*repo.RepoManager, error) {
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

// repoAdd handles 'repo add' command
func repoAdd(url string) error {
	manager, err := newRepoManager()
	if err != nil {
		return err
	}
	if err := manager.Add(url); err != nil {
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
