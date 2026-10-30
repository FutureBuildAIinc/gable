// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package appwrite is Gable's client seam for the Appwrite identity provider
// (ADR 0012). It speaks the Appwrite HTTP API with the standard library only:
// server calls carry the project API key, user calls carry the session secret.
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
	"strconv"
	"strings"
)

// Client calls one Appwrite project. The zero values for Endpoint, ProjectID
// and APIKey are invalid; all three come from config (APPWRITE_ENDPOINT,
// APPWRITE_PROJECT_ID, APPWRITE_API_KEY).
type Client struct {
	Endpoint  string
	ProjectID string
	APIKey    string

	// HTTP is the transport; nil uses http.DefaultClient.
	HTTP *http.Client
}

// Error is a non-2xx Appwrite answer.
type Error struct {
	StatusCode int
	Type       string
	Message    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("appwrite: %s (status %d, type %s)", e.Message, e.StatusCode, e.Type)
}

// Session is an Appwrite account session. Secret is the value a client sends
// as X-Appwrite-Session.
type Session struct {
	ID     string
	UserID string
	Secret string
}

// User is the Appwrite account record Gable's admission decision reads.
type User struct {
	ID                string `json:"$id"`
	Email             string `json:"email"`
	Status            bool   `json:"status"`
	EmailVerification bool   `json:"emailVerification"`
	MFA               bool   `json:"mfa"`
}

// Membership is one user's place in the staff team.
type Membership struct {
	ID        string   `json:"$id"`
	UserID    string   `json:"userId"`
	Roles     []string `json:"roles"`
	Confirmed bool     `json:"confirm"`
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// do issues one call. key requests carry X-Appwrite-Key (server calls);
// sessionSecret, when set, goes out as X-Appwrite-Session instead (user
// calls). A non-2xx answer becomes *Error.
func (c *Client) do(ctx context.Context, method, path string, body any, key bool, sessionSecret string) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Endpoint, "/")+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.ProjectID != "" {
		req.Header.Set("X-Appwrite-Project", c.ProjectID)
	}
	if key && c.APIKey != "" {
		req.Header.Set("X-Appwrite-Key", c.APIKey)
	}
	if sessionSecret != "" {
		req.Header.Set("X-Appwrite-Session", sessionSecret)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &Error{StatusCode: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
		var parsed struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &parsed) == nil {
			apiErr.Type = parsed.Type
			if parsed.Message != "" {
				apiErr.Message = parsed.Message
			}
		}
		return nil, apiErr
	}
	return raw, nil
}

// Version reads GET /v1/health/version, a public route.
func (c *Client) Version(ctx context.Context) (string, error) {
	raw, err := c.do(ctx, http.MethodGet, "/v1/health/version", nil, false, "")
	if err != nil {
		return "", err
	}
	var out struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.Version, nil
}

// CreateEmailPasswordSession signs a user in server side. Key calls skip the
// project's client auth method switches, so this keeps working after the
// client email+password method is turned off.
func (c *Client) CreateEmailPasswordSession(ctx context.Context, email, password string) (Session, error) {
	raw, err := c.do(ctx, http.MethodPost, "/v1/account/sessions/email", map[string]string{
		"email":    email,
		"password": password,
	}, true, "")
	if err != nil {
		return Session{}, err
	}
	var out struct {
		ID     string `json:"$id"`
		UserID string `json:"userId"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Session{}, err
	}
	return Session{ID: out.ID, UserID: out.UserID, Secret: out.Secret}, nil
}

// GetUser reads one account with the key.
func (c *Client) GetUser(ctx context.Context, id string) (User, error) {
	raw, err := c.do(ctx, http.MethodGet, "/v1/users/"+id, nil, true, "")
	if err != nil {
		return User{}, err
	}
	var u User
	if err := json.Unmarshal(raw, &u); err != nil {
		return User{}, err
	}
	return u, nil
}

// ListMemberships returns the user's membership in the team; found is false
// only when Appwrite answered for that user alone and holds none. It asks for
// the one user (a team may span many pages); an answer that is not the whole
// of what was asked (more members than the page holds, the user not on it) is
// an error, never a definite miss. Memberships of an API created team carry
// the confirm flag as set at creation.
func (c *Client) ListMemberships(ctx context.Context, teamID, userID string) (m Membership, found bool, err error) {
	q := url.Values{}
	q.Add("queries[]", `equal("userId", [`+strconv.Quote(userID)+`])`)
	q.Add("queries[]", "limit(25)")
	raw, err := c.do(ctx, http.MethodGet, "/v1/teams/"+url.PathEscape(teamID)+"/memberships?"+q.Encode(), nil, true, "")
	if err != nil {
		return Membership{}, false, err
	}
	var out struct {
		Total       int          `json:"total"`
		Memberships []Membership `json:"memberships"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Membership{}, false, err
	}
	for _, candidate := range out.Memberships {
		if candidate.UserID == userID {
			return candidate, true, nil
		}
	}
	if out.Total > len(out.Memberships) {
		return Membership{}, false, errors.New("appwrite: membership list was not read whole")
	}
	return Membership{}, false, nil
}

