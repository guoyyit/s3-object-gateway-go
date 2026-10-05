package auth

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"strconv"
	"strings"
)

// chunkedVerifier decodes an aws-chunked request body, verifying the
// per-chunk HMAC-SHA256 signatures and optionally the Content-MD5 of the
// decoded payload. See S3 "Signing streaming uploads".
//
// Wire format:
//
//	<hex-size>;chunk-signature=<sig>\r\n
//	<data>\r\n
//	...
//	0;chunk-signature=<sig>\r\n
//	[\r\n trailers \r\n]
type chunkedVerifier struct {
	br      *bufio.Reader
	base    io.ReadCloser
	key     []byte
	date    string
	scope   string
	prevSig []byte

	md5h    hash.Hash
	wantMD5 string

	chunk []byte
	pos   int
	err   error
}

func newChunkedVerifier(base io.ReadCloser, key []byte, date, scope, seedSig, wantMD5 string) *chunkedVerifier {
	v := &chunkedVerifier{
		br:      bufio.NewReader(base),
		base:    base,
		key:     key,
		date:    date,
		scope:   scope,
		prevSig: []byte(seedSig),
		wantMD5: wantMD5,
	}
	if wantMD5 != "" {
		v.md5h = md5.New()
	}
	return v
}

func (v *chunkedVerifier) Read(p []byte) (int, error) {
	if v.err != nil {
		return 0, v.err
	}
	if v.pos >= len(v.chunk) {
		if err := v.nextChunk(); err != nil {
			if errors.Is(err, io.EOF) {
				v.err = io.EOF
				return 0, io.EOF
			}
			v.err = err
			return 0, err
		}
	}
	n := copy(p, v.chunk[v.pos:])
	v.pos += n
	if v.md5h != nil {
		v.md5h.Write(p[:n])
	}
	return n, nil
}

func (v *chunkedVerifier) Close() error {
	return v.base.Close()
}

func (v *chunkedVerifier) nextChunk() error {
	size, sig, err := v.readHeader()
	if err != nil {
		return err
	}

	data := make([]byte, size)
	if _, err := io.ReadFull(v.br, data); err != nil {
		return ErrBodySignature
	}

	sts := strings.Join([]string{
		"AWS4-HMAC-SHA256-PAYLOAD",
		v.date,
		v.scope,
		string(v.prevSig),
		emptySHA256,
		hexSHA256(data),
	}, "\n")
	expected := hmacSHA256(v.key, []byte(sts))
	if !hmac.Equal([]byte(sig), expected) {
		return ErrBodySignature
	}

	v.prevSig = expected
	if size == 0 {
		// Consume optional trailing headers up to the blank line.
		if err := v.drainTrailers(); err != nil {
			return err
		}
		if v.md5h != nil {
			want, derr := base64.StdEncoding.DecodeString(v.wantMD5)
			if derr != nil || !bytes.Equal(want, v.md5h.Sum(nil)) {
				return ErrBodySignature
			}
		}
		v.chunk = nil
		v.pos = 0
		return io.EOF
	}

	if err := v.expectCRLF(); err != nil {
		return err
	}
	v.chunk = data
	v.pos = 0
	return nil
}

func (v *chunkedVerifier) readHeader() (int64, []byte, error) {
	line, err := v.br.ReadString('\n')
	if err != nil {
		return 0, nil, ErrBodySignature
	}
	line = strings.TrimRight(line, "\r\n")
	sizeField, params, _ := strings.Cut(line, ";")
	size, err := strconv.ParseInt(strings.TrimSpace(sizeField), 16, 64)
	if err != nil || size < 0 {
		return 0, nil, ErrBodySignature
	}
	var sig string
	for _, p := range strings.Split(params, ";") {
		k, val, ok := strings.Cut(p, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "chunk-signature") {
			sig = strings.TrimSpace(val)
		}
	}
	if sig == "" {
		return 0, nil, ErrBodySignature
	}
	raw, err := hex.DecodeString(sig)
	if err != nil {
		return 0, nil, ErrBodySignature
	}
	return size, raw, nil
}

func (v *chunkedVerifier) expectCRLF() error {
	b := make([]byte, 2)
	if _, err := io.ReadFull(v.br, b); err != nil {
		return ErrBodySignature
	}
	if b[0] != '\r' || b[1] != '\n' {
		return ErrBodySignature
	}
	return nil
}

func (v *chunkedVerifier) drainTrailers() error {
	for {
		line, err := v.br.ReadString('\n')
		if err == io.EOF && strings.TrimRight(line, "\r\n") == "" {
			return nil
		}
		if err != nil {
			return ErrBodySignature
		}
		if strings.TrimRight(line, "\r\n") == "" {
			return nil
		}
	}
}
