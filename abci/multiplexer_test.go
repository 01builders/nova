package abci

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetDesiredVersion(t *testing.T) {
	tests := []struct {
		name               string
		applicationVersion string
		height             int64
		versions           Versions
		expectedVersion    Version
		expectedError      error
	}{
		{
			name:               "height provided and version found",
			applicationVersion: "",
			height:             10,
			versions:           Versions{{Name: "v1", UntilHeight: 5}, {Name: "v2", UntilHeight: 10}},
			expectedVersion:    Version{Name: "v2", UntilHeight: 10},
			expectedError:      nil,
		},
		{
			name:               "name provided and version found",
			applicationVersion: "v1",
			height:             0,
			versions:           Versions{{Name: "v1", UntilHeight: 5}, {Name: "v2", UntilHeight: 10}},
			expectedVersion:    Version{Name: "v1", UntilHeight: 5},
			expectedError:      nil,
		},
		{
			name:               "both height and name provided",
			applicationVersion: "v1",
			height:             10,
			versions:           Versions{{Name: "v1", UntilHeight: 5}, {Name: "v2", UntilHeight: 10}},
			expectedVersion:    Version{},
			expectedError:      ErrInvalidArgument,
		},
		{
			name:               "height provided but no version found",
			applicationVersion: "",
			height:             20,
			versions:           Versions{{Name: "v1", UntilHeight: 5}, {Name: "v2", UntilHeight: 10}},
			expectedVersion:    Version{},
			expectedError:      ErrNoVersionFound,
		},
		{
			name:               "name provided but no version found",
			applicationVersion: "v3",
			height:             0,
			versions:           Versions{{Name: "v1", UntilHeight: 5}, {Name: "v2", UntilHeight: 10}},
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
