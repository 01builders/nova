package abci

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetDesiredVersion(t *testing.T) {
	tests := []struct {
		name               string
		applicationVersion uint64
		height             int64
		versions           Versions
		expectedVersion    Version
		expectedError      error
	}{
		{
			name:               "height provided and version found",
			applicationVersion: 0,
			height:             10,
			versions:           Versions{{Name: "v1", AppVersion: 1, UntilHeight: 5}, {Name: "v2", AppVersion: 2, UntilHeight: 10}},
			expectedVersion:    Version{Name: "v2", AppVersion: 2, UntilHeight: 10},
			expectedError:      nil,
		},
		{
			name:               "applicationVersion provided and version found",
			applicationVersion: 1,
			height:             0,
			versions:           Versions{{Name: "v1", AppVersion: 1, UntilHeight: 5}, {Name: "v2", AppVersion: 2, UntilHeight: 10}},
			expectedVersion:    Version{Name: "v1", AppVersion: 1, UntilHeight: 5},
			expectedError:      nil,
		},
		{
			name:               "height provided but no version found",
			applicationVersion: 0,
			height:             20,
			versions:           Versions{{Name: "v1", AppVersion: 1, UntilHeight: 5}, {Name: "v2", AppVersion: 2, UntilHeight: 10}},
			expectedVersion:    Version{},
			expectedError:      ErrNoVersionFound,
		},
		{
			name:               "applicationVersion provided but no version found",
			applicationVersion: 3,
			height:             0,
			versions:           Versions{{Name: "v1", AppVersion: 1, UntilHeight: 5}, {Name: "v2", AppVersion: 2, UntilHeight: 10}},
			expectedVersion:    Version{},
			expectedError:      ErrNoVersionFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version, err := getDesiredVersion(tt.applicationVersion, tt.height, tt.versions)

			if tt.expectedError != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tt.expectedError)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tt.expectedVersion, version)
		})
	}
}
