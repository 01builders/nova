package appd

import (
	"fmt"
	"runtime"
)

// CelestiaApp returns the compressed platform specific Celestia binary.
func CelestiaApp() ([]byte, error) {
	platform := runtime.GOOS + "_" + runtime.GOARCH // Check if we actually have binary data
	if len(binaryCompressed) == 0 {
		return nil, fmt.Errorf("no binary data available for platform %s", platform)
	}

	return binaryCompressed, nil
}
