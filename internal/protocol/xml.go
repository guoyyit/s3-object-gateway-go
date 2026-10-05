package protocol

import (
	"encoding/xml"
	"net/http"
)

// ErrorResponse is the XML body of an S3 error response.
type ErrorResponse struct {
	XMLName   struct{} `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource,omitempty"`
	RequestID string   `xml:"RequestId"`
}

// WriteError serializes an S3 error as an XML REST response.
func WriteError(w http.ResponseWriter, r *http.Request, err error, requestID string) {
	s3err := AsS3Error(err)
	body := ErrorResponse{
		Code:      s3err.Code,
		Message:   s3err.Message,
		Resource:  r.URL.Path,
		RequestID: requestID,
	}
	out, marshalErr := xml.MarshalIndent(body, "", "  ")
	if marshalErr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out = append([]byte(xml.Header), out...)
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("X-Amz-Request-Id", requestID)
	if s3err.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		w.Header().Set("Content-Range", "bytes */0")
	}
	w.WriteHeader(s3err.StatusCode)
	_, _ = w.Write(out)
}
