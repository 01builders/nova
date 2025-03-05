package nova

import (
	"github.com/01builders/nova/appd"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/01builders/nova/abci"
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
			expectedErrStr: "[version height] is required",
		},
		{
			name:           "no version specified",
			args:           []string{"--version"},
			versions:       make(abci.Versions),
			expectedErrStr: "flag needs an argument: --version",
		},
		{
			name:           "empty version specified",
			args:           []string{"--version", "", "query", "account"},
			versions:       make(abci.Versions),
			expectedErrStr: "either --version or --height must be specified",
		},
		{
			name:           "empty version specified with spaces",
			args:           []string{"--version", "   ", "query", "account"},
			versions:       make(abci.Versions),
			expectedErrStr: "either --version or --height must be specified",
		},
		{
			name:           "no height specified",
			args:           []string{"--height"},
			versions:       make(abci.Versions),
			expectedErrStr: "flag needs an argument: --height",
		},
		{
			name:           "cannot specify both height and version",
			args:           []string{"--height", "0", "--version", "1"},
			versions:       make(abci.Versions),
			expectedErrStr: "if any flags in the group [version height] are set none of the others can be; [height version] were all set",
		},
		{
			name:           "cannot passthrough start command",
			args:           []string{"start", "--height", "0"},
			versions:       make(abci.Versions),
			expectedErrStr: "cannot passthrough start command",
		},
		{
			name:           "invalid height value",
			args:           []string{"--height", "abc"},
			versions:       make(abci.Versions),
			expectedErrStr: `invalid argument "abc" for "--height`,
		},
		{
			name: "should not use latest version with lower height",
			args: []string{"--height", "100", "query", "account"},
			versions: abci.Versions{
				"v1": newVersion(50, &appd.Appd{}),
			},
			expectedErrStr: "height 100 requires the latest app",
		},
		{
			name: "underlying appd is nil",
			args: []string{"--height", "50", "query", "account"},
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
