package abci

import (
	"errors"
	"testing"

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
				{Name: "v1", UntilHeight: 100},
			},
			expectedVer: Version{Name: "v1", UntilHeight: 100},
		},
		{
			name: "Find lowest UntilHeight",
			versions: Versions{
				{Name: "v1", UntilHeight: 100},
				{Name: "v2", UntilHeight: 50}, // Genesis version
				{Name: "v3", UntilHeight: 150},
			},
			expectedVer: Version{Name: "v2", UntilHeight: 50},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version, err := tt.versions.GenesisVersion()

			if tt.expectedErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tt.expectedErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectedVer, version)
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
			name: "Exact match for height",
			versions: Versions{
				{Name: "v1", UntilHeight: 100},
			},
			height:      100,
			expectedVer: Version{Name: "v1", UntilHeight: 100},
		},
		{
			name: "Find closest matching version",
			versions: Versions{
				{Name: "v1", UntilHeight: 50},
				{Name: "v2", UntilHeight: 100},
				{Name: "v3", UntilHeight: 200},
			},
			height:      75,
			expectedVer: Version{Name: "v2", UntilHeight: 100}, // Closest greater match
		},
		{
			name: "Find closest matching version using sorting",
			versions: Versions{
				{Name: "v1", UntilHeight: 50},
				{Name: "v3", UntilHeight: 200},
				{Name: "v2", UntilHeight: 100},
			}.Sorted(),
			height:      75,
			expectedVer: Version{Name: "v2", UntilHeight: 100}, // Closest greater match
		},
		{
			name: "No matching version for height",
			versions: Versions{
				{Name: "v1", UntilHeight: 50},
				{Name: "v2", UntilHeight: 100},
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
				require.ErrorIs(t, err, tt.expectedErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectedVer, version)
			}
		})
	}
}

func TestShouldUseLatestApp(t *testing.T) {
	tests := []struct {
		name       string
		versions   Versions
		height     int64
		appVersion uint64
		expected   bool
	}{
		{
			name:       "No versions available",
			versions:   Versions{},
			height:     100,
			appVersion: 1,
			expected:   true,
		},
		{
			name: "Height lower than all versions",
			versions: Versions{
				{Name: "v1", UntilHeight: 100, AppVersion: 1},
				{Name: "v2", UntilHeight: 200, AppVersion: 2},
			},
			height:     50,
			appVersion: 1,
			expected:   false,
		},
		{
			name: "Height matches a version",
			versions: Versions{
				{Name: "v1", UntilHeight: 100, AppVersion: 1},
				{Name: "v2", UntilHeight: 200, AppVersion: 2},
			},
			height:     100,
			appVersion: 1,
			expected:   false,
		},
		{
			name: "App version matches a version",
			versions: Versions{
				{Name: "v1", UntilHeight: 100, AppVersion: 1},
				{Name: "v2", UntilHeight: 200, AppVersion: 2},
			},
			height:     150,
			appVersion: 2,
			expected:   false,
		},
		{
			name: "Height exceeds all versions",
			versions: Versions{
				{Name: "v1", UntilHeight: 100, AppVersion: 1},
				{Name: "v2", UntilHeight: 200, AppVersion: 2},
			},
			height:     250,
			appVersion: 3,
			expected:   true,
		},
		{
			name: "App version does not match any version",
			versions: Versions{
				{Name: "v1", UntilHeight: 100, AppVersion: 1},
				{Name: "v2", UntilHeight: 200, AppVersion: 2},
			},
			height:     50,
			appVersion: 3,
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.versions.ShouldUseLatestApp(tt.height, tt.appVersion)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestEnsureUniqueNames(t *testing.T) {
	tests := []struct {
		name        string
		versions    Versions
		expectedErr error
	}{
		{
			name:        "no duplicates",
			versions:    []Version{{Name: "v1"}, {Name: "v2"}, {Name: "v3"}},
			expectedErr: nil,
		},
		{
			name:        "duplicate names",
			versions:    []Version{{Name: "v1"}, {Name: "v2"}, {Name: "v1"}},
			expectedErr: errors.New("version with name v1 specified multiple times"),
		},
		{
			name:        "empty list",
			versions:    []Version{},
			expectedErr: nil,
		},
		{
			name:        "single element",
			versions:    []Version{{Name: "v1"}},
			expectedErr: nil,
		},
		{
			name:        "multiple duplicates",
			versions:    []Version{{Name: "v1"}, {Name: "v2"}, {Name: "v1"}, {Name: "v3"}, {Name: "v2"}},
			expectedErr: errors.New("version with name v1 specified multiple times"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.versions.Validate()

			if tt.expectedErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.expectedErr.Error(), "expected error message mismatch")
			} else {
				require.NoError(t, err)
			}
		})
	}
}
