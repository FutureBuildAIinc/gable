// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Slice O2 (CP-13, ADR 0011 sections 5.1, 6.1 and 7): a register's device
// is enrolled and revoked by an owner or admin with the counter PIN, never a
// cashier or a machine key, and both acts are audited; a new enrolment raises
// the epoch and revokes the previous key; the old key's later sales go to the
// queue as device_revoked; a re-enrolment over a lost store leaves the
// unknown tail item; two enrolments of one register race to one epoch. The
// shell's side (the key made in the keychain, ADR 0012 slice 5) is played by
// the test.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

const devicePIN = "4826"

func newDeviceKey(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(pub), priv
}

// enrolBody is the shell's enrolment request.
func enrolBody(epoch int64, key, manager, pin string) map[string]any {
	return map[string]any{"device_epoch": epoch, "public_key": key, "manager": manager, "pin": pin}
}

// deviceCleanup takes the device rows and their audit and outbox rows the
// test's register wrote.
func deviceCleanup(t *testing.T, f *fixture, register string) {
	t.Cleanup(func() {
		ctx := context.Background()
		entity := uuid.NewSHA1(uuid.NameSpaceURL, []byte("pos_register_device:"+register))
		for _, q := range []string{
			`DELETE FROM audit_log WHERE entity_type = 'pos_register' AND entity_id = $1`,
			`DELETE FROM events_outbox WHERE entity_type = 'pos_register' AND entity_id = $1`,
		} {
			if _, err := f.db.Pool.Exec(ctx, q, entity); err != nil {
				t.Logf("device cleanup: %v", err)
			}
		}
		for _, q := range []string{
			`DELETE FROM events_outbox WHERE entity_type = 'pos_offline_exception' AND entity_id IN (SELECT id FROM pos_offline_exceptions WHERE register_id = $1)`,
			`DELETE FROM audit_log WHERE entity_type = 'pos_offline_exception' AND entity_id IN (SELECT id FROM pos_offline_exceptions WHERE register_id = $1)`,
			`DELETE FROM pos_offline_exceptions WHERE register_id = $1`,
			`UPDATE pos_transactions SET snapshot_id = NULL WHERE register_id = $1`,
			`DELETE FROM pos_offline_snapshots WHERE register_id = $1`,
			`DELETE FROM pos_register_devices WHERE register_id = $1`,
		} {
			if _, err := f.db.Pool.Exec(ctx, q, register); err != nil {
				t.Logf("device cleanup: %v", err)
			}
		}
	})
}

