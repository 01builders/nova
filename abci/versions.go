package abci

import (
	"errors"
	"fmt"

	"github.com/01builders/nova/appd"
)

// ErrNoVersionFound is returned when no remote version is found for a given height.
var ErrNoVersionFound = errors.New("no version found")

// Version defines the configuration for remote apps.
type Version struct {
	Appd        *appd.Appd
	UntilHeight int64
	PreHandler  string   // Command to run before starting the app
	StartArgs   []string // Extra arguments to pass to the app
}

type Versions map[string]Version

// GenesisVersion returns the genesis version.
func (v Versions) GenesisVersion() (Version, error) {
	var genesis Version
	var minHeight int64 = -1

	for _, version := range v {
		if minHeight == -1 || version.UntilHeight < minHeight {
			minHeight = version.UntilHeight
			genesis = version
		}
	}

	if minHeight == -1 {
		return Version{}, fmt.Errorf("%w: no genesis version found", ErrNoVersionFound)
	}

	return genesis, nil
}

// GetForHeight returns the version for a given height.
func (v Versions) GetForHeight(height int64) (Version, error) {
	var selectedVersion Version
	for _, version := range v {
		if version.UntilHeight >= height {
			selectedVersion = version
			break
		}
	}

	if selectedVersion.UntilHeight < height {
		return Version{}, fmt.Errorf("%w: %d", ErrNoVersionFound, height)
	}

	return selectedVersion, nil
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

// GetStartArgs returns the appropriate args.
func (v Version) GetStartArgs(args []string) []string {
	if len(v.StartArgs) > 0 {
		return append(args, v.StartArgs...)
	}

	// Default flags for standalone apps.
	return append(args,
		"--grpc.enable=true",
		"--api.enable=false",
		"--api.swagger=false",
		"--with-tendermint=false",
		"--transport=grpc",
	)
}
