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
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
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
		locale                                       string
		logo                                         []byte
		ctype                                        *string
		revision                                     int64
		gstNumber                                    string
		gstNotRegistered                             bool
	}
	if err := db.Pool.QueryRow(context.Background(), `SELECT legal_name, trading_name, phone, email, website, remit_to, locale, logo, logo_content_type, revision, gst_hst_number, gst_hst_not_registered FROM dealer_profile WHERE id = 1`).
		Scan(&snapshot.legal, &snapshot.trading, &snapshot.phone, &snapshot.email, &snapshot.website, &snapshot.remit, &snapshot.locale, &snapshot.logo, &snapshot.ctype, &snapshot.revision, &snapshot.gstNumber, &snapshot.gstNotRegistered); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `UPDATE dealer_profile SET legal_name=$1, trading_name=$2, phone=$3, email=$4, website=$5, remit_to=$6, locale=$7, logo=$8, logo_content_type=$9, revision=$10, gst_hst_number=$11, gst_hst_not_registered=$12 WHERE id = 1`,
			snapshot.legal, snapshot.trading, snapshot.phone, snapshot.email, snapshot.website, snapshot.remit, snapshot.locale, snapshot.logo, snapshot.ctype, snapshot.revision, snapshot.gstNumber, snapshot.gstNotRegistered)
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
	for _, k := range []string{"legal_name", "trading_name", "phone", "email", "website", "remit_to", "locale", "gst_hst_number", "gst_hst_not_registered", "logo_url", "revision", "updated_at"} {
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

// TestDealerProfileLocale: the locale rides the profile read, the locale
// route answers it alone, the write validates the tag (a BCP 47 tag such as
// en-CA, refused in words otherwise), and a write without the field keeps the
// stored one.
func TestDealerProfileLocale(t *testing.T) {
	f := newFixture(t, 0)

	stored := f.do("GET", "/api/v1/dealer-profile", nil)
	if _, ok := stored.body["locale"].(string); !ok {
		t.Fatalf("the profile read carries no locale: %s", stored.raw)
	}

	l := f.do("GET", "/api/v1/dealer-profile/locale", nil)
	if l.status != http.StatusOK || l.body["locale"] != stored.body["locale"] || len(l.body) != 1 {
		t.Errorf("the locale read: %d %s", l.status, l.raw)
	}
	if r := f.do("GET", "/api/v1/dealer-profile/locale?bogus=1", nil); r.status != http.StatusBadRequest {
		t.Errorf("unknown parameter: %d", r.status)
	}

	rev := f.revision()
	withLocale := strings.Replace(goodBody, `"remit_to":"PO Box 1\nTesttown"}`, `"remit_to":"PO Box 1\nTesttown","locale":"en-CA"}`, 1)
	r := f.put(withLocale, "If-Match", `"`+strconv.FormatInt(rev, 10)+`"`)
	if r.status != http.StatusOK || r.body["locale"] != "en-CA" {
		t.Fatalf("write with a good tag: %d %s", r.status, r.raw)
	}
	if l := f.do("GET", "/api/v1/dealer-profile/locale", nil); l.body["locale"] != "en-CA" {
		t.Errorf("the locale read after the write: %s", l.raw)
	}

	r = f.put(strings.Replace(withLocale, `"en-CA"`, `"not a locale"`, 1), "If-Match", `"`+strconv.FormatInt(rev+1, 10)+`"`)
	if r.status != http.StatusBadRequest {
		t.Fatalf("a bad tag: %d %s", r.status, r.raw)
	}
	if d, _ := json.Marshal(r.body["error"]); !strings.Contains(string(d), "locale") {
		t.Errorf("the refusal does not name the field: %s", d)
	}

	// A tag the column's shape check refuses (the likeliest typo: an
	// underscore) must be refused in words, never answered 500.
	for _, bad := range []string{"en_US", "i-klingon", "en-u-ca-gregory", "en-CA-x-yard", "root", "und", "qaa", "x-whatever"} {
		r = f.put(strings.Replace(withLocale, `"en-CA"`, `"`+bad+`"`, 1), "If-Match", `"`+strconv.FormatInt(f.revision(), 10)+`"`)
		if r.status != http.StatusBadRequest {
			t.Errorf("locale %q: %d %s, want 400 in words", bad, r.status, r.raw)
		}
	}

	// A write without the field (a caller from before it existed) keeps en-CA.
	r = f.put(goodBody, "If-Match", `"`+strconv.FormatInt(f.revision(), 10)+`"`)
	if r.status != http.StatusOK || r.body["locale"] != "en-CA" {
		t.Errorf("write without the field: %d %s", r.status, r.raw)
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

// TestDealerProfileLogoRefusals: an image whose decoded size is past the cap
// (8000 by 8000 is a small file and a gigabyte of pixels), a truncated or
// corrupt image, and a file with bytes after the image's end are each a 400.
func TestDealerProfileLogoRefusals(t *testing.T) {
	f := newFixture(t, 0)
	ifm := func() string { return `"` + strconv.FormatInt(f.revision(), 10) + `"` }
	put := func(b []byte) resp {
		return f.do("PUT", "/api/v1/dealer-profile/logo", bytes.NewReader(b), "Content-Type", "image/png", "If-Match", ifm())
	}

	var huge bytes.Buffer
	if err := png.Encode(&huge, image.NewGray(image.Rect(0, 0, 8000, 8000))); err != nil {
		t.Fatal(err)
	}
	if huge.Len() >= dealerprofile.MaxLogoBytes {
		t.Fatalf("the probe image is %d bytes; it must sit under the byte cap to prove the dimension cap", huge.Len())
	}
	if r := put(huge.Bytes()); r.status != http.StatusBadRequest || !strings.Contains(string(r.raw), "pixels") {
		t.Errorf("8000x8000: %d %s", r.status, r.raw)
	}

	var deep bytes.Buffer
	if err := png.Encode(&deep, image.NewNRGBA64(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	if r := put(deep.Bytes()); r.status != http.StatusBadRequest {
		t.Errorf("16 bit png: %d %s", r.status, r.raw)
	}

	good := pngBytes(t, 16, 16)
	if r := put(good[:len(good)-20]); r.status != http.StatusBadRequest {
		t.Errorf("truncated: %d %s", r.status, r.raw)
	}
	corrupt := append([]byte{}, good...)
	for i := len(corrupt) / 2; i < len(corrupt)/2+8; i++ {
		corrupt[i] ^= 0xff
	}
	if r := put(corrupt); r.status != http.StatusBadRequest {
		t.Errorf("corrupt: %d %s", r.status, r.raw)
	}
	poly := append(append([]byte{}, good...), []byte("PK\x03\x04 not part of the image")...)
	if r := put(poly); r.status != http.StatusBadRequest {
		t.Errorf("trailing bytes: %d %s", r.status, r.raw)
	}
	if r := put(good); r.status != http.StatusOK {
		t.Errorf("a clean image: %d %s", r.status, r.raw)
	}

	// A real interlaced PNG (Adam7): the full decode accepts it, so only the
	// interlace check refuses it (the answer is the generic 400).
	inter, err := base64.StdEncoding.DecodeString(interlacedPNG)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(inter)); err != nil {
		t.Fatalf("the interlaced fixture must decode: %v", err)
	}
	if r := put(inter); r.status != http.StatusBadRequest {
		t.Errorf("interlaced png: %d %s", r.status, r.raw)
	}
}

// interlacedPNG is a 16 by 16 Adam7 interlaced 8 bit PNG, which Go's encoder
// cannot write.
const interlacedPNG = "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAMAAAFfKj/FAAAAIGNIUk0AAHomAACAhAAA+gAAAIDoAAB1MAAA6mAAADqYAAAXcJy6UTwAAAAzUExURf8AAO4AEd0AIswAM7sARKoAVZkAZogAd3cAiGYAmVUAqkQAuzMAzCIA3REA7gAA/////97bxooAAAABYktHRBCVsg0sAAAAB3RJTUUH6goOBh4rrRRH+wAAAElJREFUGNNdzNEOgCAMQ9Grok5F+f+/lUADSc/DkjXrACLoM1XkiqatUfVsFQ7hFj5hGMdJZitk1rPMP4thM+yG03AZHsNrKOYHbTsHgZT46VsAAAAASUVORK5CYII="

// restartJPEG is a 24 by 16 baseline JPEG with a restart interval (a DRI and
// an RST marker inside its scan), which Go's encoder cannot write.
const restartJPEG = "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAYEBQYFBAYGBQYHBwYIChAKCgkJChQODwwQFxQYGBcUFhYaHSUfGhsjHBYWICwgIyYnKSopGR8tMC0oMCUoKSj/2wBDAQcHBwoIChMKChMoGhYaKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCj/wAARCAAQABgDASIAAhEBAxEB/8QAHwAAAQUBAQEBAQEAAAAAAAAAAAECAwQFBgcICQoL/8QAtRAAAgEDAwIEAwUFBAQAAAF9AQIDAAQRBRIhMUEGE1FhByJxFDKBkaEII0KxwRVS0fAkM2JyggkKFhcYGRolJicoKSo0NTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqDhIWGh4iJipKTlJWWl5iZmqKjpKWmp6ipqrKztLW2t7i5usLDxMXGx8jJytLT1NXW19jZ2uHi4+Tl5ufo6erx8vP09fb3+Pn6/8QAHwEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoL/8QAtREAAgECBAQDBAcFBAQAAQJ3AAECAxEEBSExBhJBUQdhcRMiMoEIFEKRobHBCSMzUvAVYnLRChYkNOEl8RcYGRomJygpKjU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6goOEhYaHiImKkpOUlZaXmJmaoqOkpaanqKmqsrO0tba3uLm6wsPExcbHyMnK0tPU1dbX2Nna4uPk5ebn6Onq8vP09fb3+Pn6/90ABAAB/9oADAMBAAIRAxEAPwDx+x8P9Pk/SugsvD/T5P0rvrHw/wBPk/SugsfD/T5P0r0MTnXmeRkvEO2p/9DnrHw/0+T9KK9fsfD/AE+T9KK5amde9ufcYPiH92tT/9k="

// progressiveJPEG is a 24 by 16 progressive JPEG (ten scans), which Go's
// encoder cannot write.
const progressiveJPEG = "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wgARCAAQABgDASIAAhEBAxEB/8QAFwAAAwEAAAAAAAAAAAAAAAAAAAUGBP/EABYBAQEBAAAAAAAAAAAAAAAAAAQCBf/aAAwDAQACEAMQAAABldzlkQ6Eqyt3/8QAGRAAAgMBAAAAAAAAAAAAAAAAAAIBAwQR/9oACAEBAAEFAloFzi5xKBM5FPD/xAAYEQACAwAAAAAAAAAAAAAAAAAABAEDEv/aAAgBAwEBPwFZ8rfnJ//EABsRAAIBBQAAAAAAAAAAAAAAAAACAQMEERMh/9oACAECAQE/Aab7Bnt1nDT0/8QAFhAAAwAAAAAAAAAAAAAAAAAAACAx/9oACAEBAAY/Alh//8QAFxABAQEBAAAAAAAAAAAAAAAAAGEBIf/aAAgBAQABPyGaSaKDLnrX/9oADAMBAAIAAwAAABCnD//EABcRAQEBAQAAAAAAAAAAAAAAAAEAESH/2gAIAQMBAT8Qwzs0I3//xAAYEQEAAwEAAAAAAAAAAAAAAAABACExsf/aAAgBAgEBPxCBVXbS8Gf/xAAZEAEBAQEBAQAAAAAAAAAAAAARAMEBQXH/2gAIAQEAAT8QQqGZTMxmYzdLnweeX//Z"

// TestDealerProfileLogoJPEGEnd: a JPEG is whole only when the EOI that
// follows its last scan is its last two bytes. A payload followed by FF D9
// is not (the end marker is no proof of the end), a progressive JPEG with
// several scans still is.
func TestDealerProfileLogoJPEGEnd(t *testing.T) {
	f := newFixture(t, 0)
	put := func(b []byte) resp {
		return f.do("PUT", "/api/v1/dealer-profile/logo", bytes.NewReader(b), "Content-Type", "image/jpeg",
			"If-Match", `"`+strconv.FormatInt(f.revision(), 10)+`"`)
	}

	var plain bytes.Buffer
	if err := jpeg.Encode(&plain, image.NewRGBA(image.Rect(0, 0, 16, 16)), nil); err != nil {
		t.Fatal(err)
	}
	polyglot := append(append(append([]byte{}, plain.Bytes()...), []byte("PK\x03\x04 <script>x</script> payload")...), 0xff, 0xd9)
	if r := put(polyglot); r.status != http.StatusBadRequest {
		t.Errorf("jpeg, payload, FF D9: %d %s", r.status, r.raw)
	}
	if r := put(append(append([]byte{}, plain.Bytes()...), 0x00)); r.status != http.StatusBadRequest {
		t.Errorf("jpeg and one byte: %d %s", r.status, r.raw)
	}
	prog, err := base64.StdEncoding.DecodeString(progressiveJPEG)
	if err != nil {
		t.Fatal(err)
	}
	if r := put(prog); r.status != http.StatusOK {
		t.Errorf("a progressive jpeg: %d %s", r.status, r.raw)
	}
	if r := put(plain.Bytes()); r.status != http.StatusOK {
		t.Errorf("a clean jpeg: %d %s", r.status, r.raw)
	}
	rst, err := base64.StdEncoding.DecodeString(restartJPEG)
	if err != nil {
		t.Fatal(err)
	}
	if r := put(rst); r.status != http.StatusOK {
		t.Errorf("a jpeg with restart markers: %d %s", r.status, r.raw)
	}
}

// TestDealerProfileLogoJPEGReencoded: a JPEG upload is stored as its decoded
// image written again, so a payload hidden inside the last scan (the image's
// own end marker cut off, bytes, then FF D9: a segment walk cannot tell those
// bytes from image data) does not survive, and the picture is the same size.
func TestDealerProfileLogoJPEGReencoded(t *testing.T) {
	f := newFixture(t, 0)
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for x := 0; x < 40; x++ {
		for y := 0; y < 40; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 6), G: uint8(y * 6), B: 90, A: 255})
		}
	}
	var plain bytes.Buffer
	if err := jpeg.Encode(&plain, img, nil); err != nil {
		t.Fatal(err)
	}
	payload := []byte("PK\x03\x04 <script>evil</script> MZ")
	raw := append(append([]byte{}, plain.Bytes()[:plain.Len()-2]...), payload...)
	raw = append(raw, 0xff, 0xd9)

	r := f.do("PUT", "/api/v1/dealer-profile/logo", bytes.NewReader(raw), "Content-Type", "image/jpeg",
		"If-Match", `"`+strconv.FormatInt(f.revision(), 10)+`"`)
	if r.status != http.StatusOK {
		t.Fatalf("the upload: %d %s", r.status, r.raw)
	}
	got := f.do("GET", "/api/v1/dealer-profile/logo", nil)
	if got.status != http.StatusOK {
		t.Fatalf("the read: %d %s", got.status, got.raw)
	}
	if bytes.Contains(got.raw, payload) || bytes.Contains(got.raw, []byte("<script>")) {
		t.Errorf("the stored logo still carries the payload")
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(got.raw))
	if err != nil {
		t.Fatalf("the stored logo must decode: %v", err)
	}
	if cfg.Width != 40 || cfg.Height != 40 {
		t.Errorf("stored size %dx%d, want 40x40", cfg.Width, cfg.Height)
	}
	if ct := got.header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content type %q", ct)
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

// TestDealerProfileGSTHST pins the GST/HST registration on the wire (CP-14
// A1): a typed number is stored normalized, a malformed one is refused with
// the field named, the not registered switch and a number are refused
// together, a write that leaves both fields out keeps them, and the audit
// row carries both.
func TestDealerProfileGSTHST(t *testing.T) {
	f := newFixture(t, 0)
	ifMatch := func() []string { return []string{"If-Match", `"` + strconv.FormatInt(f.revision(), 10) + `"`} }
	withGST := func(extra string) string { return strings.TrimSuffix(goodBody, "}") + "," + extra + "}" }

	r := f.put(withGST(`"gst_hst_number":"123 456 789 rt-0001","gst_hst_not_registered":false`), ifMatch()...)
	if r.status != http.StatusOK || r.body["gst_hst_number"] != "123456789RT0001" || r.body["gst_hst_not_registered"] != false {
		t.Fatalf("normalized write: %d %s", r.status, r.raw)
	}
	for _, bad := range []string{`"gst_hst_number":"12345678RT0001"`, `"gst_hst_number":"123456789RP0001"`} {
		r := f.put(withGST(bad), ifMatch()...)
		if r.status != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", bad, r.status, r.raw)
		}
		if d, _ := json.Marshal(r.body["error"]); !strings.Contains(string(d), "gst_hst_number") || !strings.Contains(string(d), "RT") {
			t.Errorf("%s: the refusal does not name the field and its shape: %s", bad, d)
		}
	}
	if r := f.put(withGST(`"gst_hst_number":"123456789RT0001","gst_hst_not_registered":true`), ifMatch()...); r.status != http.StatusBadRequest {
		t.Fatalf("number and switch together: %d %s", r.status, r.raw)
	}
	// A caller from before the fields existed keeps them as they are.
	if r := f.put(goodBody, ifMatch()...); r.status != http.StatusOK || r.body["gst_hst_number"] != "123456789RT0001" {
		t.Fatalf("write without the fields: %d %s", r.status, r.raw)
	}
	r = f.put(withGST(`"gst_hst_number":"","gst_hst_not_registered":true`), ifMatch()...)
	if r.status != http.StatusOK || r.body["gst_hst_number"] != "" || r.body["gst_hst_not_registered"] != true {
		t.Fatalf("switch on: %d %s", r.status, r.raw)
	}
	var changes []byte
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT changes FROM audit_log WHERE entity_type = 'dealer_profile' AND action = 'dealer_profile.update' ORDER BY created_at DESC LIMIT 1`).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(changes), `"gst_hst_not_registered": true`) && !strings.Contains(string(changes), `"gst_hst_not_registered":true`) {
		t.Errorf("audit row lacks the switch: %s", changes)
	}
	if !strings.Contains(string(changes), "123456789RT0001") {
		t.Errorf("audit row lacks the number it replaced: %s", changes)
	}
}
