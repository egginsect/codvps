package cli

import (
	"fmt"

	"github.com/egginsect/codvps/internal/components"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/runner"
)

// catalog is the component registry's view of every provider.
func catalog() components.Catalog {
	return components.Catalog{Providers: providers.All()}
}

// CmdComponentsList handles 'codvps components list'.
func CmdComponentsList() error {
	layout, err := paths.New("")
	if err != nil {
		return err
	}
	return catalog().List(layout)
}

// CmdComponentsStatus handles 'codvps components status'.
func CmdComponentsStatus() error {
	layout, err := paths.New("")
	if err != nil {
		return err
	}
	return catalog().Status(layout, runner.NewExecRunner())
}

// CmdComponentsUsage handles 'codvps components help|--help|-h'.
func CmdComponentsUsage() error {
	fmt.Print(catalog().Usage())
	return nil
}
