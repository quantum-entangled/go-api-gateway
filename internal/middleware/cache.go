package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"go-api-gateway/internal/cache"
)

var captureBufPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

// CacheConfig configures the response cache middleware. TTL is the default
// applied when the upstream response has no Cache-Control max-age.
type CacheConfig struct {
	TTL        time.Duration
	MaxEntries int
	MaxBytes   int
}

// varyState remembers the Vary header list each (method, path) last
// advertised. Lookups consult it to build the right Vary-aware cache key
// before the response has been seen, and stores update it after capture.
type varyState struct {
	mu sync.RWMutex
	m  map[string][]string
}

func newVaryState() *varyState {
	return &varyState{m: make(map[string][]string)}
}

func (v *varyState) headersFor(method, path string) []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.m[method+" "+path]
}

func (v *varyState) record(method, path string, headers []string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.m[method+" "+path] = headers
}

// captureWriter buffers the inner handler's response so the middleware can
// decide, after the handler finishes, whether the response is cacheable.
type captureWriter struct {
	http.ResponseWriter
	status int
	header http.Header
	body   *bytes.Buffer
}

func (cw *captureWriter) Header() http.Header {
	return cw.header
}

func (cw *captureWriter) WriteHeader(code int) {
	cw.status = code
}

func (cw *captureWriter) Write(b []byte) (int, error) {
	if cw.status == 0 {
		cw.status = http.StatusOK
	}
	return cw.body.Write(b)
}

// Cache returns a middleware that serves cacheable GET/HEAD responses from an
// in-memory LRU. It honors request and response Cache-Control directives,
// keys entries by method+path+query+Vary header values, generates ETags for
// stored bodies, answers conditional requests with 304, and uses singleflight
// to prevent stampedes on cache misses.
func Cache(c *cache.LRU, cfg CacheConfig) func(http.Handler) http.Handler {
	var sf singleflight.Group
	vary := newVaryState()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				next.ServeHTTP(w, r)
				return
			}
			reqCC := parseCacheControl(r.Header.Get("Cache-Control"))
			if _, noStore := reqCC["no-store"]; noStore {
				next.ServeHTTP(w, r)
				return
			}

			key := buildKey(r, vary.headersFor(r.Method, r.URL.Path))
			_, bypassLookup := reqCC["no-cache"]
			if !bypassLookup {
				if entry, ok := c.Get(key); ok {
					serveFromCache(w, r, entry)
					return
				}
			}

			// Singleflight collapses concurrent misses for the same key into
			// one upstream call. Followers receive the same captured response.
			result, _, _ := sf.Do(key, func() (any, error) {
				return fillCache(next, c, cfg, r, vary), nil
			})

			res := result.(*sfResult)
			if res.entry != nil {
				serveFromCache(w, r, res.entry)
				return
			}

			// Uncacheable response: replay the captured bytes once.
			writeRaw(w, res.status, res.header, res.body)
		})
	}
}

type sfResult struct {
	entry  *cache.Entry
	status int
	header http.Header
	body   []byte
}

func fillCache(
	next http.Handler,
	c *cache.LRU,
	cfg CacheConfig,
	r *http.Request,
	vary *varyState,
) *sfResult {
	buf := captureBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer captureBufPool.Put(buf)

	cw := &captureWriter{
		ResponseWriter: nil,
		header:         make(http.Header),
		body:           buf,
	}
	next.ServeHTTP(cw, r)

	var bodyCopy []byte
	bodyCopy = append(bodyCopy, cw.body.Bytes()...)
	respCC := parseCacheControl(cw.header.Get("Cache-Control"))
	varyHeaders, varyAny := parseVary(cw.header.Get("Vary"))

	if !cacheableResponse(cw.status, respCC, varyAny, r) {
		return &sfResult{status: cw.status, header: cw.header, body: bodyCopy}
	}

	ttl := cfg.TTL
	if maxAge, ok := respCC["max-age"]; ok {
		if secs, err := strconv.Atoi(maxAge); err == nil && secs > 0 {
			ttl = time.Duration(secs) * time.Second
		}
	}

	vary.record(r.Method, r.URL.Path, varyHeaders)
	finalKey := buildKey(r, varyHeaders)
	etag := cw.header.Get("ETag")
	if etag == "" {
		etag = computeETag(bodyCopy)
		cw.header.Set("ETag", etag)
	}

	entry := &cache.Entry{
		Status:    cw.status,
		Header:    cloneHeader(cw.header),
		Body:      bodyCopy,
		ETag:      etag,
		ExpiresAt: time.Now().Add(ttl),
	}
	c.Set(finalKey, entry)

	return &sfResult{entry: entry}
}

