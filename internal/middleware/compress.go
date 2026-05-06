package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

var gzipWriterPool = sync.Pool{
	New: func() any {
		return gzip.NewWriter(io.Discard)
	},
}

// compressibleContentTypes lists prefixes we bother compressing. Skipping
// binary formats avoids wasting CPU on already-compressed bytes (images,
// archives, video).
var compressibleContentTypes = []string{
	"text/",
	"application/json",
	"application/xml",
	"application/javascript",
	"application/x-www-form-urlencoded",
}

type compressWriter struct {
	http.ResponseWriter
	gz         *gzip.Writer
	minSize    int
	wroteHead  bool
	compress   bool
	buf        []byte
	statusCode int
}

// WriteHeader defers the actual header write until we see enough body to
// decide whether compression is worth it.
func (cw *compressWriter) WriteHeader(code int) {
	cw.statusCode = code
}

func (cw *compressWriter) Write(b []byte) (int, error) {
	if cw.wroteHead {
		if cw.compress {
			return cw.gz.Write(b)
		}
		return cw.ResponseWriter.Write(b)
	}

	cw.buf = append(cw.buf, b...)
	if len(cw.buf) < cw.minSize {
		return len(b), nil
	}

	cw.decide()
	cw.flushBuffered()
	return len(b), nil
}

// decide inspects buffered state and headers to choose compression or passthrough.
// Must be called exactly once per response, before any bytes go to the wire.
func (cw *compressWriter) decide() {
	header := cw.Header()
	if header.Get("Content-Encoding") != "" {
		cw.compress = false
	} else if !isCompressibleType(header.Get("Content-Type"), cw.buf) {
		cw.compress = false
	} else {
		cw.compress = true
	}

	if cw.compress {
		header.Set("Content-Encoding", "gzip")
		header.Del("Content-Length")
	}
}

func (cw *compressWriter) flushBuffered() {
	status := cw.statusCode
	if status == 0 {
		status = http.StatusOK
	}
	cw.ResponseWriter.WriteHeader(status)
	cw.wroteHead = true

	if cw.compress {
		cw.gz.Reset(cw.ResponseWriter)
		_, _ = cw.gz.Write(cw.buf)
	} else {
		_, _ = cw.ResponseWriter.Write(cw.buf)
	}
	cw.buf = nil
}

// finish flushes any buffered body that never crossed the min-size threshold
// and closes the gzip writer if compression was used.
func (cw *compressWriter) finish() error {
	if !cw.wroteHead {
		cw.compress = false
		cw.flushBuffered()
	}
	if cw.compress {
		return cw.gz.Close()
	}
	return nil
}

// Compress returns a middleware that gzip-encodes responses when the client
// advertises gzip support, the response body is at least minSize bytes, the
// content type is compressible, and the upstream has not already set
// Content-Encoding. Vary: Accept-Encoding is always set so caches key correctly.
func Compress(minSize int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Vary", "Accept-Encoding")

			if !clientAcceptsGzip(r.Header.Get("Accept-Encoding")) {
				next.ServeHTTP(w, r)
				return
			}

			gz := gzipWriterPool.Get().(*gzip.Writer)
			defer gzipWriterPool.Put(gz)

			cw := &compressWriter{
				ResponseWriter: w,
				gz:             gz,
				minSize:        minSize,
			}
			next.ServeHTTP(cw, r)
			_ = cw.finish()
		})
	}
}

func clientAcceptsGzip(header string) bool {
	for part := range strings.SplitSeq(header, ",") {
		token := strings.TrimSpace(part)
		name, _, _ := strings.Cut(token, ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") {
			return true
		}
	}
	return false
}

func isCompressibleType(contentType string, sniff []byte) bool {
	if contentType == "" {
		contentType = http.DetectContentType(sniff)
	}
	ct, _, _ := strings.Cut(contentType, ";")
	ct = strings.TrimSpace(ct)
	for _, prefix := range compressibleContentTypes {
		if strings.HasPrefix(ct, prefix) {
			return true
		}
	}
	return false
}
