package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// streamJobs is a Server-Sent Events endpoint that forwards worker events to
// the browser so the UI can show live job progress without polling.
func (s *Server) streamJobs(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// EventSource replays the last id it saw in this header automatically on
	// reconnect. The id is "<epoch>-<seq>": the sequence alone is meaningless
	// across a server restart, which renumbers from zero. An absent header or a
	// parse failure both mean "start fresh" (0).
	lastEpoch, lastSeqRaw, _ := strings.Cut(r.Header.Get("Last-Event-ID"), "-")
	lastSeq, err := strconv.ParseUint(lastSeqRaw, 10, 64)
	if err != nil {
		lastEpoch, lastSeq = "", 0
	}

	events, unsubscribe, gap := s.Worker.Subscribe(lastEpoch, lastSeq)
	defer unsubscribe()

	// Prime the connection so proxies flush headers.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	// The replay window this client asked for has already been evicted, so the
	// events it missed can't be resent individually. Tell it to refetch state.
	if gap {
		fmt.Fprint(w, "event: reconcile\ndata: {}\n\n")
		flusher.Flush()
	}

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.shutdown:
			// Server is shutting down; end the stream so http.Server.Shutdown
			// doesn't block on this long-lived request until its timeout.
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			// The id: line is what the browser persists and returns as
			// Last-Event-ID on reconnect, letting Subscribe replay the gap. The
			// epoch prefix is what lets Subscribe tell a resumable reconnect
			// from one carrying sequence numbers a previous process issued.
			fmt.Fprintf(w, "id: %s-%d\nevent: job\ndata: %s\n\n", s.Worker.Epoch(), ev.Seq, b)
			flusher.Flush()
		}
	}
}
