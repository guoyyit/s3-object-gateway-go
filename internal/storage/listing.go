package storage

import (
	"context"
	"sort"

	"github.com/guoyyit/s3-object-gateway-go/internal/protocol"
)

// ListOptions are S3 ListObjectsV2 selection parameters.
type ListOptions struct {
	Prefix            string
	Delimiter         string
	StartAfter        string
	ContinuationToken string
	MaxKeys           int
}

// ListResult is one immutable-snapshot listing page.
type ListResult struct {
	Objects               []ObjectInfo
	CommonPrefixes        []string
	IsTruncated           bool
	NextContinuationToken string
}

// ListObjects returns one page based on a consistent snapshot.
//
// The key set is copied under the store read lock, so concurrent writes or
// deletes cannot cause duplicates or omissions within the page. The
// continuation token is either a key or a rolled-up common prefix; both are
// compared lexicographically, so a token stays valid across pages.
func (s *Store) ListObjects(ctx context.Context, bucket string, opts ListOptions) (*ListResult, error) {
	s.mu.RLock()
	objs, ok := s.buckets[bucket]
	if !ok {
		s.mu.RUnlock()
		return nil, protocol.ErrNoSuchBucket
	}
	snapshot := make([]ObjectInfo, 0, len(objs))
	for _, info := range objs {
		snapshot = append(snapshot, *info)
	}
	s.mu.RUnlock()

	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].Key < snapshot[j].Key })

	marker := opts.ContinuationToken
	if marker == "" {
		marker = opts.StartAfter
	}
	maxKeys := opts.MaxKeys
	if maxKeys <= 0 {
		maxKeys = 1000
	}

	result := &ListResult{Objects: []ObjectInfo{}, CommonPrefixes: []string{}}
	seenPrefixes := map[string]bool{}
	emitted := 0
	var nextToken string

	for _, info := range snapshot {
		if !stringHasPrefix(info.Key, opts.Prefix) {
			continue
		}
		var commonPrefix string
		if opts.Delimiter != "" {
			commonPrefix = rollupPrefix(info.Key, opts.Prefix, opts.Delimiter)
		}

		if commonPrefix != "" {
			if commonPrefix <= marker || seenPrefixes[commonPrefix] {
				continue
			}
			if emitted >= maxKeys {
				result.IsTruncated = true
				break
			}
			seenPrefixes[commonPrefix] = true
			result.CommonPrefixes = append(result.CommonPrefixes, commonPrefix)
			nextToken = commonPrefix
			emitted++
			continue
		}

		if info.Key <= marker {
			continue
		}
		if emitted >= maxKeys {
			result.IsTruncated = true
			break
		}
		result.Objects = append(result.Objects, info)
		nextToken = info.Key
		emitted++
	}

	if result.IsTruncated {
		result.NextContinuationToken = nextToken
	}
	return result, nil
}

func stringHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// rollupPrefix returns the common prefix ending in delimiter that contains
// key (relative to prefix), or "" when there is no delimiter after prefix.
func rollupPrefix(key, prefix, delimiter string) string {
	rel := key[len(prefix):]
	idx := -1
	for i := 0; i+len(delimiter) <= len(rel); i++ {
		if rel[i:i+len(delimiter)] == delimiter {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ""
	}
	return key[:len(prefix)+idx+len(delimiter)]
}
