package appd

import (
	"log"
	"runtime"
)

// CelestiaApp returns the compressed platform specific Celestia binary.
func CelestiaApp() []byte {
	// Add platform detection and logging for debugging
	platform := runtime.GOOS + "_" + runtime.GOARCH
	log.Printf("Loading binary for platform: %s", platform)

	// Check if we actually have binary data
	if len(binaryCompressed) == 0 {
		log.Fatalf("Warning: No binary data available for platform %s", platform)
	}

	return binaryCompressed
}
