package cli

import (
	"fmt"
	"os"

	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/settings"
)

const settingsUsage = "codvps config <get KEY|set KEY VALUE|unset KEY|link REPO-URL>"

func configSetting(args []string) error {
	if len(args) < 2 {
		return invalidUsage(settingsUsage)
	}
	if (args[0] == "set" && len(args) != 3) || (args[0] != "set" && len(args) != 2) {
		return invalidUsage(settingsUsage)
	}
	layout, err := paths.New("")
	if err != nil {
		return err
	}
	cfg, err := settings.Read(layout)
	if err != nil {
		return err
	}
	var value *string
	switch args[1] {
	case "repo_root":
		value = &cfg.RepoRoot
	case "github_config_dir":
		value = &cfg.GitHubConfigDir
	case "git_config_global":
		value = &cfg.GitConfigGlobal
	case "session_mode":
		value = &cfg.SessionMode
	default:
		return fmt.Errorf("unknown setting %q (known: repo_root, github_config_dir, git_config_global, session_mode)", args[1])
	}
	switch args[0] {
	case "get":
		result := *value
		if args[1] == "repo_root" && result == "" {
			result = layout.Home()
		}
		if args[1] == "session_mode" && result == "" {
			result = "systemd"
		}
		_, err = fmt.Fprintln(os.Stdout, result)
		return err
	case "set":
		*value = args[2]
	case "unset":
		*value = ""
	default:
		return invalidUsage(settingsUsage)
	}
	if err := settings.Write(layout, cfg); err != nil {
		return err
	}
	fmt.Printf("Updated %s. Existing checkouts and running sessions are unchanged.\n", args[1])
	return nil
}