func TestOfflineDevice_EnrolmentAuthority(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	ctx := context.Background()
	deviceCleanup(t, f, f.register)
	owner, _, _, _ := seedManager(t, f, "owner", devicePIN)
	cashierMgr, _, _, _ := seedManager(t, f, "cashier", devicePIN)
	path := "/api/v1/pos/offline/registers/" + f.register + "/device"
	admin := []string{"X-Test-Role", "admin", "X-Test-Sub", mustUUID(t)}
	key1, _ := newDeviceKey(t)

	// a cashier and finance never reach the act; the read admits finance
	for _, role := range []string{"cashier", "finance"} {
		r := f.do("POST", path, enrolBody(1, key1, owner, devicePIN), "X-Test-Role", role, "X-Test-Sub", mustUUID(t))
		if r.status != http.StatusForbidden {
			t.Errorf("%s enrol = %d %s, want 403", role, r.status, r.raw)
		}
		r = f.do("POST", path+"/revoke", map[string]any{"device_epoch": 1, "reason": "lost", "manager": owner, "pin": devicePIN},
			"X-Test-Role", role, "X-Test-Sub", mustUUID(t))
		if r.status != http.StatusForbidden {
			t.Errorf("%s revoke = %d %s, want 403", role, r.status, r.raw)
		}
	}
	if r := f.do("GET", path, nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusForbidden {
		t.Errorf("cashier read = %d, want 403", r.status)
	}
	r := f.do("GET", path, nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK || num(t, r.body, "next_epoch") != 1 || r.body["device"] != nil {
		t.Fatalf("finance read before enrolment = %d %s", r.status, r.raw)
	}

	// a machine key is refused both acts, whatever its scopes
	keyed := keyedServer(f, uuid.NewString())
	for _, p := range []string{path, path + "/revoke"} {
		raw, _ := json.Marshal(enrolBody(1, key1, owner, devicePIN))
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		req.URL.Host, req.URL.Scheme, req.RequestURI = strings.TrimPrefix(keyed.URL, "http://"), "http", ""
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body keyRefusalBody
		_ = json.NewDecoder(res.Body).Decode(&body)
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden || !strings.Contains(body.Error.Message, "never a machine key") {
			t.Errorf("machine key on %s = %d %q, want 403", p, res.StatusCode, body.Error.Message)
		}
	}

	// the PIN is the proof: a wrong one is refused and counted, a cashier's
	// is refused for its role
	r = f.do("POST", path, enrolBody(1, key1, owner, "0000"), admin...)
	if r.status != http.StatusForbidden || errCode(r) != "step_up_refused" {
		t.Fatalf("wrong PIN = %d %s, want 403 step_up_refused", r.status, r.raw)
	}
	if n := countOf(t, f.db, `SELECT counter_pin_failures FROM staff WHERE id = $1::uuid`, owner); n != 1 {
		t.Errorf("failures after a wrong PIN = %d, want 1", n)
	}
	r = f.do("POST", path, enrolBody(1, key1, cashierMgr, devicePIN), admin...)
	if r.status != http.StatusForbidden || errCode(r) != "step_up_role" {
		t.Fatalf("cashier's PIN = %d %s, want 403 step_up_role", r.status, r.raw)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_register_devices WHERE register_id = $1`, f.register); n != 0 {
		t.Fatalf("a refused enrolment wrote %d device rows", n)
	}

	// the manager enrols: epoch 1, the chain at GENESIS, audited
	r = f.do("POST", path, enrolBody(1, key1, owner, devicePIN), admin...)
	if r.status != http.StatusCreated {
		t.Fatalf("enrol = %d %s", r.status, r.raw)
	}
	dev := r.body["device"].(map[string]any)
	if devNum(dev, "device_epoch") != 1 || dev["public_key"] != key1 || dev["chain_head"] != "GENESIS" ||
		dev["enrolled_by"] != owner || num(t, r.body, "next_epoch") != 2 || r.body["previous"] != nil {
		t.Errorf("enrol answer = %s", r.raw)
	}
	if n := countOf(t, f.db, `SELECT counter_pin_failures FROM staff WHERE id = $1::uuid`, owner); n != 0 {
		t.Errorf("failures after a good PIN = %d, want 0", n)
	}
	entity := uuid.NewSHA1(uuid.NameSpaceURL, []byte("pos_register_device:"+f.register))
	var mgr string
	if err := f.db.Pool.QueryRow(ctx, `SELECT changes->>'manager_staff_id' FROM audit_log
		WHERE action = 'pos.register_device.enrolled' AND entity_id = $1`, entity).Scan(&mgr); err != nil || mgr != owner {
		t.Errorf("enrolment audit row: manager %q, err %v", mgr, err)
	}

	// the next enrolment names the next epoch, or it is refused
	key2, _ := newDeviceKey(t)
	r = f.do("POST", path, enrolBody(1, key2, owner, devicePIN), admin...)
	if r.status != http.StatusConflict || errCode(r) != "epoch_moved" {
		t.Fatalf("stale epoch = %d %s, want 409 epoch_moved", r.status, r.raw)
	}
	// a key is enrolled once
	r = f.do("POST", path, enrolBody(2, key1, owner, devicePIN), admin...)
	if r.status != http.StatusConflict || errCode(r) != "public_key_reused" {
		t.Fatalf("reused key = %d %s, want 409 public_key_reused", r.status, r.raw)
	}
	// a new enrolment raises the epoch and revokes the previous key
	r = f.do("POST", path, enrolBody(2, key2, owner, devicePIN), admin...)
	if r.status != http.StatusCreated {
		t.Fatalf("re-enrol = %d %s", r.status, r.raw)
	}
	prev := r.body["previous"].(map[string]any)
	if devNum(r.body["device"].(map[string]any), "device_epoch") != 2 || devNum(prev, "device_epoch") != 1 ||
		prev["revoked_at"] == nil || prev["revoke_reason"] != "re-enrolled" {
		t.Errorf("re-enrol answer = %s", r.raw)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_register_devices WHERE register_id = $1 AND revoked_at IS NULL`, f.register); n != 1 {
		t.Errorf("live enrolments = %d, want 1", n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE entity_id = $1 AND action IN
		('pos.register_device.enrolled', 'pos.register_device.revoked')`, entity); n != 3 {
		t.Errorf("device audit rows = %d, want 3 (two enrolments, one revocation)", n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM events_outbox WHERE entity_id = $1`, entity); n != 3 {
		t.Errorf("device events = %d, want 3", n)
	}
}

func TestOfflineDevice_RegisterOfAnotherBranchIsRefused(t *testing.T) {
	w := newWallFixture(t)
	deviceCleanup(t, w.fixture, w.register)
	owner, _, _, _ := seedManager(t, w.fixture, "owner", devicePIN)
	key, _ := newDeviceKey(t)
	sub := w.scoped[3]
	path := "/api/v1/pos/offline/registers/" + w.register + "/device"
	// an admin working in branch B reaches branch A's register through no
	// route (an admin naming no branch keeps every branch's reach, the
	// SEC-NOHEADER-1 rule); finance granted only branch B is held to its
	// grants with no header
	for _, hdr := range [][]string{
		{"X-Test-Role", "admin", "X-Test-Sub", sub, "X-Branch-Id", w.branchB.String()},
		{"X-Test-Role", "owner", "X-Test-Sub", sub, "X-Branch-Id", w.branchB.String()},
	} {
		for _, c := range []struct {
			method, path string
			body         any
		}{{"GET", path, nil}, {"POST", path, enrolBody(1, key, owner, devicePIN)},
			{"POST", path + "/revoke", map[string]any{"device_epoch": 1, "reason": "lost", "manager": owner, "pin": devicePIN}}} {
			r := w.do(c.method, c.path, c.body, hdr...)
			if r.status != http.StatusForbidden {
				t.Errorf("%s %s from branch B (%d headers) = %d %s, want 403", c.method, c.path, len(hdr), r.status, r.raw)
			}
		}
	}
	if r := w.do("GET", path, nil, "X-Test-Role", "finance", "X-Test-Sub", sub); r.status != http.StatusForbidden {
		t.Errorf("finance of branch B naming no branch = %d %s, want 403", r.status, r.raw)
	}
	if n := countOf(t, w.db, `SELECT count(*) FROM pos_register_devices WHERE register_id = $1`, w.register); n != 0 {
		t.Errorf("a refused enrolment wrote %d rows", n)
	}
	// inside its own branch the enrolment lands
	if r := w.do("POST", path, enrolBody(1, key, owner, devicePIN), "X-Test-Role", "admin", "X-Test-Sub", mustUUID(t)); r.status != http.StatusCreated {
		t.Errorf("own branch enrolment = %d %s", r.status, r.raw)
	}
}

// fakeSessionEnder stands in for ADR 0012's session store.
type fakeSessionEnder struct {
	mu    sync.Mutex
	calls []string
}

func (e *fakeSessionEnder) EndRegisterSessions(_ context.Context, register string, epoch int64, actor string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, register)
	return 2, nil
}

func TestOfflineDevice_RevocationSendsTheOldKeysSalesToTheQueue(t *testing.T) {
	testutil.LockOutboxTables(t)
	ender := &fakeSessionEnder{}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) { f.service.WithRegisterSessionEnder(ender) })
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	owner, _, _, _ := seedManager(t, f, "owner", devicePIN)
	admin := []string{"X-Test-Role", "admin", "X-Test-Sub", mustUUID(t)}
	path := "/api/v1/pos/offline/registers/" + b.register + "/device"

	// a sale before the revocation posts
	_, first := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	wantOutcome(t, b.sync(t, b.sign(t, first)), first, pos.OfflinePosted, "")

	r := f.do("POST", path+"/revoke", map[string]any{"device_epoch": 1, "reason": "lost", "manager": owner, "pin": devicePIN}, admin...)
	if r.status != http.StatusOK {
		t.Fatalf("revoke = %d %s", r.status, r.raw)
	}
	sessions := r.body["sessions"].(map[string]any)
	if r.body["device"] != nil || r.body["previous"].(map[string]any)["revoke_reason"] != "lost" ||
		sessions["supported"] != true || devNum(sessions, "ended") != 2 || num(t, r.body, "next_epoch") != 2 {
		t.Errorf("revoke answer = %s", r.raw)
	}
	if len(ender.calls) != 1 || ender.calls[0] != b.register {
		t.Errorf("session ender calls = %v, want one for %s", ender.calls, b.register)
	}
	// a second revocation of the same epoch finds nothing live
	r = f.do("POST", path+"/revoke", map[string]any{"device_epoch": 1, "reason": "again", "manager": owner, "pin": devicePIN}, admin...)
	if r.status != http.StatusConflict || errCode(r) != "device_not_enrolled" {
		t.Errorf("second revoke = %d %s, want 409 device_not_enrolled", r.status, r.raw)
	}

	// the old key's later sale goes to the queue, never dropped, never posted
	_, second := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	wantOutcome(t, b.sync(t, b.sign(t, second)), second, pos.OfflineRefused, "device_revoked")
	if got := b.postedCount(t); got != 1 {
		t.Errorf("posted after the revocation = %d, want 1", got)
	}

	// a new enrolment raises the epoch
	key, _ := newDeviceKey(t)
	r = f.do("POST", path, enrolBody(2, key, owner, devicePIN), admin...)
	if r.status != http.StatusCreated || devNum(r.body["device"].(map[string]any), "device_epoch") != 2 {
		t.Fatalf("enrol after revoke = %d %s", r.status, r.raw)
	}
	entity := uuid.NewSHA1(uuid.NameSpaceURL, []byte("pos_register_device:"+b.register))
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE entity_id = $1 AND action = 'pos.register_device.revoked'
		AND changes->>'reason' = 'lost'`, entity); n != 1 {
		t.Errorf("revocation audit rows = %d, want 1", n)
	}
	deviceCleanup(t, f, b.register)
}

func TestOfflineDevice_RevocationWithNoSessionStoreSaysSo(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	deviceCleanup(t, f, f.register)
	owner, _, _, _ := seedManager(t, f, "owner", devicePIN)
	admin := []string{"X-Test-Role", "owner", "X-Test-Sub", mustUUID(t)}
	path := "/api/v1/pos/offline/registers/" + f.register + "/device"
	key, _ := newDeviceKey(t)
	if r := f.do("POST", path, enrolBody(1, key, owner, devicePIN), admin...); r.status != http.StatusCreated {
		t.Fatalf("enrol = %d %s", r.status, r.raw)
	}
	r := f.do("POST", path+"/revoke", map[string]any{"device_epoch": 1, "reason": "stolen", "manager": owner, "pin": devicePIN}, admin...)
	if r.status != http.StatusOK {
		t.Fatalf("revoke = %d %s", r.status, r.raw)
	}
	s := r.body["sessions"].(map[string]any)
	if s["supported"] != false || !strings.Contains(s["message"].(string), "disable the user") {
		t.Errorf("sessions with no store = %v", s)
	}
}

func TestOfflineDevice_ReEnrolmentOverALostStoreLeavesTheUnknownTail(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	owner, _, _, _ := seedManager(t, f, "owner", devicePIN)
	admin := []string{"X-Test-Role", "admin", "X-Test-Sub", mustUUID(t)}
	path := "/api/v1/pos/offline/registers/" + b.register + "/device"

	_, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	_, s2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	b.sync(t, b.sign(t, s1), b.sign(t, s2))

	// the keychain says the device assigned five under epoch 1: three never came
	key, _ := newDeviceKey(t)
	body := enrolBody(2, key, owner, devicePIN)
	body["previous_epoch_mark"] = map[string]any{"device_epoch": 1, "last_seq": 5}
	r := f.do("POST", path, body, admin...)
	if r.status != http.StatusCreated || r.body["unknown_tail_exception_id"] == nil {
		t.Fatalf("re-enrol over a lost store = %d %s", r.status, r.raw)
	}
	var from, to, epoch int64
	var reason, status string
	var session *uuid.UUID
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT gap_from_seq, gap_to_seq, device_epoch, reason_code, status, till_session_id
		FROM pos_offline_exceptions WHERE id = $1`, r.body["unknown_tail_exception_id"]).
		Scan(&from, &to, &epoch, &reason, &status, &session); err != nil {
		t.Fatal(err)
	}
	if from != 3 || to != 5 || epoch != 1 || reason != "unknown_tail" || status != "open" || session == nil || *session != b.session {
		t.Errorf("unknown tail = %d..%d epoch %d %s/%s session %v, want 3..5 epoch 1 unknown_tail/open session %s",
			from, to, epoch, reason, status, session, b.session)
	}

	// a mark the server already holds leaves no item
	key3, _ := newDeviceKey(t)
	body = enrolBody(3, key3, owner, devicePIN)
	body["previous_epoch_mark"] = map[string]any{"device_epoch": 2, "last_seq": 0}
	r = f.do("POST", path, body, admin...)
	if r.status != http.StatusCreated || r.body["unknown_tail_exception_id"] != nil {
		t.Fatalf("re-enrol with nothing missing = %d %s", r.status, r.raw)
	}
	// a mark of an epoch the register is not on is refused
	key4, _ := newDeviceKey(t)
	body = enrolBody(4, key4, owner, devicePIN)
	body["previous_epoch_mark"] = map[string]any{"device_epoch": 1, "last_seq": 9}
	if r = f.do("POST", path, body, admin...); r.status != http.StatusBadRequest {
		t.Errorf("a mark of another epoch = %d %s, want 400", r.status, r.raw)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_offline_exceptions WHERE register_id = $1 AND reason_code = 'unknown_tail'`,
		b.register); n != 1 {
		t.Errorf("unknown tail items = %d, want 1", n)
	}
	deviceCleanup(t, f, b.register)
}

// Three managers enrol one register at once (pool size 4, three
// contenders): exactly one takes the next epoch, the other two are told the
// epoch moved, and one enrolment stays live.
func TestOfflineDevice_EnrolmentsRaceToOneEpoch(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	deviceCleanup(t, f, f.register)
	owner, _, _, _ := seedManager(t, f, "owner", devicePIN)
	var managers [3]string
	for i := range managers {
		managers[i], _, _, _ = seedManager(t, f, "admin", devicePIN)
	}
	admin := []string{"X-Test-Role", "admin", "X-Test-Sub", mustUUID(t)}
	path := "/api/v1/pos/offline/registers/" + f.register + "/device"
	key0, _ := newDeviceKey(t)
	if r := f.do("POST", path, enrolBody(1, key0, owner, devicePIN), admin...); r.status != http.StatusCreated {
		t.Fatalf("first enrol = %d %s", r.status, r.raw)
	}
	for round := 0; round < 4; round++ {
		live, _, maxEpoch := deviceState(t, f)
		// rounds two and four race with no live enrolment (revoked first),
		// so nothing but the register's own row serializes them
		if round%2 == 1 && live == 1 {
			r := f.do("POST", path+"/revoke", map[string]any{"device_epoch": maxEpoch, "reason": "race", "manager": owner,
				"pin": devicePIN}, admin...)
			if r.status != http.StatusOK {
				t.Fatalf("revoke before round %d = %d %s", round, r.status, r.raw)
			}
		}
		statuses := make([]int, 3)
		codes := make([]string, 3)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 3; i++ {
			key, _ := newDeviceKey(t)
			wg.Add(1)
			go func(i int, key string) {
				defer wg.Done()
				<-start
				// each contender its own manager: the roster row's lock
				// serializes nothing here, only the register's does
				r := f.do("POST", path, enrolBody(maxEpoch+1, key, managers[i], devicePIN), admin...)
				statuses[i], codes[i] = r.status, errCode(r)
			}(i, key)
		}
		close(start)
		wg.Wait()
		won := 0
		for i := range statuses {
			switch {
			case statuses[i] == http.StatusCreated:
				won++
			case statuses[i] != http.StatusConflict || codes[i] != "epoch_moved":
				t.Errorf("round %d contender %d = %d %s, want 201 or 409 epoch_moved", round, i, statuses[i], codes[i])
			}
		}
		live, total, newMax := deviceState(t, f)
		if won != 1 || live != 1 || newMax != maxEpoch+1 || total != newMax {
			t.Fatalf("round %d: %d won, %d live, max epoch %d, %d rows; want 1 won, 1 live, epoch %d, one row each",
				round, won, live, newMax, total, maxEpoch+1)
		}
	}
}

func deviceState(t *testing.T, f *fixture) (live, total, maxEpoch int64) {
	t.Helper()
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE revoked_at IS NULL), count(*),
		COALESCE(MAX(epoch), 0) FROM pos_register_devices WHERE register_id = $1`, f.register).Scan(&live, &total, &maxEpoch); err != nil {
		t.Fatal(err)
	}
	return live, total, maxEpoch
}

