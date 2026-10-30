// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"sync"
	"time"
)

// limiter is a fixed window counter per key. Appwrite's own rate limits do
// not apply to the core's key calls, so the core limits its own sign in
// routes (ADR 0012 section 2): per client address and per email.
type limiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	now    func() time.Time
	hits   map[string]*window
}

type window struct {
	start time.Time
	n     int
}

func newLimiter(limit int, w time.Duration) *limiter {
	return &limiter{limit: limit, window: w, now: time.Now, hits: map[string]*window{}}
}

// allow counts one attempt on key and reports whether it is within the limit;
// when it is not, retryAfter says how long until the window turns over.
func (l *limiter) allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.hits) > 10000 {
		for k, w := range l.hits {
			if now.Sub(w.start) >= l.window {
				delete(l.hits, k)
			}
		}
	}
	w := l.hits[key]
	if w == nil || now.Sub(w.start) >= l.window {
		l.hits[key] = &window{start: now, n: 1}
		return true, 0
	}
	if w.n >= l.limit {
		return false, w.start.Add(l.window).Sub(now)
	}
	w.n++
	return true, 0
}
