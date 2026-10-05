// Package s3error defines S3 protocol error codes and XML error responses.
package s3error

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

// Error is an S3 protocol error carrying a code, message and HTTP status.
type Error struct {
	Code       string
	Message    string
	StatusCode int
	Resource   string
	RequestID  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("s3 error %s: %s", e.Code, e.Message)
}

type errorInfo struct {
	code       string
	message    string
	statusCode int
}

var errorTable = map[string]errorInfo{
	"AccessDenied":                 {"AccessDenied", "Access Denied", http.StatusForbidden},
	"SignatureDoesNotMatch":        {"SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided.", http.StatusForbidden},
	"InvalidAccessKeyId":           {"InvalidAccessKeyId", "The AWS Access Key Id you provided does not exist in our records.", http.StatusForbidden},
	"RequestTimeTooSkewed":         {"RequestTimeTooSkewed", "The difference between the request time and the server's time is too large.", http.StatusForbidden},
	"MissingAuthenticationToken":   {"MissingAuthenticationToken", "The request must contain a valid AWS Authentication header.", http.StatusForbidden},
	"AuthorizationHeaderMalformed": {"AuthorizationHeaderMalformed", "The authorization header is malformed.", http.StatusBadRequest},
	"InvalidArgument":              {"InvalidArgument", "Invalid Argument.", http.StatusBadRequest},
	"InvalidRequest":               {"InvalidRequest", "Invalid Request.", http.StatusBadRequest},
	"MalformedXML":                 {"MalformedXML", "The XML you provided was not well-formed or did not validate against our published schema.", http.StatusBadRequest},
	"InvalidRange":                 {"InvalidRange", "The requested range is not satisfiable.", http.StatusRequestedRangeNotSatisfiable},
	"EntityTooSmall":               {"EntityTooSmall", "Your proposed upload is smaller than the minimum allowed object size.", http.StatusBadRequest},
	"EntityTooLarge":               {"EntityTooLarge", "Your proposed upload exceeds the maximum allowed object size.", http.StatusBadRequest},
	"InvalidPart":                  {"InvalidPart", "One or more of the specified parts could not be found.", http.StatusBadRequest},
	"InvalidPartOrder":             {"InvalidPartOrder", "The list of parts was not in ascending order.", http.StatusBadRequest},
	"NoSuchBucket":                 {"NoSuchBucket", "The specified bucket does not exist.", http.StatusNotFound},
	"NoSuchKey":                    {"NoSuchKey", "The specified key does not exist.", http.StatusNotFound},
	"NoSuchUpload":                 {"NoSuchUpload", "The specified multipart upload does not exist.", http.StatusNotFound},
	"PreconditionFailed":           {"PreconditionFailed", "At least one of the pre-conditions you specified did not hold.", http.StatusPreconditionFailed},
	"MethodNotAllowed":             {"MethodNotAllowed", "The specified method is not allowed against this resource.", http.StatusMethodNotAllowed},
	"NotImplemented":               {"NotImplemented", "A header you provided implies functionality that is not implemented.", http.StatusNotImplemented},
	"InternalError":                {"InternalError", "We encountered an internal error. Please try again.", http.StatusInternalServerError},
}

// New returns the registered error for the given S3 code, or nil.
func New(code string) *Error {
	info, ok := errorTable[code]
	if !ok {
		return nil
	}
	return &Error{Code: info.code, Message: info.message, StatusCode: info.statusCode}
}

// Newf returns a registered error with a custom message formatted via fmt.
func Newf(code, format string, args ...any) *Error {
	e := New(code)
	if e == nil {
		return &Error{Code: code, Message: fmt.Sprintf(format, args...), StatusCode: http.StatusInternalServerError}
	}
	e.Message = fmt.Sprintf(format, args...)
	return e
}

type xmlError struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Key       string   `xml:"Key,omitempty"`
	Resource  string   `xml:"Resource,omitempty"`
	RequestID string   `xml:"RequestId"`
}

var requestCounter uint64

// RequestID generates a unique request identifier for logging and responses.
func RequestID() string {
	n := atomic.AddUint64(&requestCounter, 1)
	return fmt.Sprintf("%016X", n)
}

// WriteXML serializes an error as an S3-compatible XML error response.
func WriteXML(w http.ResponseWriter, r *http.Request, err error) {
	e, ok := err.(*Error)
	if !ok {
		e = New("InternalError")
	}
	body := xmlError{
		Code:      e.Code,
		Message:   e.Message,
		Resource:  e.Resource,
		RequestID: e.RequestID,
	}
	if body.RequestID == "" {
		body.RequestID = RequestID()
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("X-Amz-Request-Id", body.RequestID)
	w.Header().Set("Date", time.Now().UTC().Format(http.TimeFormat))
	w.WriteHeader(e.StatusCode)
	enc := xml.NewEncoder(w)
	_ = enc.Encode(body)
}
