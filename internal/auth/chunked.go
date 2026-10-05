package auth

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/guoyyit/s3-object-gateway-go/internal/protocol"
)

// chunkedReader verifies and decodes an aws-chunked request body
// (x-amz-content-sha256: STREAMING-AWS4-HMAC-SHA256-PAYLOAD).
//
// Wire format per chunk:
//
//	<hex-size>[;chunk-signature=<sig>]\r\n
//	<data>\r\n
//
// terminated by a zero-size chunk.
type chunkedReader struct {
	br      *bufio.Reader
	key     []byte
	sts     string
	prevSig string
	pending []byte
	done    bool
}

func newChunkedReader(src io.Reader, signingKey []byte, sts, seedSignature string) (*chunkedReader, error) {
	return &chunkedReader{
		br:      bufio.NewReaderSize(src, 64*1024),
		key:     signingKey,
		sts:     sts,
		prevSig: seedSignature,
	}, nil
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if len(c.pending) == 0 && !c.done {
		if err := c.loadChunk(); err != nil {
			return 0, err
		}
	}
	if len(c.pending) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *chunkedReader) Close() error { return nil }

func (c *chunkedReader) loadChunk() error {
	line, err := c.br.ReadString('\n')
	if err != nil {
		return protocol.ErrContentSHA256Mismatch
	}
	line = strings.TrimSpace(line)
	sizeStr := line
	var givenSig string
	if i := strings.IndexByte(line, ';'); i >= 0 {
		sizeStr = line[:i]
		params := strings.Split(line[i+1:], ";")
		for _, p := range params {
			if strings.HasPrefix(p, "chunk-signature=") {
				givenSig = strings.TrimPrefix(p, "chunk-signature=")
			}
		}
	}
	size, err := strconv.ParseInt(sizeStr, 16, 64)
	if err != nil || size < 0 {
		return protocol.ErrContentSHA256Mismatch
	}

	data := make([]byte, size)
	if _, err := io.ReadFull(c.br, data); err != nil {
		return protocol.ErrContentSHA256Mismatch
	}
	trailer := make([]byte, 2)
	if _, err := io.ReadFull(c.br, trailer); err != nil || string(trailer) != "\r\n" {
		return protocol.ErrContentSHA256Mismatch
	}

	expected := c.chunkSignature(data)
	if givenSig == "" || !hmac.Equal([]byte(expected), []byte(givenSig)) {
		return protocol.ErrContentSHA256Mismatch
	}
	c.prevSig = givenSig

	if size == 0 {
		c.done = true
		return nil
	}
	c.pending = data
	return nil
}

// chunkSignature computes:
// HMAC-SHA256(signingKey, "AWS4-HMAC-SHA256-PAYLOAD\n" + sts + "\n" +
// prevSignature + "\n" + sha256(data) + "\n").
func (c *chunkedReader) chunkSignature(data []byte) string {
	sum := sha256.Sum256(data)
	msg := fmt.Sprintf("AWS4-HMAC-SHA256-PAYLOAD\n%s\n%s\n%s\n", c.sts, c.prevSig, hex.EncodeToString(sum[:]))
	m := hmac.New(sha256.New, c.key)
	_, _ = m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}
