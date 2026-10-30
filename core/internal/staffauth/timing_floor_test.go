// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
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

// timedSlowBody sends the body through a pipe, pause after the request has
// begun, and times the answer from the last body byte: that is the clock a
// client holds when it trickles the body in behind the headers.
func timedSlowBody(t *testing.T, mux http.Handler, d door, email string, pause time.Duration) (int, time.Duration) {
	t.Helper()
	pr, pw := io.Pipe()
	r := httptest.NewRequest(http.MethodPost, d.path, pr)
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "203.0.113.9:4000"
	if d.mut != nil {
		d.mut(r)
	}
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(w, r)
	}()
	time.Sleep(pause)
	select {
	case <-done:
		t.Fatalf("%s %s: the door answered before the body arrived", d.name, email)
	default:
	}
	written := make(chan error, 1)
	go func() {
		_, err := pw.Write([]byte(pwBody(email, "pw")))
		pw.Close()
		written <- err
	}()
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	lastByte := time.Now()
	<-done
	return w.Code, time.Since(lastByte)
}

// The floor must hold even when the body itself arrives late: a client that
// sends its headers, waits out the floor, then sends the body would get the
// old split back if the floor's clock had started on entry. The clock starts
// once the guard, the decode and both limits have passed, so from the last
// body byte the unknown and the wrong answer land in one band.
func TestFloorHoldsASlowBodyAnswerToo(t *testing.T) {
	const (
		floor = 150 * time.Millisecond
		pause = 200 * time.Millisecond // past the floor, well under ReadTimeout
	)
	_, mux := floorRig(t, floor, testSlowPath)
	for _, d := range doors {
		uCode, unknown := timedSlowBody(t, mux, d, "nobody@x.example", pause)
		wCode, wrong := timedSlowBody(t, mux, d, "wrong@x.example", pause)
		if uCode != http.StatusUnauthorized || wCode != http.StatusUnauthorized {
			t.Fatalf("%s: status %d and %d, want two refusals", d.name, uCode, wCode)
		}
		if unknown < floor || unknown > floor+testSlowPath {
			t.Fatalf("%s unknown took %v from the last body byte, want within [%v, %v]", d.name, unknown, floor, floor+testSlowPath)
		}
		if wrong < floor || wrong > floor+testSlowPath {
			t.Fatalf("%s wrong password took %v from the last body byte, want within [%v, %v]", d.name, wrong, floor, floor+testSlowPath)
		}
		diff := unknown - wrong
		if diff < 0 {
			diff = -diff
		}
		if diff > testSlowPath/4 {
			t.Fatalf("%s: unknown and wrong differ by %v from the last body byte; they must not be told apart", d.name, diff)
		}
	}
}

func median(ds []time.Duration) time.Duration {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

// A single sample can be moved by a loaded runner, so the two refusals are
// also compared by their medians over several answers, per door: the unknown
// address and the wrong password must land within a small fraction of the
// slow path of each other.
func TestFloorMediansOfUnknownAndWrongAgree(t *testing.T) {
	const samples = 7
	_, mux := floorRig(t, testFloor, testSlowPath)
	for _, d := range doors {
		var unknown, wrong []time.Duration
		for i := 0; i < samples; i++ {
			_, u := timed(mux, d, "nobody@x.example")
			_, w := timed(mux, d, "wrong@x.example")
			unknown, wrong = append(unknown, u), append(wrong, w)
		}
		mu, mw := median(unknown), median(wrong)
		diff := mu - mw
		if diff < 0 {
			diff = -diff
		}
		if diff > testSlowPath/5 {
			t.Fatalf("%s: median unknown %v and median wrong %v differ by %v, want under %v", d.name, mu, mw, diff, testSlowPath/5)
		}
	}
}
