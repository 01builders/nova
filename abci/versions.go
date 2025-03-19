package abci

import (
	"fmt"
	"sort"

	"github.com/01builders/nova/appd"
)

// Version defines the configuration for remote apps.
type Version struct {
	Name        string
	AppVersion  uint64
	ABCIVersion ABCIClientVersion
	Appd        *appd.Appd
	UntilHeight int64
	PreHandler  string   // Command to run before starting the app
	StartArgs   []string // Extra arguments to pass to the app
}

type Versions []Version

// Sorted returns a sorted slice of Versions, sorted by UntilHeight (ascending).
func (v Versions) Sorted() Versions {
	// convert map to slice
	versionList := make([]Version, 0, len(v))
	for _, ver := range v {
		versionList = append(versionList, ver)
	}

	// sort by UntilHeight in ascending order
	sort.SliceStable(versionList, func(i, j int) bool {
		return versionList[i].UntilHeight < versionList[j].UntilHeight
	})

	return versionList
}

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

// GetForName returns the version for a given name.
func (v Versions) GetForName(name string) (Version, error) {
	for _, version := range v {
		if version.Name == name {
			return version, nil
		}
	}
	return Version{}, fmt.Errorf("%w: %s", ErrNoVersionFound, name)
}

// GetForAppVersion returns the version for a given appVersion.
func (v Versions) GetForAppVersion(appVersion uint64) (Version, error) {
	for _, version := range v {
		if version.AppVersion == appVersion {
			return version, nil
		}
	}
	return Version{}, fmt.Errorf("%w: %d", ErrNoVersionFound, appVersion)
}

// GetForHeight returns the version for a given height.
func (v Versions) GetForHeight(height int64) (Version, error) {
	if height == 0 {
		return v.GenesisVersion()
	}

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
		"--api.enable=true",
		"--api.swagger=false",
		"--with-tendermint=false",
		"--transport=grpc",
	)
}

// Validate checks for duplicate names in a slice of Versions.
func (v Versions) Validate() error {
	seen := make(map[string]struct{})
	for _, v := range v {
		if _, exists := seen[v.Name]; exists {
			return fmt.Errorf("version with name %s specified multiple times", v.Name)
		}
		seen[v.Name] = struct{}{}
	}
	return nil
}
