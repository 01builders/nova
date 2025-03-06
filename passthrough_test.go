package nova

import (
	"bytes"
	"testing"

	"github.com/01builders/nova/abci"
	"github.com/01builders/nova/appd"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestNewPassthroughCmd(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		versions       abci.Versions
		expectedErrStr string
		expectedOutput string
	}{
		{
			name:           "required arguments not specified",
			args:           []string{},
			versions:       []abci.Version{},
			expectedErrStr: "requires at least 1 arg(s), only received 0",
		},
		{
			name:           "cannot passthrough start command with version",
			args:           []string{"v4", "start"},
			versions:       []abci.Version{},
			expectedErrStr: "cannot passthrough start command",
		},
		{
			name:           "cannot passthrough start command with height",
			args:           []string{"100", "start"},
			versions:       []abci.Version{},
			expectedErrStr: "cannot passthrough start command",
		},
		{
			name:           "version not found no versions",
			args:           []string{"v1"},
			versions:       []abci.Version{},
			expectedErrStr: "version v1 not found",
		},
		{
			name: "version not found existing versions",
			args: []string{"v1"},
			versions: abci.Versions{
				newVersion("v2", 50, &appd.Appd{}),
			},
			expectedErrStr: "version v1 not found",
		},
		{
			name: "should not use latest version with lower height",
			args: []string{"100", "query", "account"},
			versions: abci.Versions{
				newVersion("v1", 50, &appd.Appd{}),
			},
			expectedErrStr: "height 100 requires the latest app",
		},
		{
			name: "underlying appd is nil",
			args: []string{"50", "query", "account"},
			versions: abci.Versions{
				newVersion("v1", 50, nil),
			},
			expectedErrStr: "no binary available for version",
		},
		// TODO: add tests which call into an underlying app.
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			
			cmd, _ := NewPassthroughCmd(tt.versions)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			output, err := executeCommand(cmd, tt.args...)

			if tt.expectedErrStr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.expectedErrStr)
			} else {
				require.NoError(t, err)
				require.Contains(t, output, tt.expectedOutput)
			}
		})
	}
}

// newVersion creates a new abci.Version with a name, untilHeight, and an optional appd instance.
func newVersion(name string, untilHeight int64, app *appd.Appd) abci.Version {
	return abci.Version{
		Name:        name,
		UntilHeight: untilHeight,
		Appd:        app,
	}
}

// executeCommand executes the cobra command with the specified args.
func executeCommand(cmd *cobra.Command, args ...string) (string, error) {
	cmd.SetArgs(args)
	output, err := cmd.ExecuteC()
	return output.Name(), err
}
