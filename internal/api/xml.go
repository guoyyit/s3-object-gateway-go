package api

import (
	"encoding/xml"
	"net/http"
)

const s3NS = "http://s3.amazonaws.com/doc/2006-03-01/"

func writeXML(w http.ResponseWriter, status int, v any) {
	body, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n"))
}

type listAllMyBucketsResult struct {
	XMLName xml.Name `xml:"ListAllMyBucketsResult"`
	Xmlns   string   `xml:"xmlns,attr"`
	Owner   ownerXML
	Buckets bucketsXML
}

type ownerXML struct {
	XMLName     xml.Name `xml:"Owner"`
	ID          string   `xml:"ID"`
	DisplayName string   `xml:"DisplayName"`
}

type bucketsXML struct {
	XMLName xml.Name `xml:"Buckets"`
	Bucket  []bucketXML
}

type bucketXML struct {
	XMLName      xml.Name `xml:"Bucket"`
	Name         string   `xml:"Name"`
	CreationDate string   `xml:"CreationDate"`
}

type listBucketResult struct {
	XMLName        xml.Name       `xml:"ListBucketResult"`
	Xmlns          string         `xml:"xmlns,attr"`
	Name           string         `xml:"Name"`
	Prefix         string         `xml:"Prefix"`
	Marker         string         `xml:"Marker,omitempty"`
	MaxKeys        int            `xml:"MaxKeys"`
	Delimiter      string         `xml:"Delimiter,omitempty"`
	IsTruncated    bool           `xml:"IsTruncated"`
	NextMarker     string         `xml:"NextMarker,omitempty"`
	Contents       []contentXML   `xml:",omitempty"`
	CommonPrefixes []commonPrefix `xml:",omitempty"`
	KeyCount       int            `xml:"KeyCount"`
}

type contentXML struct {
	XMLName      xml.Name `xml:"Contents"`
	Key          string   `xml:"Key"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml:"ETag"`
	Size         int64    `xml:"Size"`
	StorageClass string   `xml:"StorageClass"`
}

type commonPrefix struct {
	XMLName xml.Name `xml:"CommonPrefixes"`
	Prefix  string   `xml:"Prefix"`
}

type initiateMultipartResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Xmlns    string   `xml:"xmlns,attr"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

type completeMultipartUpload struct {
	XMLName xml.Name          `xml:"CompleteMultipartUpload"`
	Parts   []completedPartIn `xml:"Part"`
}

type completedPartIn struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type completeMultipartResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	Xmlns    string   `xml:"xmlns,attr"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}

type listPartsResult struct {
	XMLName              xml.Name      `xml:"ListPartsResult"`
	Xmlns                string        `xml:"xmlns,attr"`
	Bucket               string        `xml:"Bucket"`
	Key                  string        `xml:"Key"`
	UploadID             string        `xml:"UploadId"`
	Initiator            ownerXML      `xml:"Initiator"`
	Owner                ownerXML      `xml:"Owner"`
	StorageClass         string        `xml:"StorageClass"`
	PartNumberMarker     int           `xml:"PartNumberMarker"`
	NextPartNumberMarker int           `xml:"NextPartNumberMarker"`
	MaxParts             int           `xml:"MaxParts"`
	IsTruncated          bool          `xml:"IsTruncated"`
	Part                 []listPartXML `xml:",omitempty"`
}

type listPartXML struct {
	XMLName      xml.Name `xml:"Part"`
	PartNumber   int      `xml:"PartNumber"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml:"ETag"`
	Size         int64    `xml:"Size"`
}

type listMultipartUploadsResult struct {
	XMLName        xml.Name    `xml:"ListMultipartUploadsResult"`
	Xmlns          string      `xml:"xmlns,attr"`
	Bucket         string      `xml:"Bucket"`
	KeyMarker      string      `xml:"KeyMarker,omitempty"`
	UploadIDMarker string      `xml:"UploadIdMarker,omitempty"`
	NextKeyMarker  string      `xml:"NextKeyMarker,omitempty"`
	MaxUploads     int         `xml:"MaxUploads"`
	IsTruncated    bool        `xml:"IsTruncated"`
	Upload         []uploadXML `xml:",omitempty"`
}

type uploadXML struct {
	XMLName      xml.Name `xml:"Upload"`
	Key          string   `xml:"Key"`
	UploadID     string   `xml:"UploadId"`
	Initiator    ownerXML `xml:"Initiator"`
	Owner        ownerXML `xml:"Owner"`
	StorageClass string   `xml:"StorageClass"`
	Initiated    string   `xml:"Initiated"`
}

type copyObjectResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	ETag         string   `xml:"ETag"`
	LastModified string   `xml:"LastModified"`
}

type deleteRequest struct {
	XMLName xml.Name       `xml:"Delete"`
	Objects []deleteObject `xml:"Object"`
	Quiet   bool           `xml:"Quiet"`
}

type deleteObject struct {
	Key       string `xml:"Key"`
	VersionID string `xml:"VersionId,omitempty"`
}

type deleteResult struct {
	XMLName xml.Name     `xml:"DeleteResult"`
	Xmlns   string       `xml:"xmlns,attr"`
	Deleted []deletedKey `xml:",omitempty"`
}

type deletedKey struct {
	XMLName xml.Name `xml:"Deleted"`
	Key     string   `xml:"Key"`
}

type errorResultXML struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}
