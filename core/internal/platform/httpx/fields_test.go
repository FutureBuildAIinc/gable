// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fieldMessages(t *testing.T, v *Validator) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := v.Err()
	if err == nil {
		return out
	}
	for _, d := range err.(*Error).Details {
		out[d.Field] = d.Message
	}
	return out
}

// RULE (ADR 0001 section 12): a timestamp on the wire is RFC 3339 UTC with the
// Z at microsecond precision, whatever zone the value was read in.
func TestTimestampMarshalsUTCMicroseconds(t *testing.T) {
	zone := time.FixedZone("x", -7*3600)
	ts := TimestampOf(time.Date(2030, 1, 2, 3, 4, 5, 123456000, zone))
	b, err := json.Marshal(ts)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `"2030-01-02T10:04:05.123456Z"`; got != want {
		t.Fatalf("marshal = %s, want %s", got, want)
	}
	whole := TimestampOf(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
	b, _ = json.Marshal(whole)
	if got, want := string(b), `"2030-01-02T03:04:05.000000Z"`; got != want {
		t.Fatalf("whole second marshal = %s, want %s: the fraction is never dropped", got, want)
	}
}

func TestTimestampUnmarshal(t *testing.T) {
	var ts Timestamp
	if err := json.Unmarshal([]byte(`"2030-01-02T10:04:05.5Z"`), &ts); err != nil {
		t.Fatal(err)
	}
	if !ts.Time.Equal(time.Date(2030, 1, 2, 10, 4, 5, 500000000, time.UTC)) {
		t.Fatalf("unmarshal = %v", ts.Time)
	}
	for _, bad := range []string{`"2030-01-02"`, `"yesterday"`, `5`, `null`, `"2030-01-02T10:04:05+02:00"`} {
		var x Timestamp
		if err := json.Unmarshal([]byte(bad), &x); err == nil {
			t.Errorf("unmarshal %s = nil error, want a refusal", bad)
		}
	}
}

func TestPtrTimestamp(t *testing.T) {
	if PtrTimestamp(nil) != nil {
		t.Fatal("PtrTimestamp(nil) must be nil so the field marshals as null")
	}
	now := time.Now()
	if p := PtrTimestamp(&now); p == nil || !p.Time.Equal(now) {
		t.Fatalf("PtrTimestamp = %v", p)
	}
}

func TestValidatorInt(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		required bool
		want     int64
		wantOK   bool
		wantErr  bool
	}{
		{"integer", `1250`, true, 1250, true, false},
		{"negative", `-5`, true, -5, true, false},
		{"absent optional", ``, false, 0, false, false},
		{"null optional", `null`, false, 0, false, false},
		{"absent required", ``, true, 0, false, true},
		{"null required", `null`, true, 0, false, true},
		{"decimal point is a decode error", `12.5`, true, 0, false, true},
		{"trailing zero decimal is still a decimal", `12.0`, true, 0, false, true},
		{"exponent", `1e3`, true, 0, false, true},
		{"string", `"12"`, true, 0, false, true},
		{"overflow", `99999999999999999999`, true, 0, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := &Validator{}
			got, ok := v.Int("freight_cents", json.RawMessage(c.raw), c.required)
			if ok != c.wantOK || got != c.want {
				t.Fatalf("Int = (%d, %v), want (%d, %v)", got, ok, c.want, c.wantOK)
			}
			if (v.Err() != nil) != c.wantErr {
				t.Fatalf("Err = %v, wantErr %v", v.Err(), c.wantErr)
			}
			if c.wantErr {
				if _, named := fieldMessages(t, v)["freight_cents"]; !named {
					t.Fatalf("the error does not name freight_cents: %v", v.Err())
				}
			}
		})
	}
}

// RULE (ADR 0001 section 7a): quantities travel as decimal strings, never as
// JSON numbers.
func TestValidatorQuantity(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    Quantity
		wantOK  bool
		wantErr bool
	}{
		{"string", `"12.5"`, 125000, true, false},
		{"whole", `"1000"`, 10000000, true, false},
		{"json number refused", `12.5`, 0, false, true},
		{"integer json number refused", `10`, 0, false, true},
		{"leading zero", `"0012"`, 0, false, true},
		{"beyond scale", `"1.00001"`, 0, false, true},
		{"empty", `""`, 0, false, true},
		{"absent", ``, 0, false, true},
		{"null", `null`, 0, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := &Validator{}
			got, ok := v.Quantity("lines[0].quantity", json.RawMessage(c.raw), true)
			if ok != c.wantOK || got != c.want {
				t.Fatalf("Quantity = (%d, %v), want (%d, %v)", got, ok, c.want, c.wantOK)
			}
			if (v.Err() != nil) != c.wantErr {
				t.Fatalf("Err = %v, wantErr %v", v.Err(), c.wantErr)
			}
			if c.wantErr {
				if _, named := fieldMessages(t, v)["lines[0].quantity"]; !named {
					t.Fatalf("the error does not name lines[0].quantity: %v", v.Err())
				}
			}
		})
	}
	v := &Validator{}
	if _, ok := v.Quantity("q", json.RawMessage(``), false); ok || v.Err() != nil {
		t.Fatalf("an absent optional quantity is neither a value nor an error: %v", v.Err())
	}
}

