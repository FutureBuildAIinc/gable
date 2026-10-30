// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//go:build appwrite

package appwrite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	proofEndpoint = "http://127.0.0.1:8580"
	proofPassword = "s9proof-not-a-real-password"
)

type proofSeed struct {
	Endpoint  string `json:"endpoint"`
	ProjectID string `json:"projectId"`
	TeamID    string `json:"teamId"`
	APIKey    string `json:"apiKey"`
	Users     map[string]struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"users"`
	NonMember struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"nonMember"`
}

// repoRoot walks up from this source file until it finds the directory that
// holds the proof compose file.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "scripts/appwrite/docker-compose.yaml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

func run(ctx context.Context, t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}

// TestProof is the ADR 0012 slice 1 proof: it boots the local Appwrite stack
// from a clean slate, seeds it through setup.sh and asserts every slice 1
// behaviour against the live service. Run with:
//
//	go test -tags appwrite -timeout 25m ./internal/appwrite/...
func TestProof(t *testing.T) {
	root := repoRoot(t)
	compose := filepath.Join(root, "scripts/appwrite/docker-compose.yaml")

	// Clean slate, then up; the first image pull can take several minutes.
	downCtx, downCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	downCmd := exec.CommandContext(downCtx, "docker", "compose", "-p", "s9proof", "-f", compose, "down", "-v")
	downCmd.Dir = root
	_, _ = downCmd.CombinedOutput() // best effort: the stack may not exist yet
	downCancel()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", "compose", "-p", "s9proof", "-f", compose, "down", "-v")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("cleanup down -v failed: %v\n%s", err, out)
		}
	})

	upCtx, upCancel := context.WithTimeout(context.Background(), 15*time.Minute)
	run(upCtx, t, root, "docker", "compose", "-p", "s9proof", "-f", compose, "up", "-d", "--wait")
	upCancel()

	out := filepath.Join(t.TempDir(), "proof.json")
	setupCtx, setupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	run(setupCtx, t, root, "bash", "scripts/appwrite/setup.sh", "--endpoint", proofEndpoint, "--out", out)
	setupCancel()

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var seed proofSeed
	if err := json.Unmarshal(raw, &seed); err != nil {
		t.Fatal(err)
	}
	if seed.APIKey == "" || seed.ProjectID == "" || seed.TeamID == "" {
		t.Fatalf("setup output incomplete: %s", raw)
	}

	ctx := context.Background()
	client := &Client{Endpoint: seed.Endpoint, ProjectID: seed.ProjectID, APIKey: seed.APIKey}

	// (f) Version floor: the live stack is 2.1.0 and passes CheckFloor.
	version, err := client.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != "2.1.0" {
		t.Fatalf("Version() = %q, want 2.1.0", version)
	}
	if err := CheckFloor(version); err != nil {
		t.Fatalf("CheckFloor(%q): %v", version, err)
	}
	t.Logf("version read: %s, floor check passed", version)

	// (a) Server side email+password sign in with the key returns a session.
	sales := seed.Users["sales"]
	session, err := client.CreateEmailPasswordSession(ctx, sales.Email, proofPassword)
	if err != nil {
		t.Fatal(err)
	}
	if session.ID == "" || session.Secret == "" {
		t.Fatalf("session missing id or secret: %+v", session)
	}
	t.Logf("session created for %s (id %s)", sales.Email, session.ID)

	// (b) Read the account and the membership with the key; MapRoles maps
	// several seeded roles onto the matching Gable role.
	user, err := client.GetUser(ctx, session.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != sales.ID || !user.Status || !user.EmailVerification {
		t.Fatalf("unexpected user: %+v", user)
	}
	for _, role := range []string{"owner", "finance", "driver", "sales"} {
		u := seed.Users[role]
		m, found, err := client.ListMemberships(ctx, seed.TeamID, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("membership for %s not found", role)
		}
		if err := Admit(User{ID: u.ID, Status: true, EmailVerification: true}, m, found); err != nil {
			t.Fatalf("admit %s: %v", role, err)
		}
		got := MapRoles(m.Roles)
		if len(got) != 1 || got[0] != role {
			t.Fatalf("MapRoles for %s = %v", role, got)
		}
	}
	t.Log("memberships read; gable roles map correctly for owner, finance, driver, sales")

	// (c) End the session as the user with the session's own secret; the
	// secret stops working afterwards.
	if err := client.DeleteSessionAsUser(ctx, session.ID, session.Secret); err != nil {
		t.Fatal(err)
	}
	code, _ := rawRequest(ctx, http.MethodGet, seed.Endpoint+"/v1/account", nil, map[string]string{
		"X-Appwrite-Project": seed.ProjectID,
		"X-Appwrite-Session": session.Secret,
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("deleted session secret still works: GET /v1/account = %d, want 401", code)
	}
	t.Log("session ended as the user; the secret is refused afterwards")

	// (d) The non-member is refused by the admission decision with the fixed
	// refusal for an absent membership.
	_, found, err := client.ListMemberships(ctx, seed.TeamID, seed.NonMember.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("non-member unexpectedly has a membership")
	}
	nonMember, err := client.GetUser(ctx, seed.NonMember.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := Admit(nonMember, Membership{}, false); !errors.Is(err, ErrNoMembership) {
		t.Fatalf("non-member admission = %v, want ErrNoMembership", err)
	}
	t.Logf("non-member %s refused: %v", seed.NonMember.Email, ErrNoMembership)

	// (e) The redirect URL is accepted only on a platform listed hostname:
	// magic URL token creation validates its url param against the platform
	// list in 2.1.0.
	code, body := rawRequest(ctx, http.MethodPost, seed.Endpoint+"/v1/account/tokens/magic-url", map[string]any{
		"userId": "unique()",
		"email":  sales.Email,
		"url":    seed.Endpoint + "/api/v1/auth/callback",
	}, map[string]string{"X-Appwrite-Project": seed.ProjectID})
	if code != http.StatusCreated {
		t.Fatalf("magic-url on registered host = %d, want 201: %s", code, body)
	}
	code, body = rawRequest(ctx, http.MethodPost, seed.Endpoint+"/v1/account/tokens/magic-url", map[string]any{
		"userId": "unique()",
		"email":  sales.Email,
		"url":    "http://unregistered-host.example/cb",
	}, map[string]string{"X-Appwrite-Project": seed.ProjectID})
	if code != http.StatusBadRequest {
		t.Fatalf("magic-url on unregistered host = %d, want 400: %s", code, body)
	}
	t.Log("platform check: callback on 127.0.0.1 accepted, unregistered host refused")

	// (e2) The same check on the redirect method itself: createOAuth2Token
	// takes Gable's callback as its success and failure URL once the host is
	// a Web platform (a 301 to the provider), and refuses a foreign host.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	oauthStart := func(cb string) (int, string) {
		u := seed.Endpoint + "/v1/account/tokens/oauth2/google?success=" + url.QueryEscape(cb) + "&failure=" + url.QueryEscape(cb)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Appwrite-Project", seed.ProjectID)
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Location")
	}
	if code, loc := oauthStart(seed.Endpoint + "/api/v1/auth/callback"); code != http.StatusMovedPermanently || !strings.HasPrefix(loc, "https://accounts.google.com/") {
		t.Fatalf("oauth2 token start on registered host = %d to %q, want 301 to the provider", code, loc)
	}
	if code, _ := oauthStart("http://unregistered-host.example/api/v1/auth/callback"); code != http.StatusBadRequest {
		t.Fatalf("oauth2 token start on unregistered host = %d, want 400", code)
	}
	t.Log("oauth2 token start: callback accepted as the success URL, foreign host refused")

	// (g) The client email+password switch is OFF: a client call (no key) is
	// refused, while the key call still succeeds.
	code, body = rawRequest(ctx, http.MethodPost, seed.Endpoint+"/v1/account/sessions/email", map[string]any{
		"email":    sales.Email,
		"password": proofPassword,
	}, map[string]string{"X-Appwrite-Project": seed.ProjectID})
	if code != http.StatusNotImplemented {
		t.Fatalf("client email session with switch off = %d, want 501: %s", code, body)
	}
	if _, err := client.CreateEmailPasswordSession(ctx, sales.Email, proofPassword); err != nil {
		t.Fatalf("key email session with switch off: %v", err)
	}
	t.Log("key-vs-client proof: client refused (501), key session created")

	// (h) The client sign up stays closed while the switch is off, so no
	// stranger can register under an invitee's address.
	code, body = rawRequest(ctx, http.MethodPost, seed.Endpoint+"/v1/account", map[string]any{
		"userId":   "unique()",
		"email":    "stranger@s9proof.example",
		"password": proofPassword,
	}, map[string]string{"X-Appwrite-Project": seed.ProjectID})
	if code < 400 {
		t.Fatalf("client sign up with the switch off = %d, want a refusal: %s", code, body)
	}
	t.Logf("client sign up refused (%d)", code)

	// ADR 0012 slice 4: the redirect sign in methods.
	proveRedirects(ctx, t, seed, client)
}

// rawRequest performs one plain HTTP call and returns the status and body.
func rawRequest(ctx context.Context, method, url string, payload map[string]any, headers map[string]string) (int, string) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, fmt.Sprintf("marshal: %v", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, fmt.Sprintf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Sprintf("do: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
