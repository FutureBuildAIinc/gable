// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//go:build appwrite

package appwrite

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// The slice 4 proof: the redirect sign in calls against the live Appwrite
// 2.1.0 stack, with a test only OIDC issuer standing in for the provider and
// the stack's mail sink standing in for the member's inbox.

const (
	stubIssuerPort = "8582"
	mailSinkAPI    = "http://127.0.0.1:8581"
	callbackURL    = proofEndpoint + "/api/v1/auth/callback"
)

// oidcStub is a minimal OpenID Connect issuer. Appwrite, inside its
// container, reaches it as host.docker.internal; the test's browser stand in
// reaches it through a dialer that maps that name to loopback. The identity it
// vouches for is whatever the test set last.
type oidcStub struct {
	mu       sync.Mutex
	email    string
	verified bool
	sub      string
	srv      *http.Server
}

func startOIDCStub(t *testing.T) *oidcStub {
	t.Helper()
	s := &oidcStub{}
	base := "http://host.docker.internal:" + stubIssuerPort
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 base,
			"authorization_endpoint": base + "/authorize",
			"token_endpoint":         base + "/token",
			"userinfo_endpoint":      base + "/userinfo",
		})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		back, err := url.Parse(q.Get("redirect_uri"))
		if err != nil || q.Get("state") == "" {
			http.Error(w, "bad authorize request", http.StatusBadRequest)
			return
		}
		bq := back.Query()
		bq.Set("code", "stub-code")
		bq.Set("state", q.Get("state"))
		back.RawQuery = bq.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "stub-access", "token_type": "Bearer", "expires_in": 600})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sub": s.sub, "email": s.email, "email_verified": s.verified, "name": "Proof person",
		})
	})
	ln, err := net.Listen("tcp", "0.0.0.0:"+stubIssuerPort)
	if err != nil {
		t.Fatalf("stub issuer listen: %v", err)
	}
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	t.Cleanup(func() { _ = s.srv.Close() })
	return s
}

func (s *oidcStub) vouch(sub, email string, verified bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sub, s.email, s.verified = sub, email, verified
}

