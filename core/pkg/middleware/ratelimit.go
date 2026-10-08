// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gablelbm/gable/pkg/clientip"
	"github.com/gablelbm/gable/pkg/httputil"
)

const maxVisitors = 100000

type rateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	rate     int
	window   time.Duration
}

type visitor struct {
	count       int
	windowStart time.Time
}

// RateLimit returns middleware that enforces a per-IP request limit within a
// sliding window. Requests exceeding the limit receive 429 Too Many Requests.
// The caller is the TCP peer; X-Forwarded-For is believed only when the peer
// is inside the trusted proxy networks (the zero value trusts none).
func RateLimit(requestsPerMinute int, trusted clientip.Trusted) func(http.Handler) http.Handler {
	rl := &rateLimiter{
		visitors: make(map[string]*visitor),
		rate:     requestsPerMinute,
		window:   time.Minute,
	}

	// Cleanup stale entries every 5 minutes
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			rl.mu.Lock()
			now := time.Now()
			for ip, v := range rl.visitors {
				if now.Sub(v.windowStart) > rl.window*2 {
					delete(rl.visitors, ip)
				}
			}
			rl.mu.Unlock()
		}
	}()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := trusted.Of(r)

			rl.mu.Lock()
			now := time.Now()
			v, exists := rl.visitors[ip]
			if !exists || now.Sub(v.windowStart) > rl.window {
				// Fail-open: if visitor map is at capacity, skip tracking and allow through
				if !exists && len(rl.visitors) > maxVisitors {
					rl.mu.Unlock()
					next.ServeHTTP(w, r)
					return
				}
				rl.visitors[ip] = &visitor{count: 1, windowStart: now}
				rl.mu.Unlock()
				next.ServeHTTP(w, r)
				return
			}
			v.count++
			if v.count > rl.rate {
				rl.mu.Unlock()
				httputil.RespondError(w, r, "Too Many Requests", http.StatusTooManyRequests, nil)
				return
			}
			rl.mu.Unlock()
			next.ServeHTTP(w, r)
		})
	}
}

// StrictRateLimit returns middleware that enforces a stricter per-IP request
// limit, intended for sensitive endpoints like login. It maintains its own
// visitor map so counts are independent of the global rate limiter.
func StrictRateLimit(requestsPerMinute int, trusted clientip.Trusted) func(http.Handler) http.Handler {
	return RateLimit(requestsPerMinute, trusted)
}
