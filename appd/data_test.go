package appd

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCelestiaApp(t *testing.T) {
	data, err := CelestiaApp()
	require.NoError(t, err, "CelestiaApp should not return an error")
	require.NotNil(t, data, "CelestiaApp should return non-nil data")
	assert.NotEmpty(t, data, "CelestiaApp should return non-empty binary data")
}
