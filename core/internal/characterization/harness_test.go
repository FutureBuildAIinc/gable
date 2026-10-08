// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

import (
	"bytes"
	"context"
	"crypto/rand"

	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// updateGoldens rewrites every golden file instead of comparing. Recording is
// an explicit, reviewable act: `go test ./internal/characterization -update`.
var updateGoldens = flag.Bool("update", false, "rewrite characterisation golden files")

const skipReason = "postgres unavailable: set DATABASE_URL to run integration tests"

// harness owns the whole throwaway environment: a database it creates and
// drops itself, the one core binary built from the working tree, and one
// server subprocess stopped by its process group.
type harness struct {
	t           *testing.T
	baseURL     string
	client      *http.Client
	serverCmd   *exec.Cmd
	serverLog   *os.File
	vars        map[string]string // substitution table: {name} in paths/bodies/headers
	reqCount    int
	backendRoot string
}

// goldensDBURL is the throwaway database this binary's harness runs against,
// set up once by TestMain (empty when DATABASE_URL is unset and the suite
// skips). The handle is package-level because the drop must live in TestMain
// regardless of which test owns the run: neither t.Cleanup nor TestMain's own
// defer runs on the `go test -timeout` panic path (the panic fires on the
// timer goroutine and takes the process down), so a timed-out run's database
// is reclaimed by the next run's sweep, not by teardown.
var (
	goldensDBURL  string
	goldensDBName string

	// seedDay is the UTC day the harness seeded on, captured once before the
	// seed binary runs. Every calendar date in a transcript normalises to its
	// offset from this day, and the end-of-run guard fails the run if UTC
	// midnight was crossed between seed and script.
	seedDay string
)

const dayLayout = "2006-01-02"

func TestMain(m *testing.M) {
	flag.Parse()

	teardown := setupGoldensDatabase()
	defer teardown() // ordinary paths; a timeout panic skips it (see setupGoldensDatabase)

	code := m.Run()
	teardown() // os.Exit below skips defers, so call it on the normal path too
	os.Exit(code)
}

// setupGoldensDatabase creates this run's throwaway database and returns a
// teardown that drops it. It first sweeps stale gv1_goldens_* databases left
// behind by earlier runs whose cleanup never ran: a `go test -timeout` panic
// happens on the timer goroutine, so neither t.Cleanup nor this function's
// own defer unwinds - the dropped-by-teardown path covers ordinary failures
// only, and the timeout-panic path leaks its database until a later run's
// sweep (or the operator) removes it.
//
// The sweep treats a database as stale only when no backend is connected to
// it. The admin connection this function holds is to the caller's DATABASE_URL
// database, not to any golden one, so that test protects exactly the live
// harnesses whose migrate/seed/server pool is already attached; a fresh
// database has no backend from its CREATE until the built binaries connect,
// so a concurrent run whose sweep passes inside that window could in
// principle drop it. That window is the go build that precedes the first
// connection; overlapping runs have not hit it, and a dropped database fails
// the affected run loudly rather than corrupting a golden.
func setupGoldensDatabase() func() {
	baseDBURL := os.Getenv("DATABASE_URL")
	if baseDBURL == "" {
		return func() {}
	}

	admin, err := sql.Open("pgx", baseDBURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "goldens: open admin connection: %v\n", err)
		os.Exit(1)
	}

	sweepStaleGoldensDBs(admin)

	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		fmt.Fprintf(os.Stderr, "goldens: generate db name: %v\n", err)
		os.Exit(1)
	}
	dbName := "gv1_goldens_" + hex.EncodeToString(suffix)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %q`, dbName)); err != nil {
		fmt.Fprintf(os.Stderr, "goldens: create database %s: %v\n", dbName, err)
		os.Exit(1)
	}

	freshURL, err := rewriteDBPath(baseDBURL, dbName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "goldens: rewrite DATABASE_URL: %v\n", err)
		os.Exit(1)
	}
	goldensDBURL = freshURL
	goldensDBName = dbName
	seedDay = time.Now().UTC().Format(dayLayout)

	var dropped bool
	return func() {
		if dropped || goldensDBName == "" {
			return
		}
		dropped = true
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		if _, err := admin.ExecContext(dropCtx, fmt.Sprintf(`DROP DATABASE %q WITH (FORCE)`, goldensDBName)); err != nil {
			fmt.Fprintf(os.Stderr, "goldens: drop database %s: %v\n", goldensDBName, err)
		}
		admin.Close()
	}
}

// sweepStaleGoldensDBs drops every gv1_goldens_* database with no connected
// backend. See setupGoldensDatabase for why "no backend" is the staleness
// test.
func sweepStaleGoldensDBs(admin *sql.DB) {
	rows, err := admin.Query(`SELECT datname FROM pg_database WHERE datname LIKE 'gv1_goldens_%'`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "goldens: sweep query: %v\n", err)
		return
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return
		}
		names = append(names, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return
	}

	for _, name := range names {
		var backends int
		if err := admin.QueryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname = $1`, name).Scan(&backends); err != nil {
			continue // unreadable: leave it alone, the operator can drop it
		}
		if backends > 0 {
			continue // a live harness owns it
		}
		if _, err := admin.Exec(fmt.Sprintf(`DROP DATABASE %q WITH (FORCE)`, name)); err != nil {
			fmt.Fprintf(os.Stderr, "goldens: sweep drop %s: %v\n", name, err)
		}
	}
}

