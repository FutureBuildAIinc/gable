// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dealerprofile_test

// The dealer profile routes on the wire contract (ADR 0001): the read, the
// write with its revision precondition and its audit row holding the before
// and after, the logo upload with its size cap and content check, and the
// transaction proof at pool size 4. The profile is one row, so these tests run
// in order and put the row back as they found it.

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/dealerprofile"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
)

type fixture struct {
	t   *testing.T
	db  *database.DB
	srv *httptest.Server
}

func newFixture(t *testing.T, maxConns int32) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	var db *database.DB
	if maxConns > 0 {
		db = testutil.RequireDBMaxConns(t, maxConns)
	} else {
		db = testutil.RequireDB(t)
	}
	var snapshot struct {
		legal, trading, phone, email, website, remit string
		logo                                         []byte
		ctype                                        *string
		revision                                     int64
	}
	if err := db.Pool.QueryRow(context.Background(), `SELECT legal_name, trading_name, phone, email, website, remit_to, logo, logo_content_type, revision FROM dealer_profile WHERE id = 1`).
		Scan(&snapshot.legal, &snapshot.trading, &snapshot.phone, &snapshot.email, &snapshot.website, &snapshot.remit, &snapshot.logo, &snapshot.ctype, &snapshot.revision); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `UPDATE dealer_profile SET legal_name=$1, trading_name=$2, phone=$3, email=$4, website=$5, remit_to=$6, logo=$7, logo_content_type=$8, revision=$9 WHERE id = 1`,
			snapshot.legal, snapshot.trading, snapshot.phone, snapshot.email, snapshot.website, snapshot.remit, snapshot.logo, snapshot.ctype, snapshot.revision)
	})
	svc := dealerprofile.NewService(dealerprofile.NewRepository(db), db, audit.NewLogger(db))
	mux := http.NewServeMux()
	dealerprofile.NewHandler(svc).RegisterRoutes(mux, dealerprofile.Guards{})
	f := &fixture{t: t, db: db, srv: httptest.NewServer(mux)}
	t.Cleanup(f.srv.Close)
	return f
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func (f *fixture) do(method, path string, body io.Reader, headers ...string) resp {
	f.t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, body)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("X-Request-ID", "req-dealer-profile")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, raw: raw}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&out.body)
	return out
}

func (f *fixture) put(body string, headers ...string) resp {
	f.t.Helper()
	return f.do("PUT", "/api/v1/dealer-profile", strings.NewReader(body), append([]string{"Content-Type", "application/json"}, headers...)...)
}

func (f *fixture) revision() int64 {
	f.t.Helper()
	r := f.do("GET", "/api/v1/dealer-profile", nil)
	n, err := r.body["revision"].(json.Number).Int64()
	if err != nil {
		f.t.Fatalf("revision: %v (%s)", err, r.raw)
	}
	return n
}