// numOf is a number of a decoded body, or -1 (safe off the test's own
// goroutine).
func devNum(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return n
		}
	case float64:
		return int64(v)
	}
	return -1
}

// errCode is the error envelope's code, or empty (safe off the test's own
// goroutine).
func errCode(r resp) string {
	e, _ := r.body["error"].(map[string]any)
	if d, _ := e["details"].([]any); len(d) > 0 {
		if dm, _ := d[0].(map[string]any); dm != nil {
			if c, _ := dm["code"].(string); c != "" {
				return c
			}
		}
	}
	code, _ := e["code"].(string)
	return code
}

// The server holds the device's reported mark (5) above
// its recorded mark (2); a re-enrolment that sends no keychain mark (a
// replaced device whose keychain is gone) must still leave the unknown tail
// (ADR 0011 5.1: the report less the server's recorded mark).
func TestOfflineDevice_UnknownTailFromTheServersReport(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	owner, _, _, _ := seedManager(t, f, "owner", devicePIN)
	admin := []string{"X-Test-Role", "admin", "X-Test-Sub", mustUUID(t)}
	path := "/api/v1/pos/offline/registers/" + b.register + "/device"
	_, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	_, s2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	b.sync(t, b.sign(t, s1), b.sign(t, s2))
	var reported int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT reported_seq FROM pos_register_devices WHERE register_id = $1 AND revoked_at IS NULL`,
		b.register).Scan(&reported); err != nil {
		t.Fatal(err)
	}
	t.Logf("server reported_seq = %d", reported)
	key, _ := newDeviceKey(t)
	r := f.do("POST", path, enrolBody(2, key, owner, devicePIN), admin...)
	if r.status != http.StatusCreated {
		t.Fatalf("re-enrol = %d %s", r.status, r.raw)
	}
	if r.body["unknown_tail_exception_id"] == nil {
		t.Errorf("re-enrol with the server's report at %d and its mark at 2 left no unknown tail: %s", reported, r.raw)
	}
	deviceCleanup(t, f, b.register)
}

// A manager revokes the lost device first, then
// re-enrols with the keychain's mark: the tail must still be recorded.
func TestOfflineDevice_UnknownTailAfterARevocation(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	owner, _, _, _ := seedManager(t, f, "owner", devicePIN)
	admin := []string{"X-Test-Role", "admin", "X-Test-Sub", mustUUID(t)}
	path := "/api/v1/pos/offline/registers/" + b.register + "/device"
	_, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	b.sync(t, b.sign(t, s1))
	if r := f.do("POST", path+"/revoke", map[string]any{"device_epoch": 1, "reason": "lost", "manager": owner, "pin": devicePIN},
		admin...); r.status != http.StatusOK {
		t.Fatalf("revoke = %d %s", r.status, r.raw)
	}
	key, _ := newDeviceKey(t)
	body := enrolBody(2, key, owner, devicePIN)
	body["previous_epoch_mark"] = map[string]any{"device_epoch": 1, "last_seq": 4}
	r := f.do("POST", path, body, admin...)
	if r.status != http.StatusCreated || r.body["unknown_tail_exception_id"] == nil {
		t.Errorf("re-enrol after a revocation with the keychain's mark at 4 (server at 1) = %d %s, want 201 with an unknown tail",
			r.status, r.raw)
	}
	deviceCleanup(t, f, b.register)
}

// A sync of the revoked epoch holds lock 0a on its row and raises the
// device's reported mark while a re-enrolment arrives: the enrolment waits
// for it and writes the tail to the mark the sync left, never the one it read
// before the lock.
func TestOfflineDevice_UnknownTailWaitsForASyncOfTheRevokedEpoch(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	owner, _, _, _ := seedManager(t, f, "owner", devicePIN)
	admin := []string{"X-Test-Role", "admin", "X-Test-Sub", mustUUID(t)}
	path := "/api/v1/pos/offline/registers/" + b.register + "/device"
	_, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	b.sync(t, b.sign(t, s1))
	if r := f.do("POST", path+"/revoke", map[string]any{"device_epoch": 1, "reason": "lost", "manager": owner, "pin": devicePIN},
		admin...); r.status != http.StatusOK {
		t.Fatalf("revoke = %d %s", r.status, r.raw)
	}

	// the sync in flight: lock 0a on epoch 1's row, its report raised to 6
	ctx := context.Background()
	tx, err := f.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM pos_register_devices WHERE register_id = $1 AND epoch = 1 FOR UPDATE`,
		b.register); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE pos_register_devices SET reported_seq = 6 WHERE register_id = $1 AND epoch = 1`,
		b.register); err != nil {
		t.Fatal(err)
	}
	key, _ := newDeviceKey(t)
	done := make(chan resp, 1)
	go func() { done <- f.do("POST", path, enrolBody(2, key, owner, devicePIN), admin...) }()
	select {
	case r := <-done:
		t.Fatalf("the enrolment did not wait for the sync's lock: %d %s", r.status, r.raw)
	case <-time.After(500 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.status != http.StatusCreated || r.body["unknown_tail_exception_id"] == nil {
		t.Fatalf("re-enrol after the sync = %d %s, want 201 with an unknown tail", r.status, r.raw)
	}
	var from, to int64
	if err := f.db.Pool.QueryRow(ctx, `SELECT gap_from_seq, gap_to_seq FROM pos_offline_exceptions WHERE id = $1`,
		r.body["unknown_tail_exception_id"]).Scan(&from, &to); err != nil {
		t.Fatal(err)
	}
	if from != 2 || to != 6 {
		t.Errorf("unknown tail = %d..%d, want 2..6 (the mark the sync left)", from, to)
	}
	deviceCleanup(t, f, b.register)
}