func TestCharacterisationGoldens(t *testing.T) {
	if goldensDBURL == "" {
		t.Skip(skipReason)
	}

	h := newHarness(t, goldensDBURL)
	for _, g := range allGroups() {
		g := g
		t.Run(g.name, func(t *testing.T) {
			steps := h.runGroup(t, g)
			compareGoldens(t, g.name, steps)
		})
	}

	// Date-relative goldens are only meaningful when every request ran on the
	// day the seed ran: a run that crosses UTC midnight mid-script sees bucket
	// windows and day-offset dates shift under it. Fail loudly instead of
	// recording (or comparing against) a transcript that straddles the day
	// boundary; re-running gives a clean run.
	if today := time.Now().UTC().Format(dayLayout); today != seedDay {
		t.Fatalf("run crossed UTC midnight (seeded on %s, now %s): date-relative goldens are unreliable across the boundary; re-run the harness", seedDay, today)
	}
}

func newHarness(t *testing.T, freshURL string) *harness {
	t.Helper()

	// The database this harness uses was created by TestMain and is dropped by
	// its teardown, so parallel `go test ./...` packages (which share the CI
	// DATABASE_URL database) can never collide with it, and a test timeout
	// cannot leak it.
	_, thisFile, _, _ := runtime.Caller(0)
	charDir := filepath.Dir(thisFile)
	backendRoot := filepath.Join(charDir, "..", "..")

	buildDir := t.TempDir()
	// The one core binary, once: every role the harness runs (migrate, seed,
	// serve) is a subcommand of it, so the goldens exercise cmd/core's
	// dispatch and the exact entry point the image runs.
	run(t, backendRoot, subprocessEnv(nil), "go", "",
		"build", "-o", buildDir+string(os.PathSeparator), "./cmd/core")

	// The seed draws demo data through the global math/rand source. Go
	// auto-seeds it randomly at startup, which would make two runs of the
	// harness see different databases and the goldens meaningless; with the
	// deterministic seed the sequence is fixed, so the same seed data lands
	// every time. TZ is pinned so date values derived from time.Now() roll
	// over at the same instant on every machine.
	run(t, backendRoot, subprocessEnv(map[string]string{
		"DATABASE_URL": freshURL,
	}), buildDir+"/core", "", "migrate")

	run(t, backendRoot, subprocessEnv(map[string]string{
		"DATABASE_URL": freshURL,
		"DEMO_SEED":    "1",
		"GODEBUG":      "randautoseed=0",
		"TZ":           "Etc/UTC",
	}), buildDir+"/core", "", "seed")

	seedClockWindowFixtures(t, freshURL)

	vars := seedVars(t, freshURL)

	h := &harness{t: t, vars: vars, backendRoot: backendRoot}
	h.startServer(t, freshURL, buildDir)
	return h
}