const goodBody = `{"legal_name":"Test Yard Supply Ltd","trading_name":"Test Yard","phone":"555-0100","email":"office@example.test","website":"https://example.test","remit_to":"PO Box 1\nTesttown"}`

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{R: 200, G: 30, B: 30, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDealerProfileReadShape(t *testing.T) {
	f := newFixture(t, 0)
	r := f.do("GET", "/api/v1/dealer-profile", nil)
	if r.status != http.StatusOK {
		t.Fatalf("get: %d %s", r.status, r.raw)
	}
	for _, k := range []string{"legal_name", "trading_name", "phone", "email", "website", "remit_to", "logo_url", "revision", "updated_at"} {
		if _, ok := r.body[k]; !ok {
			t.Errorf("field %q missing from %s", k, r.raw)
		}
	}
	if r.header.Get("ETag") == "" {
		t.Error("no ETag on the read")
	}
	if got := f.do("GET", "/api/v1/dealer-profile?bogus=1", nil); got.status != http.StatusBadRequest {
		t.Errorf("unknown parameter: %d", got.status)
	}
}

func TestDealerProfileWriteNeedsRevisionAndAudits(t *testing.T) {
	f := newFixture(t, 0)
	rev := f.revision()

	if r := f.put(goodBody); r.status != http.StatusPreconditionRequired {
		t.Fatalf("no precondition: %d %s", r.status, r.raw)
	}
	if r := f.put(goodBody, "If-Match", `"`+strconv.FormatInt(rev+5, 10)+`"`); r.status != http.StatusConflict {
		t.Fatalf("stale: %d %s", r.status, r.raw)
	}
	if r := f.put(`{"legal_name":"","trading_name":"x","phone":"","email":"not-an-email","website":"","remit_to":""}`, "If-Match", `"`+strconv.FormatInt(rev, 10)+`"`); r.status != http.StatusBadRequest {
		t.Fatalf("invalid: %d %s", r.status, r.raw)
	} else if d, _ := json.Marshal(r.body["error"]); !strings.Contains(string(d), "legal_name") || !strings.Contains(string(d), "email") {
		t.Errorf("details do not name both fields: %s", d)
	}
	if r := f.put(`{"legal_name":"A","bogus":1}`, "If-Match", `"`+strconv.FormatInt(rev, 10)+`"`); r.status != http.StatusBadRequest {
		t.Fatalf("unknown field: %d %s", r.status, r.raw)
	}

	r := f.put(goodBody, "If-Match", `"`+strconv.FormatInt(rev, 10)+`"`)
	if r.status != http.StatusOK {
		t.Fatalf("write: %d %s", r.status, r.raw)
	}
	if r.body["legal_name"] != "Test Yard Supply Ltd" || r.body["revision"] != json.Number(strconv.FormatInt(rev+1, 10)) {
		t.Errorf("write body: %s", r.raw)
	}
	if r.header.Get("ETag") != `"`+strconv.FormatInt(rev+1, 10)+`"` {
		t.Errorf("ETag = %q", r.header.Get("ETag"))
	}

	var changes []byte
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT changes FROM audit_log WHERE entity_type = 'dealer_profile' AND action = 'dealer_profile.update' ORDER BY created_at DESC LIMIT 1`).Scan(&changes); err != nil {
		t.Fatalf("no audit row: %v", err)
	}
	var c struct {
		Before map[string]any `json:"before"`
		After  map[string]any `json:"after"`
	}
	if err := json.Unmarshal(changes, &c); err != nil {
		t.Fatal(err)
	}
	if c.After["legal_name"] != "Test Yard Supply Ltd" || c.Before == nil || c.Before["legal_name"] == "Test Yard Supply Ltd" {
		t.Errorf("audit before/after wrong: %s", changes)
	}
}

func TestDealerProfileLogo(t *testing.T) {
	f := newFixture(t, 0)
	rev := f.revision()
	ifm := func() string { return `"` + strconv.FormatInt(f.revision(), 10) + `"` }
	_ = rev
	good := pngBytes(t, 8, 8)

	if r := f.do("PUT", "/api/v1/dealer-profile/logo", bytes.NewReader(good), "Content-Type", "image/png"); r.status != http.StatusPreconditionRequired {
		t.Errorf("no precondition: %d %s", r.status, r.raw)
	}
	if r := f.do("PUT", "/api/v1/dealer-profile/logo", strings.NewReader("hello"), "Content-Type", "text/plain", "If-Match", ifm()); r.status != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d %s", r.status, r.raw)
	}
	if r := f.do("PUT", "/api/v1/dealer-profile/logo", strings.NewReader("not really a png at all"), "Content-Type", "image/png", "If-Match", ifm()); r.status != http.StatusBadRequest {
		t.Errorf("content that is not the declared type: %d %s", r.status, r.raw)
	}
	big := append(append([]byte{}, good...), bytes.Repeat([]byte{0}, dealerprofile.MaxLogoBytes)...)
	if r := f.do("PUT", "/api/v1/dealer-profile/logo", bytes.NewReader(big), "Content-Type", "image/png", "If-Match", ifm()); r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize: %d %s", r.status, r.raw)
	}

	r := f.do("PUT", "/api/v1/dealer-profile/logo", bytes.NewReader(good), "Content-Type", "image/png", "If-Match", ifm())
	if r.status != http.StatusOK {
		t.Fatalf("upload: %d %s", r.status, r.raw)
	}
	if r.body["logo_url"] != "/api/v1/dealer-profile/logo" {
		t.Errorf("logo_url = %v", r.body["logo_url"])
	}
	g := f.do("GET", "/api/v1/dealer-profile/logo", nil)
	if g.status != http.StatusOK || g.header.Get("Content-Type") != "image/png" || !bytes.Equal(g.raw, good) {
		t.Errorf("logo read: %d %q (%d bytes)", g.status, g.header.Get("Content-Type"), len(g.raw))
	}
	var n int
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = 'dealer_profile.logo' AND entity_type = 'dealer_profile'`).Scan(&n); err != nil || n == 0 {
		t.Errorf("no logo audit row (%v)", err)
	}
}

// TestDealerProfileThreeWritersOneWinner: three racers on one revision at
// pool size 4, exactly one wins and the others are stale.
func TestDealerProfileThreeWritersOneWinner(t *testing.T) {
	f := newFixture(t, 4)
	rev := f.revision()
	var wg sync.WaitGroup
	statuses := make([]int, 3)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := strings.Replace(goodBody, "Test Yard Supply Ltd", "Racer "+strconv.Itoa(i), 1)
			statuses[i] = f.put(body, "If-Match", `"`+strconv.FormatInt(rev, 10)+`"`).status
		}(i)
	}
	wg.Wait()
	wins, stale := 0, 0
	for _, s := range statuses {
		switch s {
		case http.StatusOK:
			wins++
		case http.StatusConflict:
			stale++
		}
	}
	if wins != 1 || stale != 2 {
		t.Fatalf("statuses = %v, want one 200 and two 409", statuses)
	}
	if got := f.revision(); got != rev+1 {
		t.Errorf("revision = %d, want %d", got, rev+1)
	}
}