// walkProvider plays the browser through a provider redirect: it opens the
// start address, follows every redirect by hand and stops at the first one
// that lands on Gable's callback, whose query it returns.
func walkProvider(ctx context.Context, t *testing.T, startURL string) url.Values {
	t.Helper()
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	hc := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "host.docker.internal:") {
				addr = "127.0.0.1:" + strings.TrimPrefix(addr, "host.docker.internal:")
			}
			return dialer.DialContext(ctx, network, addr)
		}},
		Timeout: 30 * time.Second,
	}
	next := startURL
	for hop := 0; hop < 10; hop++ {
		if strings.HasPrefix(next, callbackURL) {
			u, err := url.Parse(next)
			if err != nil {
				t.Fatal(err)
			}
			return u.Query()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatalf("hop %d to %s: %v", hop, next, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		if loc == "" {
			t.Fatalf("hop %d to %s: %d with no Location: %s", hop, next, resp.StatusCode, body)
		}
		nu, err := req.URL.Parse(loc)
		if err != nil {
			t.Fatal(err)
		}
		next = nu.String()
	}
	t.Fatal("provider walk did not reach the callback in 10 hops")
	return nil
}

func userTotal(ctx context.Context, t *testing.T, seed proofSeed) int {
	t.Helper()
	code, body := rawRequest(ctx, http.MethodGet, seed.Endpoint+"/v1/users?queries[]="+url.QueryEscape(`{"method":"limit","values":[1]}`), nil,
		map[string]string{"X-Appwrite-Project": seed.ProjectID, "X-Appwrite-Key": seed.APIKey})
	if code != http.StatusOK {
		t.Fatalf("users.list = %d: %s", code, body)
	}
	var out struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out.Total
}

// mailLinkFor reads the sink until a message to addr carries a link to Gable's
// callback with the given state, and returns that link. Earlier proof steps
// mail the same address, so the state picks out this step's message.
func mailLinkFor(ctx context.Context, t *testing.T, addr, state string) string {
	t.Helper()
	link := regexp.MustCompile(`https?://[^\s"'<>]*/api/v1/auth/callback\?[^\s"'<>]+`)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		code, body := rawRequest(ctx, http.MethodGet, mailSinkAPI+"/email", nil, nil)
		if code == http.StatusOK {
			var mails []struct {
				To []struct {
					Address string `json:"address"`
				} `json:"to"`
				HTML string `json:"html"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(body), &mails); err == nil {
				for _, m := range mails {
					for _, to := range m.To {
						if !strings.EqualFold(to.Address, addr) {
							continue
						}
						l := strings.ReplaceAll(link.FindString(m.HTML+" "+m.Text), "&amp;", "&")
						if strings.Contains(l, "state="+state) {
							return l
						}
					}
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("no sign in link mailed to %s within 90s", addr)
	return ""
}

// proveRedirects is the ADR 0012 slice 4 proof, run against the stack
// TestProof has booted and seeded.
func proveRedirects(ctx context.Context, t *testing.T, seed proofSeed, client *Client) {
	t.Helper()
	stub := startOIDCStub(t)
	sales := seed.Users["sales"]
	const state = "proofstate123"
	success := callbackURL + "?state=" + state
	failure := callbackURL + "?state=" + state + "&failed=1"

	// (i) A provider round trip: the OAuth2 token URL's route and parameters
	// are accepted, Appwrite lands on Gable's callback keeping the state query
	// with the user id and one time secret, and the key call spends them.
	stub.vouch("proof-sub-sales", sales.Email, true)
	q := walkProvider(ctx, t, client.OAuth2TokenURL("oidc", success, failure))
	if q.Get("failed") != "" || q.Get("state") != state || q.Get("userId") == "" || q.Get("secret") == "" {
		t.Fatalf("provider round trip landed on %v, want state, userId and secret on the success URL", q)
	}
	session, err := client.CreateSessionFromToken(ctx, q.Get("userId"), q.Get("secret"))
	if err != nil {
		t.Fatalf("CreateSessionFromToken: %v", err)
	}
	if session.UserID != sales.ID {
		t.Fatalf("a verified provider address for a member opened %s, want the member %s", session.UserID, sales.ID)
	}
	if _, err := client.CreateSessionFromToken(ctx, q.Get("userId"), q.Get("secret")); err == nil {
		t.Fatal("the one time secret was spent twice")
	}
	user, err := client.GetUser(ctx, session.UserID)
	if err != nil {
		t.Fatal(err)
	}
	m, found, err := client.ListMemberships(ctx, seed.TeamID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := Admit(user, m, found); err != nil {
		t.Fatalf("the member from the provider round trip was not admitted: %v", err)
	}
	if err := client.DeleteSessionAsUser(ctx, session.ID, session.Secret); err != nil {
		t.Fatal(err)
	}
	t.Logf("provider round trip: state kept, session spent once for %s, member admitted", sales.ID)

	// (j) A stranger whose provider address is unverified but matches a
	// member's: Appwrite must not hand the member's account over. Whatever it
	// does, no session for the member comes out, and Gable's admission refuses
	// whoever does.
	before := userTotal(ctx, t, seed)
	stub.vouch("proof-sub-stranger", seed.Users["finance"].Email, false)
	q = walkProvider(ctx, t, client.OAuth2TokenURL("oidc", success, failure))
	t.Logf("unverified provider address landed on %v", q)
	if q.Get("failed") != "1" || q.Get("userId") != "" || q.Get("secret") != "" {
		t.Fatalf("an unverified provider address for a member landed on %v, want the failure URL with no secret", q)
	}
	if after := userTotal(ctx, t, seed); after != before {
		t.Fatalf("the refused sign in changed users from %d to %d", before, after)
	}
	// The control: the same member address, vouched as verified, is accepted,
	// so the refusal above is the verified flag and nothing else.
	stub.vouch("proof-sub-finance", seed.Users["finance"].Email, true)
	q = walkProvider(ctx, t, client.OAuth2TokenURL("oidc", success, failure))
	fs, err := client.CreateSessionFromToken(ctx, q.Get("userId"), q.Get("secret"))
	if err != nil || fs.UserID != seed.Users["finance"].ID {
		t.Fatalf("control: verified address for finance gave %+v, err %v (landed %v)", fs, err, q)
	}
	_ = client.DeleteSessionAsUser(ctx, fs.ID, fs.Secret)
	t.Log("unverified provider address for a member: refused at the failure URL, no secret, no user created")

	// (k) A magic link for a member: the key call mails it, the link lands on
	// Gable's callback with the state, the user id and the secret, no user is
	// created, and redeeming it opens the member's own session.
	before = userTotal(ctx, t, seed)
	if err := client.CreateMagicURLToken(ctx, sales.ID, sales.Email, success); err != nil {
		t.Fatalf("CreateMagicURLToken: %v", err)
	}
	link := mailLinkFor(ctx, t, sales.Email, state)
	lu, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	lq := lu.Query()
	if lq.Get("state") != state || lq.Get("userId") != sales.ID || lq.Get("secret") == "" {
		t.Fatalf("mailed link carries %v, want state, the member's userId and a secret", lq)
	}
	if after := userTotal(ctx, t, seed); after != before {
		t.Fatalf("magic link for an existing id changed users from %d to %d", before, after)
	}
	ms, err := client.CreateSessionFromToken(ctx, lq.Get("userId"), lq.Get("secret"))
	if err != nil {
		t.Fatalf("redeeming the mailed link: %v", err)
	}
	if ms.UserID != sales.ID {
		t.Fatalf("mailed link opened %s, want %s", ms.UserID, sales.ID)
	}
	_ = client.DeleteSessionAsUser(ctx, ms.ID, ms.Secret)
	t.Log("magic link: mailed to the member, redeemed once for the member, no user created")

	// (l) An unknown email: the lookup finds nobody and users.list is unchanged.
	before = userTotal(ctx, t, seed)
	if _, found, err := client.FindUserByEmail(ctx, "nobody-here@s9proof.example"); err != nil || found {
		t.Fatalf("FindUserByEmail(unknown) = found %v, err %v", found, err)
	}
	if got, found, err := client.FindUserByEmail(ctx, sales.Email); err != nil || !found || got.ID != sales.ID {
		t.Fatalf("FindUserByEmail(member) = %+v, found %v, err %v", got, found, err)
	}
	if after := userTotal(ctx, t, seed); after != before {
		t.Fatalf("lookups changed users from %d to %d", before, after)
	}
	t.Log(fmt.Sprintf("unknown email: nobody found, users.list stays at %d", before))
}
