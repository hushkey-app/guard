package deploy

// Watching a deploy happen, as it happens.
//
// The row in the database is still the truth — it is what a page reloading
// mid-deploy reads, what a second person sees, and what is there tomorrow. This
// is the fast path on top of it: the same output, pushed to whoever is looking
// right now, so `docker compose pull` reads like a terminal rather than like a
// progress bar that jumps once a second.
//
// Two rules make it safe to have both:
//
//   - **Nothing here is authoritative.** A frame is a copy of what was just
//     written to the row. Lose every frame and the deploy is unaffected and the
//     page catches up on its next tick — which is exactly what happens to a tab
//     that was closed, a proxy that dropped the connection, or a browser that
//     never opened one.
//   - **A slow watcher is never a slow deploy.** Sends are non-blocking and
//     coalescing: a listener that is behind gets the newest frame and misses the
//     ones in between, because the newest frame contains all the output anyway.
//     The SSH reader must never wait on a socket in somebody's browser.

// Frame is one update about one machine.
type Frame struct {
	RunID int64 `json:"run_id"`
	// NodeID is the machine, and is what a preparation frame carries instead of
	// a run — installing docker is not a deploy, but it is the same "a long
	// command is talking" problem and deserves the same pane.
	NodeID int64  `json:"node_id"`
	Kind   string `json:"kind"`
	Status string `json:"status,omitempty"`
	Output string `json:"output,omitempty"`
	// Done marks the last frame a watcher will get for this subject, so a page
	// can close the connection rather than hold one open for a finished deploy.
	Done bool `json:"done,omitempty"`
}

// What a frame is about.
const (
	KindRun     = "run"
	KindPrepare = "prepare"
)

type watcher struct {
	kind    string
	subject int64
	frames  chan Frame
}

// Watch follows one run, or one machine's install.
//
// The cancel function must be called: it is what stops the runner holding a
// channel for a browser that has gone away.
func (r *Runner) Watch(kind string, subject int64) (<-chan Frame, func()) {
	r.prepare()
	// Buffered by one, and writes coalesce into it. A watcher that is keeping
	// up sees every frame; one that is not sees the latest, which is a superset
	// of what it missed.
	entry := &watcher{kind: kind, subject: subject, frames: make(chan Frame, 1)}
	r.mu.Lock()
	if r.watchers == nil {
		r.watchers = map[*watcher]struct{}{}
	}
	r.watchers[entry] = struct{}{}
	r.mu.Unlock()
	return entry.frames, func() {
		r.mu.Lock()
		delete(r.watchers, entry)
		r.mu.Unlock()
		close(entry.frames)
	}
}

// publish hands a frame to everyone watching that subject.
//
// It never blocks and never fails. This is called from the goroutine reading a
// machine's output, and a deploy that stalled because somebody's laptop went to
// sleep with a tab open would be the worst possible bug to have here.
func (r *Runner) publish(frame Frame) {
	r.mu.Lock()
	listening := make([]*watcher, 0, len(r.watchers))
	for entry := range r.watchers {
		if entry.kind != frame.Kind {
			continue
		}
		switch frame.Kind {
		case KindRun:
			if entry.subject != frame.RunID {
				continue
			}
		default:
			if entry.subject != frame.NodeID {
				continue
			}
		}
		listening = append(listening, entry)
	}
	r.mu.Unlock()

	for _, entry := range listening {
		send(entry, frame)
	}
}

// send drops the frame a slow watcher has not read yet and leaves the newest in
// its place. The newest carries the whole output, so nothing is actually lost —
// only the intermediate renders somebody would never have seen.
//
// The recover is not decoration: a watcher can be cancelled between being
// listed and being sent to, and closing a channel a publisher is about to write
// to is a panic that would otherwise take the process down. Losing a frame for
// a listener that has just left is the correct outcome.
func send(entry *watcher, frame Frame) {
	defer func() { _ = recover() }()
	select {
	case entry.frames <- frame:
	default:
		select {
		case <-entry.frames:
		default:
		}
		select {
		case entry.frames <- frame:
		default:
		}
	}
}
