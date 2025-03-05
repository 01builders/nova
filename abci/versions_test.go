package abci

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenesisVersion(t *testing.T) {
	tests := []struct {
		name        string
		versions    Versions
		expectedErr error
		expectedVer Version
	}{
		{
			name:        "No versions available",
			versions:    Versions{},
			expectedErr: ErrNoVersionFound,
		},
		{
			name: "Single genesis version",
			versions: Versions{
				"v1": {UntilHeight: 100},
			},
			expectedVer: Version{UntilHeight: 100},
		},
		{
			name: "Find lowest UntilHeight",
			versions: Versions{
				"v1": {UntilHeight: 100},
				"v2": {UntilHeight: 50}, // Genesis version
				"v3": {UntilHeight: 150},
			},
			expectedVer: Version{UntilHeight: 50},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version, err := tt.versions.GenesisVersion()

			if tt.expectedErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.expectedErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedVer, version)
			}
		})
	}
}

func TestGetForHeight(t *testing.T) {
	tests := []struct {
		name        string
		versions    Versions
		height      int64
		expectedErr error
		expectedVer Version
	}{
		{
			name:        "No versions available",
			versions:    Versions{},
			height:      100,
			expectedErr: ErrNoVersionFound,
		},
		{
			name: "exact match for height",
			versions: Versions{
				"v1": {UntilHeight: 100},
			},
			height:      100,
			expectedVer: Version{UntilHeight: 100},
		},
		{
			name: "find closest matching version",
			versions: Versions{
				"v1": {UntilHeight: 50},
				"v2": {UntilHeight: 100},
				"v3": {UntilHeight: 200},
			},
			height:      75,
			expectedVer: Version{UntilHeight: 100}, // Closest greater match
		},
		{
			name: "No matching version for height",
			versions: Versions{
				"v1": {UntilHeight: 50},
				"v2": {UntilHeight: 100},
			},
			height:      150,
			expectedErr: ErrNoVersionFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version, err := tt.versions.GetForHeight(tt.height)

			if tt.expectedErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.expectedErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedVer, version)
			}
		})
	}
}

func TestShouldLatestApp(t *testing.T) {
	tests := []struct {
		name     string
		versions Versions
		height   int64
		expected bool
	}{
		{
			name:     "No versions available",
			versions: Versions{},
			height:   100,
			expected: true,
		},
		{
			name: "Height lower than all versions",
			versions: Versions{
				"v1": {UntilHeight: 100},
				"v2": {UntilHeight: 200},
			},
			height:   50,
			expected: false,
		},
		{
			name: "Height matches a version",
			versions: Versions{
				"v1": {UntilHeight: 100},
				"v2": {UntilHeight: 200},
			},
			height:   100,
			expected: false,
		},
		{
			name: "Height exceeds all versions",
			versions: Versions{
				"v1": {UntilHeight: 100},
				"v2": {UntilHeight: 200},
			},
			height:   250,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.versions.ShouldLatestApp(tt.height)
			assert.Equal(t, tt.expected, result)
		})
	}
}
