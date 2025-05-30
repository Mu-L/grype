package cli

import (
	"errors"
	"os"
	"runtime/debug"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"

	"github.com/anchore/clio"
	"github.com/anchore/grype/cmd/grype/cli/commands"
	grypeHandler "github.com/anchore/grype/cmd/grype/cli/ui"
	"github.com/anchore/grype/cmd/grype/internal/ui"
	v6 "github.com/anchore/grype/grype/db/v6"
	"github.com/anchore/grype/grype/grypeerr"
	"github.com/anchore/grype/internal/bus"
	"github.com/anchore/grype/internal/log"
	"github.com/anchore/grype/internal/redact"
	"github.com/anchore/stereoscope"
	syftHandler "github.com/anchore/syft/cmd/syft/cli/ui"
	"github.com/anchore/syft/syft"
)

func Application(id clio.Identification) clio.Application {
	app, _ := create(id)
	return app
}

func Command(id clio.Identification) *cobra.Command {
	_, cmd := create(id)
	return cmd
}

func SetupConfig(id clio.Identification) *clio.SetupConfig {
	return clio.NewSetupConfig(id).
		WithGlobalConfigFlag().   // add persistent -c <path> for reading an application config from
		WithGlobalLoggingFlags(). // add persistent -v and -q flags tied to the logging config
		WithConfigInRootHelp().   // --help on the root command renders the full application config in the help text
		WithUIConstructor(
			// select a UI based on the logging configuration and state of stdin (if stdin is a tty)
			func(cfg clio.Config) (*clio.UICollection, error) {
				// remove CI var from consideration when determining if we should use the UI
				lipgloss.SetDefaultRenderer(lipgloss.NewRenderer(os.Stdout, termenv.WithEnvironment(environWithoutCI{})))

				// setup the UIs
				noUI := ui.None(cfg.Log.Quiet)
				if !cfg.Log.AllowUI(os.Stdin) || cfg.Log.Quiet {
					return clio.NewUICollection(noUI), nil
				}

				return clio.NewUICollection(
					ui.New(cfg.Log.Quiet,
						grypeHandler.New(grypeHandler.DefaultHandlerConfig()),
						syftHandler.New(syftHandler.DefaultHandlerConfig()),
					),
					noUI,
				), nil
			},
		).
		WithInitializers(
			func(state *clio.State) error {
				// clio is setting up and providing the bus, redact store, and logger to the application. Once loaded,
				// we can hoist them into the internal packages for global use.
				stereoscope.SetBus(state.Bus)
				syft.SetBus(state.Bus)
				bus.Set(state.Bus)

				redact.Set(state.RedactStore)

				log.Set(state.Logger)
				syft.SetLogger(state.Logger)
				stereoscope.SetLogger(state.Logger)

				return nil
			},
		).
		WithPostRuns(func(_ *clio.State, _ error) {
			stereoscope.Cleanup()
		}).
		WithMapExitCode(func(err error) int {
			// return exit code 2 to indicate when a vulnerability severity is discovered
			// that is equal or above the given --fail-on severity value.
			if errors.Is(err, grypeerr.ErrAboveSeverityThreshold) {
				return 2
			}
			// return exit code 100 to indicate a DB upgrade is available (cmd: db check).
			if errors.Is(err, grypeerr.ErrDBUpgradeAvailable) {
				return 100
			}
			return 1
		})
}

func create(id clio.Identification) (clio.Application, *cobra.Command) {
	clioCfg := SetupConfig(id)

	app := clio.New(*clioCfg)

	rootCmd := commands.Root(app)

	// add sub-commands
	rootCmd.AddCommand(
		commands.DB(app),
		commands.Completion(app),
		commands.Explain(app),
		clio.VersionCommand(id, syftVersion, dbVersion),
		clio.ConfigCommand(app, nil),
	)

	return app, rootCmd
}

func syftVersion() (string, any) {
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		log.Debug("unable to find the buildinfo section of the binary (syft version is unknown)")
		return "", ""
	}

	for _, d := range buildInfo.Deps {
		if d.Path == "github.com/anchore/syft" {
			return "Syft Version", d.Version
		}
	}

	log.Debug("unable to find 'github.com/anchore/syft' from the buildinfo section of the binary")
	return "", ""
}

func dbVersion() (string, any) {
	return "Supported DB Schema", v6.ModelVersion
}

type environWithoutCI struct {
}

func (e environWithoutCI) Environ() []string {
	var out []string
	for _, s := range os.Environ() {
		if strings.HasPrefix(s, "CI=") {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (e environWithoutCI) Getenv(s string) string {
	if s == "CI" {
		return ""
	}
	return os.Getenv(s)
}