// rewriteDBPath returns dbURL with its database path replaced by dbName.
func rewriteDBPath(dbURL, dbName string) (string, error) {
	u, err := url.Parse(dbURL)
	if err != nil {
		return "", err
	}
	u.Path = "/" + dbName
	return u.String(), nil
}

// envAllowList is the complete set of variable names a harness subprocess may
// inherit from the caller's environment. Everything else a developer's shell
// might hold (INTEGRATION_API_KEY, RUN_PAYMENTS_*, AVALARA_*, OPENROUTER_API_KEY,
// TWILIO_*, CORS_ORIGINS, ...) changes the server's configured behaviour and
// with it the goldens, so it never reaches the subprocess: the explicit map is
// the only other source, and it carries only harness-chosen values.
var envAllowList = []string{
	"PATH", "HOME", "GOCACHE", "GOMODCACHE", "GOFLAGS", "GOPATH", "TMPDIR",
}

// subprocessEnv builds the environment for every subprocess the harness runs
// (go build, migrate, seed, server): the allow list above, plus the explicit
// per-command values, and nothing else — never os.Environ().
func subprocessEnv(explicit map[string]string) []string {
	out := make([]string, 0, len(envAllowList)+len(explicit))
	for _, k := range envAllowList {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	for k, v := range explicit {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// run executes a helper binary, streaming its output into the test log so a
// failed migrate/seed/build explains itself.
func run(t *testing.T, dir string, env []string, name string, stdin string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		t.Logf("%s %s:\n%s", name, strings.Join(args, " "), out)
	}
	if err != nil {
		t.Fatalf("%s %s: %v", name, strings.Join(args, " "), err)
	}
}

// startServer runs the real server as a subprocess on a free port: the
// serve role of the one core binary built above. The whole wiring in
// internal/app/serve (middleware order, schedulers, adapters) is the
// behaviour under characterisation, so the harness exercises the
// production entry point rather than reconstructing the handler in
// process. The subprocess gets its own process group and is stopped by
// that group number, even on failure.
func (h *harness) startServer(t *testing.T, dbURL, buildDir string) {
	t.Helper()

	port, err := freePort()
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}

	srvDir := t.TempDir()
	logPath := filepath.Join(buildDir, "server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create server log: %v", err)
	}

	cmd := exec.Command(filepath.Join(buildDir, "core"), "serve")
	cmd.Dir = srvDir
	cmd.Env = subprocessEnv(map[string]string{
		"PORT":           strconv.Itoa(port),
		"DATABASE_URL":   dbURL,
		"AUTH_MODE":      "dev",
		"LOG_LEVEL":      "ERROR",
		"EDI_OUTPUT_DIR": filepath.Join(srvDir, "edi_out"),
		"TZ":             "Etc/UTC",
	})
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = serverProcAttr()
	// Pdeathsig is paired with the OS thread that calls Start: if the Go
	// runtime retires that thread, the kernel delivers the signal to a living
	// parent. Locking the thread around Start pins the pairing to a thread
	// that stays alive for the rest of the process.
	runtime.LockOSThread()
	err = cmd.Start()
	runtime.UnlockOSThread()
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	pgid := cmd.Process.Pid // Setpgid makes the pid the group leader's id

	t.Cleanup(func() {
		// SIGTERM lets the server drain; the fallback SIGKILL guarantees the
		// group is gone even if graceful shutdown hangs.
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			<-done
		}
		logFile.Close()
	})

	h.serverCmd = cmd
	h.serverLog = logFile
	h.baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	h.client = &http.Client{Jar: jar, Timeout: 60 * time.Second}

	h.waitReady(t, logPath)
}

