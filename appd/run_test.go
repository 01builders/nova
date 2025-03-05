package appd

import (
	"bytes"
	"github.com/stretchr/testify/require"
	"testing"
)

// TestCreateExecCommand execs a command to an embedded binary.
func TestCreateExecCommand(t *testing.T) {
	appdInstance, err := New("test-app", nil)
	require.NoError(t, err)
	require.NotNil(t, appdInstance)

	cmd := appdInstance.CreateExecCommand("version")
	require.NotNil(t, cmd)

	var outputBuffer bytes.Buffer
	cmd.Stdout = &outputBuffer

	require.NoError(t, cmd.Run())

	output := string(outputBuffer.Bytes())
	require.NotEmpty(t, output)
}
