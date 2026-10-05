package server

import (
	"strconv"
	"strings"

	"github.com/guoyyit/s3-object-gateway-go/internal/protocol"
)

// byteRange describes one inclusive byte range within an object.
type byteRange struct {
	start int64
	end   int64
}

// parseRange parses one "bytes=" range against the object size.
// Returns (nil, false, nil) when no Range header was sent.
func parseRange(header string, size int64) (*byteRange, bool, error) {
	if header == "" {
		return nil, false, nil
	}
	if !strings.HasPrefix(header, "bytes=") {
		return nil, true, protocol.ErrInvalidRange
	}
	spec := strings.TrimPrefix(header, "bytes=")
	if strings.Contains(spec, ",") {
		// Multiple ranges are not supported; S3 clients use a single range.
		return nil, true, protocol.ErrInvalidRange
	}
	spec = strings.TrimSpace(spec)
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return nil, true, protocol.ErrInvalidRange
	}
	startStr, endStr := spec[:dash], spec[dash+1:]

	if startStr == "" {
		if endStr == "" {
			return nil, true, protocol.ErrInvalidRange
		}
		suffix, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || suffix <= 0 {
			return nil, true, protocol.ErrInvalidRange
		}
		if size == 0 {
			return nil, true, protocol.ErrInvalidRange
		}
		start := size - suffix
		if start < 0 {
			start = 0
		}
		return &byteRange{start: start, end: size - 1}, true, nil
	}

	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil || start < 0 {
		return nil, true, protocol.ErrInvalidRange
	}
	if start >= size {
		return nil, true, protocol.ErrInvalidRange
	}
	end := size - 1
	if endStr != "" {
		parsed, perr := strconv.ParseInt(endStr, 10, 64)
		if perr != nil {
			return nil, true, protocol.ErrInvalidRange
		}
		if parsed < start {
			return nil, true, protocol.ErrInvalidRange
		}
		end = parsed
		if end > size-1 {
			end = size - 1
		}
	}
	return &byteRange{start: start, end: end}, true, nil
}
