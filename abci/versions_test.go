package abci

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShouldUseLatestApp(t *testing.T) {
	tests := []struct {
		name       string
		versions   Versions
		appVersion uint64
		expected   bool
	}{
		{"No versions available", Versions{}, 1, true},
		{
			"App version matches the first version",
			Versions{
				{AppVersion: 1},
				{AppVersion: 2},
			}, 1, false,
		},
		{
			"App version matches a later version",
			Versions{
				{AppVersion: 1},
				{AppVersion: 2},
			}, 2, false,
		},
		{
			"App version does not match any version",
			Versions{
				{AppVersion: 1},
				{AppVersion: 2},
			}, 3, true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.versions.ShouldUseLatestApp(tt.appVersion))
		})
	}
}

func TestEnsureUniqueVersions(t *testing.T) {
	tests := []struct {
		name        string
		versions    Versions
		expectedErr error
	}{
		{
			name:        "no duplicates",
			versions:    []Version{{AppVersion: 1}, {AppVersion: 2}, {AppVersion: 3}},
			expectedErr: nil,
		},
		{
			name:        "duplicate app versions",
			versions:    []Version{{AppVersion: 1}, {AppVersion: 2}, {AppVersion: 1}},
			expectedErr: errors.New("version 1 specified multiple times"),
		},
		{
			name:        "empty list",
			versions:    []Version{},
			expectedErr: errors.New("no versions specified"),
		},
		{
			name:        "single element",
			versions:    []Version{{AppVersion: 1}},
			expectedErr: nil,
		},
		{
			name:        "multiple duplicates",
			versions:    []Version{{AppVersion: 1}, {AppVersion: 2}, {AppVersion: 1}, {AppVersion: 3}, {AppVersion: 2}},
			expectedErr: errors.New("version 1 specified multiple times"),
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
