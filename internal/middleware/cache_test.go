package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go-api-gateway/internal/cache"
	"go-api-gateway/internal/middleware"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cachedJSONHandler(body string, hits *int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	})
}

func TestCache_HitServesFromCacheWithoutHittingUpstream(t *testing.T) {
	c := cache.NewLRU(10, 1<<20)
	var hits int32
	handler := middleware.Cache(c, middleware.CacheConfig{TTL: time.Minute})(cachedJSONHandler("payload", &hits))

	for range 3 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/test", nil)
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "payload", rec.Body.String())
	}

	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
}

func TestCache_BypassesNonGetMethods(t *testing.T) {
	c := cache.NewLRU(10, 1<<20)
	var hits int32
	handler := middleware.Cache(c, middleware.CacheConfig{TTL: time.Minute})(cachedJSONHandler("payload", &hits))

	for range 3 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/test", nil)
		handler.ServeHTTP(rec, req)
	}

	assert.Equal(t, int32(3), atomic.LoadInt32(&hits))
	assert.Equal(t, 0, c.Len())
}

func TestCache_RespectsResponseNoStore(t *testing.T) {
	c := cache.NewLRU(10, 1<<20)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("private"))
	})
	handler := middleware.Cache(c, middleware.CacheConfig{TTL: time.Minute})(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/test", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, "private", rec.Body.String())
	assert.Equal(t, 0, c.Len())
}

func TestCache_HonorsResponseMaxAge(t *testing.T) {
	c := cache.NewLRU(10, 1<<20)
	var hits int32
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Cache-Control", "max-age=1")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("payload"))
	})
	// Config TTL is generous so the response's max-age must override it.
	handler := middleware.Cache(c, middleware.CacheConfig{TTL: time.Hour})(inner)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/test", nil))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/test", nil))
	require.Equal(t, int32(1), atomic.LoadInt32(&hits), "second request must hit cache before TTL elapses")

	time.Sleep(1100 * time.Millisecond)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/test", nil))

	assert.Equal(t, int32(2), atomic.LoadInt32(&hits), "request after max-age elapsed must miss and re-fetch")
}

func TestCache_RequestNoStoreBypassesCacheEntirely(t *testing.T) {
	c := cache.NewLRU(10, 1<<20)
	var hits int32
	handler := middleware.Cache(c, middleware.CacheConfig{TTL: time.Minute})(cachedJSONHandler("payload", &hits))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Cache-Control", "no-store")
	handler.ServeHTTP(rec, req)

	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
	assert.Equal(t, 0, c.Len())
}

func TestCache_RequestNoCacheBypassesLookupButStillStores(t *testing.T) {
	c := cache.NewLRU(10, 1<<20)
	var hits int32
	handler := middleware.Cache(c, middleware.CacheConfig{TTL: time.Minute})(cachedJSONHandler("payload", &hits))

	recFirst := httptest.NewRecorder()
	reqFirst := httptest.NewRequest("GET", "/test", nil)
	handler.ServeHTTP(recFirst, reqFirst)

	recSecond := httptest.NewRecorder()
	reqSecond := httptest.NewRequest("GET", "/test", nil)
	reqSecond.Header.Set("Cache-Control", "no-cache")
	handler.ServeHTTP(recSecond, reqSecond)

	assert.Equal(t, int32(2), atomic.LoadInt32(&hits))
	assert.Equal(t, 1, c.Len())
}

func TestCache_ETagConditionalReturns304(t *testing.T) {
	c := cache.NewLRU(10, 1<<20)
	handler := middleware.Cache(c, middleware.CacheConfig{TTL: time.Minute})(cachedJSONHandler("payload", nil))

	recFirst := httptest.NewRecorder()
	reqFirst := httptest.NewRequest("GET", "/test", nil)
	handler.ServeHTTP(recFirst, reqFirst)
	etag := recFirst.Header().Get("ETag")
	require.NotEmpty(t, etag, "first response must carry a generated ETag")

	recSecond := httptest.NewRecorder()
	reqSecond := httptest.NewRequest("GET", "/test", nil)
	reqSecond.Header.Set("If-None-Match", etag)
	handler.ServeHTTP(recSecond, reqSecond)

	assert.Equal(t, http.StatusNotModified, recSecond.Code)
	assert.Empty(t, recSecond.Body.String(), "304 must not carry a body")
	assert.Equal(t, etag, recSecond.Header().Get("ETag"))
}

func TestCache_VaryProducesSeparateEntries(t *testing.T) {
	c := cache.NewLRU(10, 1<<20)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Accept-Language")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("lang=" + r.Header.Get("Accept-Language")))
	})
	handler := middleware.Cache(c, middleware.CacheConfig{TTL: time.Minute})(inner)

	langs := []string{"en", "fr"}
	for _, lang := range langs {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Accept-Language", lang)
		handler.ServeHTTP(rec, req)
		assert.Equal(t, "lang="+lang, rec.Body.String())
	}

	assert.Equal(t, len(langs), c.Len())
}

func TestCache_VaryAnyIsNotCached(t *testing.T) {
	c := cache.NewLRU(10, 1<<20)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "*")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("payload"))
	})
	handler := middleware.Cache(c, middleware.CacheConfig{TTL: time.Minute})(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, "payload", rec.Body.String())
	assert.Equal(t, 0, c.Len())
}

func TestCache_AuthorizedRequestSkippedUnlessPublic(t *testing.T) {
	c := cache.NewLRU(10, 1<<20)
	handler := middleware.Cache(c, middleware.CacheConfig{TTL: time.Minute})(cachedJSONHandler("private", nil))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer xyz")
	handler.ServeHTTP(rec, req)

	assert.Equal(t, 0, c.Len())
}

func TestCache_AuthorizedRequestCachedWhenPublic(t *testing.T) {
	c := cache.NewLRU(10, 1<<20)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("shared"))
	})
	handler := middleware.Cache(c, middleware.CacheConfig{TTL: time.Minute})(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer xyz")
	handler.ServeHTTP(rec, req)

	assert.Equal(t, 1, c.Len())
}

func TestCache_NonOKResponseNotCached(t *testing.T) {
	c := cache.NewLRU(10, 1<<20)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("missing"))
	})
	handler := middleware.Cache(c, middleware.CacheConfig{TTL: time.Minute})(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "missing", rec.Body.String())
	assert.Equal(t, 0, c.Len())
}

func TestCache_QueryParamOrderProducesSameKey(t *testing.T) {
	c := cache.NewLRU(10, 1<<20)
	var hits int32
	handler := middleware.Cache(c, middleware.CacheConfig{TTL: time.Minute})(cachedJSONHandler("payload", &hits))

	for _, qs := range []string{"?a=1&b=2", "?b=2&a=1"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/test"+qs, nil)
		handler.ServeHTTP(rec, req)
		assert.Equal(t, "payload", rec.Body.String())
	}

	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
}

func TestCache_SingleflightCollapsesConcurrentMisses(t *testing.T) {
	c := cache.NewLRU(10, 1<<20)
	var hits int32
	gate := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		<-gate
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("payload"))
	})
	handler := middleware.Cache(c, middleware.CacheConfig{TTL: time.Minute})(inner)

	const N = 20
	var wg sync.WaitGroup
	for range N {
		wg.Go(func() {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/test", nil)
			handler.ServeHTTP(rec, req)
			assert.Equal(t, "payload", rec.Body.String())
		})
	}

	// Give all goroutines time to enter sf.Do before releasing the upstream.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
}
