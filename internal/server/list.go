package server

import (
	"encoding/xml"
	"net/http"
	"strconv"

	"github.com/guoyyit/s3-object-gateway-go/internal/protocol"
	"github.com/guoyyit/s3-object-gateway-go/internal/storage"
)

type listBucketResult struct {
	XMLName               xml.Name       `xml:"ListBucketResult"`
	Xmlns                 string         `xml:"xmlns,attr"`
	Name                  string         `xml:"Name"`
	Prefix                string         `xml:"Prefix"`
	Delimiter             string         `xml:"Delimiter,omitempty"`
	MaxKeys               int            `xml:"MaxKeys"`
	IsTruncated           bool           `xml:"IsTruncated"`
	KeyCount              int            `xml:"KeyCount"`
	ContinuationToken     string         `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string         `xml:"NextContinuationToken,omitempty"`
	StartAfter            string         `xml:"StartAfter,omitempty"`
	Contents              []listContent  `xml:"Contents"`
	CommonPrefixes        []commonPrefix `xml:"CommonPrefixes"`
}

type listContent struct {
	Key          string  `xml:"Key"`
	LastModified string  `xml:"LastModified"`
	ETag         etagXML `xml:"ETag"`
	Size         int64   `xml:"Size"`
	StorageClass string  `xml:"StorageClass"`
}

type etagXML string

func (e etagXML) MarshalXML(enc *xml.Encoder, start xml.StartElement) error {
	return enc.EncodeElement(struct {
		Inner string `xml:",innerxml"`
	}{Inner: string(e)}, start)
}

type commonPrefix struct {
	Prefix string `xml:"Prefix"`
}

func (s *Server) handleListObjects(w http.ResponseWriter, r *http.Request, bucket, requestID string) {
	q := r.URL.Query()
	maxKeys := 1000
	if v := q.Get("max-keys"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxKeys = n
		}
	}
	result, err := s.store.ListObjects(r.Context(), bucket, storage.ListOptions{
		Prefix:            q.Get("prefix"),
		Delimiter:         q.Get("delimiter"),
		StartAfter:        q.Get("start-after"),
		ContinuationToken: q.Get("continuation-token"),
		MaxKeys:           maxKeys,
	})
	if err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	}

	out := listBucketResult{
		Xmlns:             "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:              bucket,
		Prefix:            q.Get("prefix"),
		Delimiter:         q.Get("delimiter"),
		MaxKeys:           maxKeys,
		IsTruncated:       result.IsTruncated,
		ContinuationToken: q.Get("continuation-token"),
		StartAfter:        q.Get("start-after"),
	}
	for _, info := range result.Objects {
		out.Contents = append(out.Contents, listContent{
			Key:          info.Key,
			LastModified: info.LastModified.UTC().Format(timeFormat),
			ETag:         etagXML(quoteETag(info.ETag)),
			Size:         info.Size,
			StorageClass: "STANDARD",
		})
	}
	for _, cp := range result.CommonPrefixes {
		out.CommonPrefixes = append(out.CommonPrefixes, commonPrefix{Prefix: cp})
	}
	out.KeyCount = len(out.Contents) + len(out.CommonPrefixes)
	out.NextContinuationToken = result.NextContinuationToken
	writeXML(w, http.StatusOK, out)
}

type listBucketV1Result struct {
	XMLName        xml.Name       `xml:"ListBucketResult"`
	Xmlns          string         `xml:"xmlns,attr"`
	Name           string         `xml:"Name"`
	Prefix         string         `xml:"Prefix"`
	Delimiter      string         `xml:"Delimiter,omitempty"`
	Marker         string         `xml:"Marker,omitempty"`
	MaxKeys        int            `xml:"MaxKeys"`
	IsTruncated    bool           `xml:"IsTruncated"`
	NextMarker     string         `xml:"NextMarker,omitempty"`
	Contents       []listContent  `xml:"Contents"`
	CommonPrefixes []commonPrefix `xml:"CommonPrefixes"`
}

func (s *Server) handleListObjectsV1(w http.ResponseWriter, r *http.Request, bucket, requestID string) {
	q := r.URL.Query()
	maxKeys := 1000
	if v := q.Get("max-keys"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxKeys = n
		}
	}
	result, err := s.store.ListObjects(r.Context(), bucket, storage.ListOptions{
		Prefix:            q.Get("prefix"),
		Delimiter:         q.Get("delimiter"),
		ContinuationToken: q.Get("marker"),
		MaxKeys:           maxKeys,
	})
	if err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	}
	out := listBucketV1Result{
		Xmlns:       "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:        bucket,
		Prefix:      q.Get("prefix"),
		Delimiter:   q.Get("delimiter"),
		Marker:      q.Get("marker"),
		MaxKeys:     maxKeys,
		IsTruncated: result.IsTruncated,
		NextMarker:  result.NextContinuationToken,
	}
	for _, info := range result.Objects {
		out.Contents = append(out.Contents, listContent{
			Key:          info.Key,
			LastModified: info.LastModified.UTC().Format(timeFormat),
			ETag:         etagXML(quoteETag(info.ETag)),
			Size:         info.Size,
			StorageClass: "STANDARD",
		})
	}
	for _, cp := range result.CommonPrefixes {
		out.CommonPrefixes = append(out.CommonPrefixes, commonPrefix{Prefix: cp})
	}
	writeXML(w, http.StatusOK, out)
}