func (h *harness) waitReady(t *testing.T, logPath string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := h.client.Get(h.baseURL + "/healthz/live")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		// Fail fast if the server already exited.
		if h.serverCmd.ProcessState != nil {
			t.Fatalf("server exited during startup; log:\n%s", readLog(logPath))
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("server not ready within 60s; log:\n%s", readLog(logPath))
}

func readLog(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(read log: %v)", err)
	}
	return string(b)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// seedVars resolves the deterministic anchors the scenarios reference: ids of
// seeded rows picked by natural key, plus today's UTC date. These values vary
// per database, so scenarios carry them as {placeholders} and the normaliser
// maps them onto stable ids in the goldens.
func seedVars(t *testing.T, dbURL string) map[string]string {
	t.Helper()
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	defer db.Close()

	vars := map[string]string{}
	one := func(v string, query string, args ...any) {
		var s sql.NullString
		if err := db.QueryRow(query, args...).Scan(&s); err != nil {
			t.Fatalf("seed var %s: %v", v, err)
		}
		vars[v] = s.String
	}

	one("customer", `SELECT id FROM customers WHERE account_number = 'KELBROOK-001'`)
	one("customerEmail", `SELECT email FROM customers WHERE account_number = 'KELBROOK-001'`)
	one("product", `SELECT id FROM products WHERE sku = 'LUM-248-PREM'`)
	one("productSheet", `SELECT id FROM products WHERE sku = 'PLY-34-CDX'`)
	one("branch", `SELECT id FROM locations WHERE code = 'KEL-MAIN'`)
	one("vendor", `SELECT id FROM vendors ORDER BY name LIMIT 1`)
	one("glAccount", `SELECT id FROM gl_accounts WHERE code = '1010'`)
	if vars["glAccount"] == "" { // fall back: lowest code in the chart
		one("glAccount", `SELECT id FROM gl_accounts ORDER BY code LIMIT 1`)
	}
	one("glAccount2", `SELECT id FROM gl_accounts WHERE code = '4010'`)
	if vars["glAccount2"] == "" {
		one("glAccount2", `SELECT id FROM gl_accounts ORDER BY code OFFSET 1 LIMIT 1`)
	}
	one("vehicle", `SELECT id FROM vehicles WHERE deleted_at IS NULL ORDER BY name LIMIT 1`)
	one("driver", `SELECT id FROM drivers WHERE deleted_at IS NULL ORDER BY name LIMIT 1`)
	one("marketIndex", `SELECT id FROM market_indices WHERE index_code = 'RL_SPF_2X4'`)
	// The seed day captured by TestMain, not "now at request time": the two
	// can only differ if the run crossed UTC midnight, which the end-of-run
	// guard rejects anyway.
	vars["today"] = seedDay
	return vars
}

// ---------------------------------------------------------------------------
// Request execution and transcript capture
// ---------------------------------------------------------------------------

type multipartDef struct {
	FieldName   string `json:"field"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Content     string `json:"content"`
}

type capturedRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    any               `json:"body,omitempty"`
}

type capturedResponse struct {
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Body        any    `json:"body"`
}

type capturedStep struct {
	Name string `json:"name"`
	// SortPrimaryArray, when true, sorts only the response's primary array
	// (the body's top-level array, or the data array of a paged envelope)
	// before comparison; arrays nested inside each element keep their wire
	// order and stay pinned. It is recorded in the golden so a reader can
	// see what is and is not pinned. Used only where the base's own ordering
	// is unstable: a list whose ORDER BY tiebreaks on random row ids (the
	// dispatch-day fixture rows all share one created_at).
	SortPrimaryArray bool `json:"sort_primary_array,omitempty"`
	// MaskCustomerIdentity, when true, replaces the values of the customer
	// identity fields (customer_id, customer_name, and a customer's name)
	// with a class placeholder before comparison. Used on the per-customer
	// report rows the demo seed fills from rand draws consumed inside
	// map-iteration loops: which customer owns which drawn amount is random
	// per run, while the amounts, buckets, counts and row order are not.
	// Recorded in the golden so a reader can see what is and is not pinned.
	MaskCustomerIdentity bool `json:"mask_customer_identity,omitempty"`
	// MaskOrderCount, when true, replaces the order_count field with a
	// placeholder before comparison. Used on the top-customers step only:
	// the script itself writes orders for one seeded customer (the quote and
	// integration converts), so the count on a rank owned by that customer
	// carries the script's own orders on top of the randomly assigned
	// segment's count - the revenue sequence and row order are stable, that
	// one count is not. Recorded in the golden, like the other mask flags.
	MaskOrderCount bool             `json:"mask_order_count,omitempty"`
	Request        capturedRequest  `json:"request"`
	Response       capturedResponse `json:"response"`
}

// doStep executes one scenario step: substitute {vars}, send, capture the
// exchange verbatim, and extract response values into {vars} for later steps.
func (h *harness) doStep(t *testing.T, s stepDef) capturedStep {
	t.Helper()

	path := h.subst(s.path)
	if !strings.HasPrefix(path, "/") {
		t.Fatalf("step %s: path must start with /: %q", s.name, path)
	}

	var bodyReader io.Reader
	var capturedBody any
	contentType := ""

	switch b := s.body.(type) {
	case nil:
	case *multipartDef:
		var buf bytes.Buffer
		w := multipartWriter(&buf, b)
		contentType = w.FormDataContentType()
		bodyReader = &buf
		capturedBody = b
	default:
		raw, err := json.Marshal(substAny(b, h.vars))
		if err != nil {
			t.Fatalf("step %s: marshal body: %v", s.name, err)
		}
		contentType = "application/json"
		bodyReader = bytes.NewReader(raw)
		capturedBody = decodeJSON(t, raw)
	}

	req, err := http.NewRequest(s.method, h.baseURL+path, bodyReader)
	if err != nil {
		t.Fatalf("step %s: build request: %v", s.name, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	capturedHeaders := map[string]string{}
	for k, v := range s.headers {
		v = h.subst(v)
		req.Header.Set(k, v)
		// The portal session cookie is implied by the login step; its value
		// is a rotating signature, so the golden records its presence only.
		if strings.EqualFold(k, "Cookie") {
			v = "<portal-session>"
		}
		capturedHeaders[k] = v
	}

	// The cookie jar (portal login) rides along automatically; record the
	// header as masked if the jar holds one for this host.
	if u, err := url.Parse(h.baseURL); err == nil {
		for _, c := range h.client.Jar.Cookies(u) {
			_ = c
			capturedHeaders["Cookie"] = "<portal-session>"
			break
		}
	}

	// The global rate limiter is per client IP (120/min). The script is
	// sequential and short, but rotating X-Forwarded-For keeps the count per
	// window well clear of the limit on slow CI machines, so a 429 can never
	// leak into a golden as a timing artefact.
	h.reqCount++
	req.Header.Set("X-Forwarded-For", fmt.Sprintf("10.43.0.%d", 1+h.reqCount/30))

	step := capturedStep{Name: s.name, Request: capturedRequest{
		Method:  s.method,
		Path:    path,
		Headers: capturedHeaders,
		Body:    capturedBody,
	}}

	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("step %s: %s %s: %v", s.name, s.method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		t.Fatalf("step %s: read response: %v", s.name, err)
	}

	step.Response = capturedResponse{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        captureBody(resp.Header.Get("Content-Type"), raw),
	}
	step.SortPrimaryArray = s.sortPrimaryArray
	step.MaskCustomerIdentity = s.maskCustomerIdentity
	if s.maskCustomerIdentity {
		maskCustomerIdentity(step.Response.Body)
	}
	step.MaskOrderCount = s.maskOrderCount
	if s.maskOrderCount {
		maskField(step.Response.Body, "order_count", "<orders>")
	}
	if s.sortPrimaryArray {
		switch body := step.Response.Body.(type) {
		case []any:
			sortPrimaryArray(body)
		case map[string]any:
			// Paged envelopes: the primary array sits one level down.
			if data, ok := body["data"].([]any); ok {
				sortPrimaryArray(data)
			}
		}
	}
	t.Logf("step %s: %s %s -> %d (%s)", s.name, s.method, path, resp.StatusCode, resp.Header.Get("Content-Type"))

	if s.extract != nil {
		doc := decodeJSON(t, raw)
		for v, ptr := range s.extract {
			val, err := jsonPoint(doc, ptr)
			if err != nil {
				t.Fatalf("step %s: extract %s from %s: %v", s.name, v, ptr, err)
			}
			h.vars[v] = fmt.Sprintf("%v", val)
		}
	}

	return step
}

// captureBody keeps JSON verbatim (decoded with UseNumber so number literals
// survive re-marshalling), text as a string, and binaries as a length plus a
// hash over the normalised bytes (see normaliseBytes).
func captureBody(contentType string, raw []byte) any {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if ct == "application/json" {
		var doc any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&doc); err == nil {
			return doc
		}
		// Invalid JSON still records the raw text.
		return map[string]any{"text": string(raw)}
	}
	if ct == "" || ct == "text/plain" || ct == "text/html" {
		return map[string]any{"text": string(raw)}
	}
	return map[string]any{"binary": binarySummary(raw)}
}

// runGroup executes a group's steps in order and returns the raw transcript.
func (h *harness) runGroup(t *testing.T, g groupDef) []capturedStep {
	t.Helper()
	steps := make([]capturedStep, 0, len(g.steps))
	for _, s := range g.steps {
		steps = append(steps, h.doStep(t, s))
	}
	return normaliseTranscript(steps)
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// reTodayToken matches {today+N} / {today-N} in scenario paths and bodies:
// clock-window scenarios ask for explicit windows relative to the seed day
// without the script pre-computing a var per offset.
var reTodayToken = regexp.MustCompile(`\{today([+-]\d+)\}`)

func expandTodayTokens(s string) string {
	return reTodayToken.ReplaceAllStringFunc(s, func(m string) string {
		off, err := strconv.Atoi(reTodayToken.FindStringSubmatch(m)[1])
		if err != nil {
			return m
		}
		return parseSeedDay(seedDay).AddDate(0, 0, off).Format(dayLayout)
	})
}

func (h *harness) subst(s string) string {
	return substAny(s, h.vars).(string)
}

// substAny replaces {name} tokens inside every string of a JSON-ish value.
func substAny(v any, vars map[string]string) any {
	switch x := v.(type) {
	case string:
		s := expandTodayTokens(x)
		for name, val := range vars {
			s = strings.ReplaceAll(s, "{"+name+"}", val)
		}
		return s
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = substAny(vv, vars)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = substAny(vv, vars)
		}
		return out
	case []map[string]any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = substAny(vv, vars)
		}
		return out
	default:
		return v
	}
}

// customerIdentityFields are the response fields that name a customer rather
// than describe an amount, bucket or count. On the per-customer report steps
// (AR aging, top customers, order activity) the demo seed randomises which
// customer owns which drawn value, so only the identity varies per run - and
// identity is exactly what those steps do not pin.
var customerIdentityFields = map[string]bool{
	"customer_id":   true,
	"customer_name": true,
	"name":          true,
}

// maskField rewrites every value under the named key, in place, to ph. It is
// the per-step mechanism behind MaskOrderCount; MaskCustomerIdentity has its
// own walker because it masks a set of keys.
func maskField(v any, key, ph string) {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			if k == key {
				x[k] = ph
				continue
			}
			maskField(vv, key, ph)
		}
	case []any:
		for _, vv := range x {
			maskField(vv, key, ph)
		}
	}
}

// maskCustomerIdentity rewrites the customer identity fields of a captured
// response body, in place, to the class placeholder "<customer>". It runs
// before normalisation, so a masked customer uuid never takes an <id-N>
// placeholder, and it touches nothing else: amounts, buckets, counts, dates
// and row order compare exactly as before. Two customers in one response both
// mask to "<customer>": the step pins the ledger, not the names on it.
func maskCustomerIdentity(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			if customerIdentityFields[k] {
				x[k] = "<customer>"
				continue
			}
			maskCustomerIdentity(vv)
		}
	case []any:
		for _, vv := range x {
			maskCustomerIdentity(vv)
		}
	}
}

// sortPrimaryArray sorts one response array (the body's top-level array, or
// the data array of a paged envelope) by the canonical encoding of its
// elements with volatile values (uuids, dates, timestamps, keys) masked to
// constants: the raw ids differ per run, so sorting on them would itself be
// random. Only the passed-in array is sorted - arrays nested inside each
// element keep their wire order, so a line-order regression still fails the
// golden. Sorting runs before normalisation assigns placeholders, so
// numbering follows the sorted order deterministically.
func sortPrimaryArray(arr []any) {
	sort.SliceStable(arr, func(i, j int) bool {
		bi, _ := json.Marshal(maskVolatile(arr[i]))
		bj, _ := json.Marshal(maskVolatile(arr[j]))
		return bytes.Compare(bi, bj) < 0
	})
}

// maskVolatile copies a decoded JSON value with run-varying strings replaced
// by per-class constants, for order-insensitive comparison keys.
func maskVolatile(v any) any {
	switch x := v.(type) {
	case string:
		return maskString(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = maskVolatile(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = maskVolatile(vv)
		}
		return out
	default:
		return v
	}
}

func maskString(s string) string {
	n := &normaliser{ids: map[string]string{}, seedDay: seedDay}
	s = reJWT.ReplaceAllString(s, "<jwt>")
	s = reKey.ReplaceAllString(s, "<api-key>")
	s = reUUID.ReplaceAllString(s, "<uuid>")
	// Timestamps and dates mask to their seed-day offsets, matching the
	// normaliser: two elements with different dates must order
	// deterministically, not tie on a shared placeholder and fall back to
	// the unstable wire order.
	s = reTS.ReplaceAllStringFunc(s, n.tsPlaceholder)
	s = rePgTS.ReplaceAllStringFunc(s, n.tsPlaceholder)
	s = reDate.ReplaceAllStringFunc(s, n.dayOffset)
	return s
}

func decodeJSON(t *testing.T, raw []byte) any {
	t.Helper()
	var doc any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode JSON: %v (body: %.400s)", err, raw)
	}
	return doc
}

// jsonPoint resolves a RFC 6901 JSON pointer against a decoded document.
func jsonPoint(doc any, ptr string) (any, error) {
	if ptr == "" {
		return doc, nil
	}
	cur := doc
	for _, tok := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		tok = strings.ReplaceAll(tok, "~1", "/")
		tok = strings.ReplaceAll(tok, "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[tok]
			if !ok {
				return nil, fmt.Errorf("key %q not found", tok)
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, fmt.Errorf("index %q out of range", tok)
			}
			cur = node[idx]
		default:
			return nil, fmt.Errorf("cannot descend into %T at %q", cur, tok)
		}
	}
	return cur, nil
}

// multipartWriter fills buf with the multipart/form-data body for one file
// part. The file content is fixed harness input, so it is recorded verbatim
// in the golden.
func multipartWriter(buf *bytes.Buffer, m *multipartDef) *multipart.Writer {
	w := multipart.NewWriter(buf)
	part, err := w.CreateFormFile(m.FieldName, m.Filename)
	if err != nil {
		panic(err) // harness bug, not runtime failure
	}
	if _, err := part.Write([]byte(m.Content)); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return w
}