func TestValidatorUUID(t *testing.T) {
	good := "5f2c6d62-2c3c-4a0d-9d4b-0a1b2c3d4e5f"
	v := &Validator{}
	id, ok := v.UUID("customer_id", &good, true)
	if !ok || id.String() != good {
		t.Fatalf("UUID = (%v, %v)", id, ok)
	}
	for name, in := range map[string]*string{"absent": nil, "empty": strPtr(""), "garbage": strPtr("nope"), "uppercase": strPtr(strings.ToUpper(good))} {
		v := &Validator{}
		if _, ok := v.UUID("customer_id", in, true); ok {
			t.Errorf("%s: accepted", name)
		}
		if _, named := fieldMessages(t, v)["customer_id"]; !named {
			t.Errorf("%s: the error does not name customer_id", name)
		}
	}
	v = &Validator{}
	if _, ok := v.UUID("job_id", nil, false); ok || v.Err() != nil {
		t.Fatalf("an absent optional UUID is neither a value nor an error: %v", v.Err())
	}
}

func strPtr(s string) *string { return &s }

func TestValidatorTimestamp(t *testing.T) {
	v := &Validator{}
	ts, ok := v.Timestamp("expires_at", json.RawMessage(`"2031-05-06T07:08:09.25Z"`), false)
	if !ok || ts == nil || ts.Time.Nanosecond() != 250000000 {
		t.Fatalf("Timestamp = (%v, %v)", ts, ok)
	}
	v = &Validator{}
	if ts, ok := v.Timestamp("expires_at", json.RawMessage(`null`), false); ok || ts != nil || v.Err() != nil {
		t.Fatalf("null optional timestamp = (%v, %v, %v)", ts, ok, v.Err())
	}
	v = &Validator{}
	v.Timestamp("expires_at", json.RawMessage(`"tomorrow"`), false)
	if _, named := fieldMessages(t, v)["expires_at"]; !named {
		t.Fatalf("a bad timestamp does not name expires_at: %v", v.Err())
	}
}

func decodeRequest(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
}

// RULE (ADR 0001 sections 3 and 4): a body the route cannot consume is a 400
// bad_request that names the field where the decoder knows it.
func TestDecodeJSON(t *testing.T) {
	type dst struct {
		Name string          `json:"name"`
		Qty  json.RawMessage `json:"qty"`
		Rows []struct {
			N int `json:"n"`
		} `json:"rows"`
	}

	var ok dst
	if err := DecodeJSON(decodeRequest(`{"name":"a","qty":"5","rows":[{"n":1}]}`), &ok); err != nil {
		t.Fatalf("a valid body: %v", err)
	}
	if ok.Name != "a" || string(ok.Qty) != `"5"` || len(ok.Rows) != 1 {
		t.Fatalf("decoded = %+v", ok)
	}

	cases := []struct {
		name      string
		body      string
		wantField string
	}{
		{"empty body", ``, ""},
		{"not json", `{nope`, ""},
		{"unknown field", `{"name":"a","nmae":"b"}`, "nmae"},
		{"wrong type", `{"name":5}`, "name"},
		{"wrong type in a row", `{"rows":[{"n":"x"}]}`, "rows[0].n"},
		{"two documents", `{"name":"a"}{"name":"b"}`, ""},
		{"an array", `[1]`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var d dst
			err := DecodeJSON(decodeRequest(c.body), &d)
			e, isErr := err.(*Error)
			if !isErr || e.Status != http.StatusBadRequest || e.Code != CodeBadRequest {
				t.Fatalf("DecodeJSON = %#v, want a 400 bad_request", err)
			}
			if c.wantField != "" {
				if len(e.Details) != 1 || e.Details[0].Field != c.wantField {
					t.Fatalf("details = %+v, want one entry naming %q", e.Details, c.wantField)
				}
			}
		})
	}
}

func TestDecodeJSONBodyTooLarge(t *testing.T) {
	r := decodeRequest(`{"name":"` + strings.Repeat("a", 64) + `"}`)
	r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, 16)
	var d struct {
		Name string `json:"name"`
	}
	err := DecodeJSON(r, &d)
	e, ok := err.(*Error)
	if !ok || e.Status != http.StatusRequestEntityTooLarge || e.Code != CodePayloadTooLarge {
		t.Fatalf("DecodeJSON = %#v, want a 413 payload_too_large", err)
	}
}
