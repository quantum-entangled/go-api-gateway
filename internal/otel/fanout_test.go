package otel

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingHandler struct {
	enabled bool
	records []slog.Record
	attrs   []slog.Attr
	group   string
	err     error
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool {
	return h.enabled
}

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return h.err
}

func (h *recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append([]slog.Attr{}, h.attrs...)
	clone.attrs = append(clone.attrs, attrs...)
	return &clone
}

func (h *recordingHandler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.group = name
	return &clone
}

type mutatingHandler func(context.Context, slog.Record) error

func (m mutatingHandler) Enabled(context.Context, slog.Level) bool        { return true }
func (m mutatingHandler) Handle(ctx context.Context, r slog.Record) error { return m(ctx, r) }
func (m mutatingHandler) WithAttrs([]slog.Attr) slog.Handler              { return m }
func (m mutatingHandler) WithGroup(string) slog.Handler                   { return m }

func TestFanoutHandler_HandleForwardsToAllEnabledHandlers(t *testing.T) {
	a := &recordingHandler{enabled: true}
	b := &recordingHandler{enabled: true}
	f := fanoutHandler{handlers: []slog.Handler{a, b}}

	rec := slog.NewRecord(time.Time{}, slog.LevelInfo, "hello", 0)
	require.NoError(t, f.Handle(context.Background(), rec))

	assert.Len(t, a.records, 1)
	assert.Len(t, b.records, 1)
}

func TestFanoutHandler_HandleSkipsDisabledHandler(t *testing.T) {
	enabled := &recordingHandler{enabled: true}
	disabled := &recordingHandler{enabled: false}
	f := fanoutHandler{handlers: []slog.Handler{enabled, disabled}}

	rec := slog.NewRecord(time.Time{}, slog.LevelInfo, "hello", 0)
	require.NoError(t, f.Handle(context.Background(), rec))

	assert.Len(t, enabled.records, 1)
	assert.Empty(t, disabled.records)
}

func TestFanoutHandler_HandleStopsOnFirstError(t *testing.T) {
	boom := errors.New("boom")
	first := &recordingHandler{enabled: true, err: boom}
	second := &recordingHandler{enabled: true}
	f := fanoutHandler{handlers: []slog.Handler{first, second}}

	rec := slog.NewRecord(time.Time{}, slog.LevelInfo, "hello", 0)
	err := f.Handle(context.Background(), rec)

	assert.ErrorIs(t, err, boom)
	assert.Empty(t, second.records)
}

func TestFanoutHandler_EnabledIsOrAcrossHandlers(t *testing.T) {
	cases := []struct {
		name     string
		handlers []slog.Handler
		want     bool
	}{
		{"none enabled", []slog.Handler{&recordingHandler{}, &recordingHandler{}}, false},
		{"one enabled", []slog.Handler{&recordingHandler{}, &recordingHandler{enabled: true}}, true},
		{"all enabled", []slog.Handler{&recordingHandler{enabled: true}, &recordingHandler{enabled: true}}, true},
		{"empty", []slog.Handler{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fanoutHandler{handlers: tc.handlers}
			assert.Equal(t, tc.want, f.Enabled(context.Background(), slog.LevelInfo))
		})
	}
}

func TestFanoutHandler_WithAttrsPropagatesAndDoesNotMutateOriginal(t *testing.T) {
	a := &recordingHandler{enabled: true}
	b := &recordingHandler{enabled: true}
	original := fanoutHandler{handlers: []slog.Handler{a, b}}
	derived := original.WithAttrs([]slog.Attr{slog.String("svc", "gateway")}).(fanoutHandler)

	for _, h := range derived.handlers {
		handler := h.(*recordingHandler)
		require.Len(t, handler.attrs, 1)
		assert.Equal(t, "svc", handler.attrs[0].Key)
	}

	assert.Empty(t, a.attrs)
	assert.Empty(t, b.attrs)
}

func TestFanoutHandler_WithGroupPropagatesAndDoesNotMutateOriginal(t *testing.T) {
	a := &recordingHandler{enabled: true}
	b := &recordingHandler{enabled: true}
	original := fanoutHandler{handlers: []slog.Handler{a, b}}
	derived := original.WithGroup("derived").(fanoutHandler)

	for _, h := range derived.handlers {
		assert.Equal(t, "derived", h.(*recordingHandler).group)
	}

	assert.Empty(t, a.group)
	assert.Empty(t, b.group)
}

func TestFanoutHandler_HandleClonesRecord(t *testing.T) {
	mutator := mutatingHandler(func(_ context.Context, r slog.Record) error {
		r.AddAttrs(slog.String("injected", "by mutator"))
		return nil
	})
	observer := &recordingHandler{enabled: true}
	f := fanoutHandler{handlers: []slog.Handler{mutator, observer}}

	rec := slog.NewRecord(time.Time{}, slog.LevelInfo, "hello", 0)
	require.NoError(t, f.Handle(context.Background(), rec))

	require.Len(t, observer.records, 1)
	observer.records[0].Attrs(func(a slog.Attr) bool {
		assert.NotEqual(t, "injected", a.Key, "mutation in sibling handler leaked")
		return true
	})
}
