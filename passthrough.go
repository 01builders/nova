package nova

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/01builders/nova/abci"
)

const (
	flagVersion = "version"
	flagHeight  = "height"
)

// NewPassthroughCmd creates a command that allows executing commands on any app version.
// This enables direct interaction with older app versions for debugging or older queries.
func NewPassthroughCmd(versions abci.Versions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "passthrough [command]",
		Short: "Execute a command on a specific app version",
		Long: `Execute a command on a specific app version.
This allows interacting with older app versions for debugging or older queries.
Use --version to specify a named version or --height to execute on the version active at a specific height.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("no command specified to pass through")
			}

			if strings.EqualFold("start", args[0]) {
				return errors.New("cannot passthrough start command")
			}

			// Get the version name or determine from height
			versionName := cmd.Flags().Lookup(flagVersion).Value.String()
			heightStr := cmd.Flags().Lookup(flagHeight).Value.String()

			var appVersion abci.Version
			var found bool

			// Determine which version to use based on flags
			if versionName != "" {
				// Get by name
				appVersion, found = versions[versionName]
				if !found {
					return fmt.Errorf("version %s not found", versionName)
				}
			} else if heightStr != "" {
				// Get by height
				height, err := strconv.ParseInt(heightStr, 10, 64)
				if err != nil {
					return fmt.Errorf("invalid height %s: %w", heightStr, err)
				}

				if versions.ShouldLatestApp(height) {
					return fmt.Errorf("height %d requires the latest app, use the command directly without passthrough", height)
				}

				appVersion, err = versions.GetForHeight(height)
				if err != nil {
					return fmt.Errorf("no version found for height %d: %w", height, err)
				}
			} else {
				return errors.New("either --version or --height must be specified")
			}

			// ensure we have a valid appd instance
			if appVersion.Appd == nil {
				return fmt.Errorf("no binary available for version %s", versionName)
			}

			// prepare the command to be executed
			execCmd := appVersion.Appd.CreateExecCommand(args...)
			return execCmd.Run()
		},
	}

	cmd.Flags().String(flagVersion, "", "App version name to execute the command on")
	cmd.Flags().Int64(flagHeight, 0, "Blockchain height to determine which app version to use")
	cmd.MarkFlagsMutuallyExclusive(flagVersion, flagHeight)
	cmd.MarkFlagsOneRequired(flagVersion, flagHeight)

	return cmd
}
