// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/appwrite"
)

const (
	testFloor    = 300 * time.Millisecond
	testSlowPath = 120 * time.Millisecond
)

// floorRig is both password doors over a directory that answers an unknown
// address at once and a wrong password after the hash.
func floorRig(t *testing.T, floor, slow time.Duration) (*Handler, *http.ServeMux) {
	t.Helper()
	dir := okDir()
	dir.answer = func(email string) (time.Duration, error) {
		switch email {
		case "nobody@x.example":
			return 0, &appwrite.Error{StatusCode: http.StatusUnauthorized}
		case "wrong@x.example":
			return slow, &appwrite.Error{StatusCode: http.StatusUnauthorized}
		case "down@x.example":
			return slow, &appwrite.Error{StatusCode: http.StatusInternalServerError}
		}
		return slow, nil
	}
	svc, _, _ := newSvc(t, dir)
	h := NewHandler(svc, HandlerConfig{Origin: ownOrigin, SignInFloor: floor, PerEmail: 100, PerAddress: 100})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux
}

type door struct {
	name, path string
	mut        func(*http.Request)
}

var doors = []door{
	{"web", "/api/v1/auth/signin", csrf},
	{"shell", shellSignInPath, nil},
}

func timed(mux http.Handler, d door, email string) (int, time.Duration) {
	start := time.Now()
	w := post(mux, d.path, pwBody(email, "pw"), d.mut)
	return w.Code, time.Since(start)
}

func TestFloorHoldsEveryPasswordAnswerToOneBand(t *testing.T) {
	_, mux := floorRig(t, testFloor, testSlowPath)
	for _, d := range doors {
		for email, want := range map[string]int{
			"nobody@x.example": http.StatusUnauthorized,
			"wrong@x.example":  http.StatusUnauthorized,
			"down@x.example":   http.StatusServiceUnavailable,
			"a@x.example":      http.StatusOK,
		} {
			code, took := timed(mux, d, email)
			if code != want {
				t.Fatalf("%s %s: status %d, want %d", d.name, email, code, want)
			}
			if took < testFloor || took > testFloor+testSlowPath {
				t.Fatalf("%s %s took %v, want within [%v, %v]", d.name, email, took, testFloor, testFloor+testSlowPath)
			}
		}
	}
}

func TestFloorNeverShortensASlowAnswer(t *testing.T) {
	slow := 250 * time.Millisecond
	_, mux := floorRig(t, 100*time.Millisecond, slow)
	for _, d := range doors {
		if _, took := timed(mux, d, "wrong@x.example"); took < slow {
			t.Fatalf("%s: a %v answer came back in %v", d.name, slow, took)
		}
	}
}

func TestFloorSkipsAnswersGivenBeforeAppwrite(t *testing.T) {
	h, mux := floorRig(t, testFloor, testSlowPath)
	h.emailPasswordAllowed = func(_ context.Context) bool { return false }
	for _, d := range doors {
		code, took := timed(mux, d, "nobody@x.example")
		if code != http.StatusForbidden || took >= testFloor {
			t.Fatalf("%s: allowlist refusal = %d in %v", d.name, code, took)
		}
	}
}