// DeleteSessionAsUser ends a session as the user holding it: the session
// secret goes out as X-Appwrite-Session and the key is not sent.
func (c *Client) DeleteSessionAsUser(ctx context.Context, sessionID, secret string) error {
	_, err := c.do(ctx, http.MethodDelete, "/v1/account/sessions/"+sessionID, nil, false, secret)
	return err
}

// OAuth2TokenURL builds the address the browser navigates to so Appwrite runs
// the provider redirect for provider (ADR 0012 section 4). It is navigation
// only: the URL carries the project id and the two return URLs, never the API
// key, and nothing is called here. Appwrite appends the user id and the one
// time secret to success.
func (c *Client) OAuth2TokenURL(provider, success, failure string) string {
	q := url.Values{}
	q.Set("project", c.ProjectID)
	q.Set("success", success)
	q.Set("failure", failure)
	return strings.TrimRight(c.Endpoint, "/") + "/v1/account/tokens/oauth2/" + url.PathEscape(provider) + "?" + q.Encode()
}

// CreateSessionFromToken spends a user id and one time secret (an OAuth2 or
// magic URL token) for an Appwrite session, server side with the key.
func (c *Client) CreateSessionFromToken(ctx context.Context, userID, secret string) (Session, error) {
	raw, err := c.do(ctx, http.MethodPost, "/v1/account/sessions/token", map[string]string{
		"userId": userID,
		"secret": secret,
	}, true, "")
	if err != nil {
		return Session{}, err
	}
	var out struct {
		ID     string `json:"$id"`
		UserID string `json:"userId"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Session{}, err
	}
	return Session{ID: out.ID, UserID: out.UserID, Secret: out.Secret}, nil
}

// FindUserByEmail returns the user whose email is exactly email; found is
// false when Appwrite holds none. More than one match is an error, never a
// pick.
func (c *Client) FindUserByEmail(ctx context.Context, email string) (u User, found bool, err error) {
	q := url.Values{}
	q.Add("queries[]", `equal("email", [`+strconv.Quote(email)+`])`)
	q.Add("queries[]", "limit(2)")
	raw, err := c.do(ctx, http.MethodGet, "/v1/users?"+q.Encode(), nil, true, "")
	if err != nil {
		return User{}, false, err
	}
	var out struct {
		Users []User `json:"users"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return User{}, false, err
	}
	var match []User
	for _, candidate := range out.Users {
		if strings.EqualFold(candidate.Email, email) {
			match = append(match, candidate)
		}
	}
	switch len(match) {
	case 0:
		return User{}, false, nil
	case 1:
		return match[0], true, nil
	}
	return User{}, false, errors.New("appwrite: more than one user holds that email")
}

// CreateMagicURLToken asks Appwrite to email userID a sign in link whose
// target is redirectURL. The id must be an existing user's: Appwrite creates
// a user for an id it has not seen, so the caller never passes a new one.
func (c *Client) CreateMagicURLToken(ctx context.Context, userID, email, redirectURL string) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/account/tokens/magic-url", map[string]any{
		"userId": userID,
		"email":  email,
		"url":    redirectURL,
	}, true, "")
	return err
}
