package nova

import (
	"bytes"
	"github.com/01builders/nova/appd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"

	"github.com/01builders/nova/abci"
	"github.com/spf13/cobra"
)

func TestNewPassthroughCmd(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		versions       abci.Versions
		mockVersions   abci.Versions
		expectedErrStr string
		expectedOutput string
	}{
		{
			name:           "required arguments not specified",
			args:           []string{},
			versions:       make(abci.Versions),
			expectedErrStr: "requires at least 1 arg(s), only received 0",
		},
		{
			name:           "cannot passthrough start command with version",
			args:           []string{"v4", "start"},
			versions:       make(abci.Versions),
			expectedErrStr: "cannot passthrough start command",
		},
		{
			name:           "cannot passthrough start command with height",
			args:           []string{"100", "start"},
			versions:       make(abci.Versions),
			expectedErrStr: "cannot passthrough start command",
		},
		{
			name:           "version not found no versions",
			args:           []string{"v1"},
			versions:       make(abci.Versions),
			expectedErrStr: "version v1 not found",
		},
		{
			name: "version not found existing versions",
			args: []string{"v1"},
			versions: abci.Versions{
				"v2": newVersion(50, &appd.Appd{}),
			},
			expectedErrStr: "version v1 not found",
		},
		{
			name: "should not use latest version with lower height",
			args: []string{"100", "query", "account"},
			versions: abci.Versions{
				"v1": newVersion(50, &appd.Appd{}),
			},
			expectedErrStr: "height 100 requires the latest app",
		},
		{
			name: "underlying appd is nil",
			args: []string{"50", "query", "account"},
			versions: abci.Versions{
				"v1": newVersion(50, nil),
			},
			expectedErrStr: "no binary available for version",
		},
		// TODO: add tests which call into an underlying app.
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			cmd := NewPassthroughCmd(tt.versions)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			output, err := executeCommand(cmd, tt.args...)

			if tt.expectedErrStr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErrStr)
			} else {
				require.NoError(t, err)
				assert.Contains(t, output, tt.expectedOutput)
			}
		})
	}
}

// newVersion returns a new instance of abci.Version with the provided untilHeight and app.
func newVersion(untilHeight int64, app *appd.Appd) abci.Version {
	return abci.Version{
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
