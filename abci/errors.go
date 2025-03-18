package abci

import "errors"

var (
	// ErrNoVersionFound is returned when no remote version is found for a given height.
	ErrNoVersionFound = errors.New("no version found")

	// ErrInvalidArgument is returned when an invalid argument is provided.
	ErrInvalidArgument = errors.New("invalid argument")
)
