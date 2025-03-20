package nova

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/01builders/nova/abci"
)

// NewPassthroughCmd creates a command that allows executing commands on any app version.
// This enables direct interaction with older app versions for debugging or older queries.
func NewPassthroughCmd(versions abci.Versions) (*cobra.Command, error) {
	if err := versions.Validate(); err != nil {
		return nil, fmt.Errorf("invalid versions: %w", err)
	}

	cmd := &cobra.Command{
		Use:                "passthrough [version/height] [command]",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		Short:              "Execute a command on a specific app version",
		Long: `Execute a command on a specific app version.
This allows interacting with older app versions for debugging or older queries.
Use a version name to specify a named version or a height to execute on the version active at a specific height as first argument.`,
		Example: `passthrough v3 status`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) >= 2 && strings.EqualFold("start", args[1]) {
				return errors.New("cannot passthrough start command")
			}

			// Get the version name or determine from height
			versionName := args[0]
			height, errParseHeight := strconv.ParseInt(versionName, 10, 64)

			var (
				appVersion abci.Version
				err        error
			)

			// Determine which version to use based on flags
			if versionName != "" && height == 0 {
				// Get by name
				appVersion, err = versions.GetForName(versionName)
				if err != nil {
					return fmt.Errorf("version %s not found: %w", versionName, err)
				}
			} else if errParseHeight == nil { // Get by height
				if versions.ShouldUseLatestApp(height, 0) {
					return fmt.Errorf("height %d requires the latest app, use the command directly without passthrough", height)
				}

				appVersion, err = versions.GetForHeight(height)
				if err != nil {
					return fmt.Errorf("no version found for height %d: %w", height, err)
				}
			}

			// ensure we have a valid appd instance
			if appVersion.Appd == nil {
				return fmt.Errorf("no binary available for version %s", versionName)
			}

			// prepare the command to be executed
			execCmd := appVersion.Appd.CreateExecCommand(args[1:]...)
			return execCmd.Run()
		},
	}

	return cmd, nil
}
