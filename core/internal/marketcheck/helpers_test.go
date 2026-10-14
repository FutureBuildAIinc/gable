// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package marketcheck_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/marketcheck"
	"github.com/google/uuid"
)

const testHook = "test-hook-0001"

// memStore is an in memory system_settings.
type memStore struct {
	mu   sync.Mutex
	rows map[string]string
}

func newMemStore() *memStore { return &memStore{rows: map[string]string{}} }

func (m *memStore) Read(_ context.Context, k string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.rows[k]
	return v, ok, nil
}
func (m *memStore) Write(_ context.Context, k, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[k] = v
	return nil
}
func (m *memStore) Delete(_ context.Context, k string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, k)
	return nil
}

// fakeVault seals with base64 under the shared envelope.
type fakeVault struct{ present bool }

func (v fakeVault) Present() bool { return v.present }
func (v fakeVault) Seal(p string) (string, error) {
	return "enc:v1:" + base64.StdEncoding.EncodeToString([]byte(p)), nil
}
func (v fakeVault) Open(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, "enc:v1:"))
	return string(b), err
}

type memCatalog struct {
	yard     marketcheck.Yard
	products map[uuid.UUID]marketcheck.Product
}

func (c *memCatalog) Yard(context.Context) (marketcheck.Yard, error) { return c.yard, nil }
func (c *memCatalog) Products(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]marketcheck.Product, error) {
	out := map[uuid.UUID]marketcheck.Product{}
	for _, id := range ids {
		if p, ok := c.products[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}

type passTx struct{}

func (passTx) RunInTx(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

func product(sku string, price, cost int64) marketcheck.Product {
	return marketcheck.Product{ID: uuid.New(), SKU: sku, Description: "Desc " + sku, Category: "Lumber",
		UOM: "PCS", PriceCents: price, CostCents: cost}
}

func catalogOf(ps ...marketcheck.Product) *memCatalog {
	c := &memCatalog{yard: marketcheck.Yard{Name: "Kelowna Main Yard", Town: "Kelowna, BC"},
		products: map[uuid.UUID]marketcheck.Product{}}
	for _, p := range ps {
		c.products[p.ID] = p
	}
	return c
}

func ids(ps ...marketcheck.Product) []uuid.UUID {
	out := make([]uuid.UUID, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

// logCapture collects every log line.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}
func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func newLogger() (*slog.Logger, *logCapture) {
	c := &logCapture{}
	return slog.New(slog.NewTextHandler(c, &slog.HandlerOptions{Level: slog.LevelDebug})), c
}

// configured returns a service pointed at the stub, with settings saved.
func configured(t *testing.T, stub *flowStub, cat *memCatalog, mod func(*marketcheck.SettingsInput)) (*marketcheck.Service, *memStore, *logCapture) {
	t.Helper()
	st := newMemStore()
	lg, cap := newLogger()
	svc := marketcheck.NewService(st, cat).WithVault(fakeVault{present: true}).WithTxRunner(passTx{}).
		WithLogger(lg).WithBatchTimeout(2 * time.Second)
	hook := testHook
	in := marketcheck.SettingsInput{ConnectURL: ptr(stub.URL), WebhookID: &hook,
		StoreLabel: ptr("Home Depot, Spokane WA: nearest store with live prices")}
	if mod != nil {
		mod(&in)
	}
	if _, err := svc.SaveSettings(context.Background(), in); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	return svc, st, cap
}

func ptr[T any](v T) *T { return &v }

// flowStub answers each request with fn; it records bodies and paths.
type flowStub struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
	paths  []string
}

func newFlow(t *testing.T, fn func(w http.ResponseWriter, r *http.Request, body string)) *flowStub {
	t.Helper()
	f := &flowStub{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(b))
		f.paths = append(f.paths, r.Method+" "+r.URL.Path+" "+r.Header.Get("Content-Type"))
		f.mu.Unlock()
		fn(w, r, string(b))
	}))
	t.Cleanup(f.Close)
	return f
}

// matchAll answers every sent item as matched at the given price in dollars.
func matchAll(price float64) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, body string) {
		var skus []string
		for _, part := range strings.Split(body, `"sku":"`)[1:] {
			skus = append(skus, part[:strings.Index(part, `"`)])
		}
		var rows []string
		for _, s := range skus {
			rows = append(rows, fmt.Sprintf(`{"sku":%q,"status":"matched","match":{"title":"HD %s","theirPrice":%v,"listedPrice":%v,"link":"https://www.homedepot.com/p/%s","confidence":90,"normNote":""},"note":""}`, s, s, price, price, s))
		}
		_, _ = fmt.Fprintf(w, `{"store":{"id":"1234","name":"Spokane Store"},"rows":[%s]}`, strings.Join(rows, ","))
	}
}

func isErr(err error, sub string) bool {
	return err != nil && strings.Contains(err.Error(), sub)
}

var _ = errors.New
