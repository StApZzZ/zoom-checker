package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"zoom-checker/internal/realtime"
	"zoom-checker/internal/store"
)

// Server-Sent Events (docs/architecture.md §5.8).
//
// Broker notifications carry no payload: a stream re-reads state when poked.
// Pokes arriving close together are coalesced (the first one schedules a send
// after the debounce delay, later ones ride along), so a burst of webhooks costs
// one snapshot and the latency stays bounded by the delay.
//
// Streams end when the client disconnects, the request context is cancelled or
// the login ends (checked every SessionCheck).
// For a graceful shutdown the HTTP server should cancel request contexts (e.g.
// http.Server.BaseContext cancelled from RegisterOnShutdown); otherwise
// Shutdown waits for open streams until its own deadline.

// lessonSSETimings are the stream timings; tests shorten them.
type lessonSSETimings struct {
	SnapshotDebounce time.Duration // lesson stream: delay after a poke
	ListDebounce     time.Duration // global stream: delay after a poke
	Ping             time.Duration // comment line keeping proxies from closing idle streams
	Refresh          time.Duration // lesson stream: snapshot without pokes (safety net)
	SessionCheck     time.Duration // re-validate the login (logout / disabled user ends the stream)
	Retry            time.Duration // client reconnect delay advertised with "retry:"
}

var lessonSSE = lessonSSETimings{
	SnapshotDebounce: 150 * time.Millisecond,
	ListDebounce:     500 * time.Millisecond,
	Ping:             20 * time.Second,
	Refresh:          60 * time.Second,
	SessionCheck:     60 * time.Second,
	Retry:            3 * time.Second,
}

// lessonSSESessionGone reports whether the stream's login ended (logout, expiry,
// disabled account). Database errors do not end the stream. It only validates:
// an open stream must not keep the session alive.
func (s *Server) lessonSSESessionGone(r *http.Request) bool {
	_, _, err := s.validateSession(r)
	return errors.Is(err, errNoSession)
}

// lessonSSEWriter writes SSE frames and flushes after each one.
type lessonSSEWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

// startLessonSSE sends the stream headers. The write and read deadlines are
// disabled so server-wide timeouts do not cut long-lived streams.
func startLessonSSE(w http.ResponseWriter) *lessonSSEWriter {
	rc := http.NewResponseController(w)
	// ErrNotSupported on exotic writers: nothing to disable then. The read
	// deadline matters too: when it fires, net/http cancels the request context.
	_ = rc.SetWriteDeadline(time.Time{})
	_ = rc.SetReadDeadline(time.Time{})
	lessonSSEHeaders(w.Header())
	w.WriteHeader(http.StatusOK)
	return &lessonSSEWriter{w: w, rc: rc}
}

func lessonSSEHeaders(h http.Header) {
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
}

// lessonSSEHead answers a HEAD request to a stream with the headers only. The
// GET patterns also match HEAD, and net/http discards the body of a HEAD
// response, so a streaming loop would hold the connection for nothing.
func lessonSSEHead(w http.ResponseWriter) {
	lessonSSEHeaders(w.Header())
	w.WriteHeader(http.StatusOK)
}

func (sw *lessonSSEWriter) write(b []byte) error {
	if _, err := sw.w.Write(b); err != nil {
		return err
	}
	if err := sw.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

// event writes "event: <name>\ndata: <json>\n\n" (JSON never contains raw newlines).
func (sw *lessonSSEWriter) event(name string, data []byte) error {
	var b bytes.Buffer
	b.Grow(len(name) + len(data) + 16)
	b.WriteString("event: ")
	b.WriteString(name)
	b.WriteString("\ndata: ")
	b.Write(data)
	b.WriteString("\n\n")
	return sw.write(b.Bytes())
}

func (sw *lessonSSEWriter) comment(text string) error { return sw.write([]byte(": " + text + "\n\n")) }

func (sw *lessonSSEWriter) retry(d time.Duration) error {
	return sw.write([]byte("retry: " + strconv.FormatInt(d.Milliseconds(), 10) + "\n\n"))
}

// handleLessonStream: GET /api/lessons/{id}/stream — "snapshot" events carrying
// the LessonView. The stream closes when the lesson is deleted.
func (s *Server) handleLessonStream(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	if r.Method == http.MethodHead {
		if _, err := s.Store.GetLesson(ctx, id); err != nil {
			return storeErr(err, lessonNotFoundMsg)
		}
		lessonSSEHead(w)
		return nil
	}
	// Subscribe before the first snapshot so no change between them is lost.
	var poke <-chan struct{}
	if s.Broker != nil {
		ch, cancel := s.Broker.Subscribe(realtime.LessonTopic(id))
		defer cancel()
		poke = ch
	}
	snapshot := func() ([]byte, error) {
		v, err := s.buildLessonView(ctx, id, s.lessonNow())
		if err != nil {
			return nil, err
		}
		return json.Marshal(v)
	}
	first, err := snapshot()
	if err != nil {
		return storeErr(err, lessonNotFoundMsg) // headers not sent yet: a normal JSON error
	}
	sw := startLessonSSE(w)
	if err := sw.retry(lessonSSE.Retry); err != nil {
		return nil
	}
	if err := sw.event("snapshot", first); err != nil {
		return nil
	}

	t := lessonSSE
	ping := time.NewTicker(t.Ping)
	defer ping.Stop()
	recheck := time.NewTicker(t.SessionCheck)
	defer recheck.Stop()
	refresh := time.NewTimer(t.Refresh)
	defer refresh.Stop()
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	defer debounce.Stop()
	pending := false

	send := func() bool {
		data, err := snapshot()
		if errors.Is(err, store.ErrNotFound) {
			return false // lesson deleted: end the stream
		}
		refresh.Reset(t.Refresh) // also after a failure, so the safety net keeps running
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			s.Log.Warn("lesson stream snapshot failed", "lesson_id", id, "err", err)
			return true // transient: the next poke or refresh tries again
		}
		return sw.event("snapshot", data) == nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poke:
			if !pending {
				pending = true
				debounce.Reset(t.SnapshotDebounce)
			}
		case <-debounce.C:
			pending = false
			if !send() {
				return nil
			}
		case <-refresh.C:
			if !send() {
				return nil
			}
		case <-ping.C:
			if sw.comment("ping") != nil {
				return nil
			}
		case <-recheck.C:
			if s.lessonSSESessionGone(r) {
				return nil
			}
		}
	}
}

// handleLessonsStream: GET /api/stream — "lessons" events (empty JSON object)
// whenever any lesson changes; dashboards then refetch their lists.
func (s *Server) handleLessonsStream(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if r.Method == http.MethodHead {
		lessonSSEHead(w)
		return nil
	}
	var poke <-chan struct{}
	if s.Broker != nil {
		ch, cancel := s.Broker.Subscribe(realtime.TopicLessons)
		defer cancel()
		poke = ch
	}
	sw := startLessonSSE(w)
	if err := sw.retry(lessonSSE.Retry); err != nil {
		return nil
	}
	t := lessonSSE
	ping := time.NewTicker(t.Ping)
	defer ping.Stop()
	recheck := time.NewTicker(t.SessionCheck)
	defer recheck.Stop()
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	defer debounce.Stop()
	pending := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poke:
			if !pending {
				pending = true
				debounce.Reset(t.ListDebounce)
			}
		case <-debounce.C:
			pending = false
			if sw.event("lessons", []byte("{}")) != nil {
				return nil
			}
		case <-ping.C:
			if sw.comment("ping") != nil {
				return nil
			}
		case <-recheck.C:
			if s.lessonSSESessionGone(r) {
				return nil
			}
		}
	}
}
