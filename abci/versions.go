package abci

import "github.com/01builders/nova/appd"

// Version defines the configuration for remote apps.
type Version struct {
	Appd        *appd.Appd
	UntilHeight int64
	StartArgs   []string // Extra arguments to pass to the app
}

type Versions map[string]Version

// GenesisVersion returns the genesis version.
func (v Versions) GenesisVersion() Version {
	var genesis Version
	var minHeight int64 = -1

	for _, version := range v {
		if minHeight == -1 || version.UntilHeight < minHeight {
			minHeight = version.UntilHeight
			genesis = version
		}
	}

	return genesis
}

// GetForHeight returns the version for a given height.
func (v Versions) GetForHeight(height int64) (string, Version) {
	var selectedVersion Version
	var name string

	for n, version := range v {
		if version.UntilHeight >= height {
			selectedVersion = version
			name = n
			break
		}
	}

	return name, selectedVersion
}

// ShouldLatestApp returns true if the given height is higher than all version's UntilHeight.
func (v Versions) ShouldLatestApp(height int64) bool {
	for _, version := range v {
		if version.UntilHeight >= height {
			return false
		}
	}
	return true
}
