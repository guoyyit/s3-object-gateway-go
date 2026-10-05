package storage

import "errors"

var (
	// ErrInvalidPartNumber indicates a part number outside 1..10000.
	ErrInvalidPartNumber = errors.New("invalid part number")
	// ErrPartTooSmall indicates a non-final part smaller than the minimum.
	ErrPartTooSmall = errors.New("part too small")
	// ErrInvalidPartOrder indicates parts were not ascending.
	ErrInvalidPartOrder = errors.New("invalid part order")
	// ErrInvalidPart indicates a referenced part was not uploaded or its
	// ETag does not match.
	ErrInvalidPart = errors.New("invalid part")
)
