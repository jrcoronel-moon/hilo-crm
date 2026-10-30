package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jrcoronel-moon/hilo-crm/internal/events"
)

// Live updates: GET /events streams every change in the current workspace as
// server-sent events, so the open pages refresh while agents work.

const (
	heartbeat  = 25 * time.Second // keeps proxies from closing an idle stream
	writeGrace = 10 * time.Second // a write that takes longer drops the stream
)

func (h *Handler) live(w http.ResponseWriter, r *http.Request, v view) {
	if h.hub == nil || v.Current == nil {
		http.NotFound(w, r)
		return
	}
	sub, err := h.hub.Subscribe(v.Current.ID, v.Sess.UserID)
	if errors.Is(err, events.ErrTooMany) {
		http.Error(w, "too many live connections", http.StatusTooManyRequests)
		return
	}
	defer sub.Close()

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(chunk string) bool {
		// The server's WriteTimeout would cut the stream; each write gets its own.
		if rc.SetWriteDeadline(time.Now().Add(writeGrace)) != nil {
			return false
		}
		if _, err := fmt.Fprint(w, chunk); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send("retry: 3000\n\n") {
		return
	}
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.done:
			return
		case <-tick.C:
			if !send(": ping\n\n") {
				return
			}
		case e := <-sub.C:
			e.Self = e.By(v.Sess.UserID)
			b, err := json.Marshal(e)
			if err != nil || !send(fmt.Sprintf("id: %d\nevent: change\ndata: %s\n\n", e.ID, b)) {
				return
			}
		}
	}
}
