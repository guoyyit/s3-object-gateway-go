package protocol

import "net/http"

// S3Error describes an S3 REST error response.
type S3Error struct {
	Code       string
	Message    string
	StatusCode int
}

func (e *S3Error) Error() string { return e.Code }

// Standard S3 errors used by the gateway.
var (
	ErrAccessDenied                 = &S3Error{"AccessDenied", "Access Denied", http.StatusForbidden}
	ErrInvalidArgument              = &S3Error{"InvalidArgument", "Invalid Argument", http.StatusBadRequest}
	ErrInvalidRange                 = &S3Error{"InvalidRange", "The requested range is not satisfiable", http.StatusRequestedRangeNotSatisfiable}
	ErrPreconditionFailed           = &S3Error{"PreconditionFailed", "At least one of the pre-conditions you specified did not hold", http.StatusPreconditionFailed}
	ErrNoSuchBucket                 = &S3Error{"NoSuchBucket", "The specified bucket does not exist", http.StatusNotFound}
	ErrNoSuchKey                    = &S3Error{"NoSuchKey", "The specified key does not exist.", http.StatusNotFound}
	ErrNoSuchUpload                 = &S3Error{"NoSuchUpload", "The specified multipart upload does not exist.", http.StatusNotFound}
	ErrInvalidPart                  = &S3Error{"InvalidPart", "One or more of the specified parts could not be found.", http.StatusBadRequest}
	ErrEntityTooSmall               = &S3Error{"EntityTooSmall", "Your proposed upload is smaller than the minimum allowed object size.", http.StatusBadRequest}
	ErrMethodNotAllowed             = &S3Error{"MethodNotAllowed", "The specified method is not allowed against this resource.", http.StatusMethodNotAllowed}
	ErrNotImplemented               = &S3Error{"NotImplemented", "A header you provided implies functionality that is not implemented.", http.StatusNotImplemented}
	ErrInternal                     = &S3Error{"InternalError", "We encountered an internal error. Please try again.", http.StatusInternalServerError}
	ErrBucketAlreadyExists          = &S3Error{"BucketAlreadyExists", "The requested bucket name is not available.", http.StatusConflict}
	ErrBucketNotEmpty               = &S3Error{"BucketNotEmpty", "The bucket you tried to delete is not empty", http.StatusConflict}
	ErrInvalidDigest                = &S3Error{"InvalidDigest", "The Content-MD5 you specified is not valid.", http.StatusBadRequest}
	ErrBadDigest                    = &S3Error{"BadDigest", "The Content-MD5 you specified did not match what we received.", http.StatusBadRequest}
	ErrAuthorizationHeaderMalformed = &S3Error{"AuthorizationHeaderMalformed", "The authorization header is malformed.", http.StatusBadRequest}
	ErrInvalidAccessKeyID           = &S3Error{"InvalidAccessKeyId", "The AWS Access Key Id you provided does not exist in our records.", http.StatusForbidden}
	ErrSignatureDoesNotMatch        = &S3Error{"SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided.", http.StatusForbidden}
	ErrRequestTimeTooSkewed         = &S3Error{"RequestTimeTooSkewed", "The difference between the request time and the server's time is too large.", http.StatusForbidden}
	ErrContentSHA256Mismatch        = &S3Error{"XAmzContentSHA256Mismatch", "The provided 'x-amz-content-sha256' header does not match what was computed.", http.StatusBadRequest}
	ErrExpiredToken                 = &S3Error{"AccessDenied", "Request has expired", http.StatusForbidden}
	ErrNotModified                  = &S3Error{"NotModified", "Not Modified", http.StatusNotModified}
)

// WithMessage returns a copy of the error with a custom message.
func WithMessage(err *S3Error, msg string) *S3Error {
	cp := *err
	cp.Message = msg
	return &cp
}

// AsS3Error converts any error to an *S3Error, mapping unknown errors to InternalError.
func AsS3Error(err error) *S3Error {
	if err == nil {
		return nil
	}
	if s3err, ok := err.(*S3Error); ok {
		return s3err
	}
	return WithMessage(ErrInternal, err.Error())
}
