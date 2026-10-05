package server

import (
	"encoding/xml"
	"net/http"

	"github.com/guoyyit/s3-object-gateway-go/internal/protocol"
)

type listMultipartUploadsResult struct {
	XMLName xml.Name             `xml:"ListMultipartUploadsResult"`
	Xmlns   string               `xml:"xmlns,attr"`
	Bucket  string               `xml:"Bucket"`
	Uploads []multipartUploadXML `xml:"Upload"`
}

type multipartUploadXML struct {
	Key       string `xml:"Key"`
	UploadID  string `xml:"UploadId"`
	Initiated string `xml:"Initiated"`
}

func (s *Server) handleListMultipart(w http.ResponseWriter, r *http.Request, bucket, requestID string) {
	if r.Method != http.MethodGet {
		protocol.WriteError(w, r, protocol.ErrMethodNotAllowed, requestID)
		return
	}
	ups, err := s.store.ListMultipartUploads(r.Context(), bucket)
	if err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	}
	out := listMultipartUploadsResult{
		Xmlns:  "http://s3.amazonaws.com/doc/2006-03-01/",
		Bucket: bucket,
	}
	for _, up := range ups {
		out.Uploads = append(out.Uploads, multipartUploadXML{
			Key:       up.Key,
			UploadID:  up.UploadID,
			Initiated: up.Initiated.UTC().Format(timeFormat),
		})
	}
	writeXML(w, http.StatusOK, out)
}
