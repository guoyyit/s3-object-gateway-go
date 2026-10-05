package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// storedMeta is the on-disk JSON representation of a committed object.
type storedMeta struct {
	Key                string            `json:"key"`
	VersionID          string            `json:"versionId"`
	DataFile           string            `json:"dataFile"`
	Size               int64             `json:"size"`
	ContentType        string            `json:"contentType,omitempty"`
	ContentEncoding    string            `json:"contentEncoding,omitempty"`
	ContentLanguage    string            `json:"contentLanguage,omitempty"`
	ContentDisposition string            `json:"contentDisposition,omitempty"`
	CacheControl       string            `json:"cacheControl,omitempty"`
	ETag               string            `json:"etag"`
	LastModified       time.Time         `json:"lastModified"`
	Expires            time.Time         `json:"expires,omitempty"`
	StorageClass       string            `json:"storageClass,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

func (m *storedMeta) toObjectMeta() ObjectMeta {
	return ObjectMeta{
		Key:                m.Key,
		Size:               m.Size,
		ContentType:        m.ContentType,
		ContentEncoding:    m.ContentEncoding,
		ContentLanguage:    m.ContentLanguage,
		ContentDisposition: m.ContentDisposition,
		CacheControl:       m.CacheControl,
		ETag:               m.ETag,
		LastModified:       m.LastModified,
		Expires:            m.Expires,
		StorageClass:       m.StorageClass,
		Metadata:           m.Metadata,
	}
}

// storedUpload is the on-disk JSON record of an in-progress multipart upload.
type storedUpload struct {
	UploadID  string     `json:"uploadId"`
	Key       string     `json:"key"`
	Meta      storedMeta `json:"meta"`
	Parts     []partRec  `json:"parts"`
	Initiated time.Time  `json:"initiated"`
}

type partRec struct {
	PartNumber int    `json:"partNumber"`
	ETag       string `json:"etag"`
	Size       int64  `json:"size"`
	File       string `json:"file"`
}

// manifest is the durable index of committed objects, keyed by object key.
type manifest struct {
	Objects map[string]storedMeta `json:"objects"`
}

func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

func readJSONFile(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(f).Decode(v)
}