func serveFromCache(w http.ResponseWriter, r *http.Request, entry *cache.Entry) {
	if matched := r.Header.Get("If-None-Match"); matched != "" && etagMatches(matched, entry.ETag) {
		for k, vs := range entry.Header {
			if k == "Content-Length" || k == "Content-Type" {
				continue
			}
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("ETag", entry.ETag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeRaw(w, entry.Status, entry.Header, entry.Body)
}

func writeRaw(w http.ResponseWriter, status int, header http.Header, body []byte) {
	for k, vs := range header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// cacheableResponse decides whether a response is allowed in the cache.
// We only cache 200s, refuse Cache-Control no-store/private, refuse
// Vary: * (unenumerable selecting headers), and refuse responses to
// authenticated requests unless the response opts in via public.
func cacheableResponse(status int, cc map[string]string, varyAny bool, r *http.Request) bool {
	if status != http.StatusOK {
		return false
	}
	if _, ok := cc["no-store"]; ok {
		return false
	}
	if _, ok := cc["private"]; ok {
		return false
	}
	if varyAny {
		return false
	}
	if r.Header.Get("Authorization") != "" {
		if _, ok := cc["public"]; !ok {
			return false
		}
	}
	return true
}

// buildKey combines method, path, sorted query, and the request's values for
// any Vary headers the upstream advertised.
func buildKey(r *http.Request, varyHeaders []string) string {
	var b strings.Builder
	b.WriteString(r.Method)
	b.WriteByte(' ')
	b.WriteString(r.URL.Path)
	b.WriteByte('?')

	q := r.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		vs := q[k]
		sort.Strings(vs)
		for _, v := range vs {
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(v)
			b.WriteByte('&')
		}
	}

	for _, h := range varyHeaders {
		b.WriteByte('|')
		b.WriteString(strings.ToLower(h))
		b.WriteByte('=')
		b.WriteString(r.Header.Get(h))
	}

	return b.String()
}

// parseVary returns the list of header names from a Vary header. The second
// return is true when the header contains "*", which means "varies on
// something not in the request headers" - such responses are uncacheable.
func parseVary(vary string) ([]string, bool) {
	if vary == "" {
		return nil, false
	}
	var out []string
	for part := range strings.SplitSeq(vary, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if p == "*" {
			return nil, true
		}
		out = append(out, p)
	}
	return out, false
}

func parseCacheControl(value string) map[string]string {
	out := make(map[string]string)
	if value == "" {
		return out
	}
	for part := range strings.SplitSeq(value, ",") {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		name, val, _ := strings.Cut(token, "=")
		out[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(val)
	}
	return out
}

func cloneHeader(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, vs := range h {
		copied := make([]string, len(vs))
		copy(copied, vs)
		out[k] = copied
	}
	return out
}

func computeETag(body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf(`"%s"`, hex.EncodeToString(sum[:16]))
}

// etagMatches implements a relaxed match against If-None-Match: a
// comma-separated list of quoted tags or "*". The weak prefix (W/) is
// stripped from both sides so an upstream-issued W/"x" matches a client's
// "x" and vice versa.
func etagMatches(ifNoneMatch, etag string) bool {
	if etag == "" {
		return false
	}
	stored := strings.TrimPrefix(etag, "W/")
	for part := range strings.SplitSeq(ifNoneMatch, ",") {
		token := strings.TrimSpace(part)
		token = strings.TrimPrefix(token, "W/")
		if token == "*" || token == stored {
			return true
		}
	}
	return false
}
