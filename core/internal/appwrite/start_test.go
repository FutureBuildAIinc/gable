// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package appwrite

import (
	"errors"
	"net/http"
	"testing"
)

func versionClient(t *testing.T, version string) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"` + version + `"}`))
	})
	return newTestServer(t, mux)
}

// A 2.x server at or above the floor opens every method.
func TestCheckStartAtFloorOpens(t *testing.T) {
	for _, v := range []string{"2.0.0", "2.1.0"} {
		state, err := CheckStart(t.Context(), versionClient(t, v))
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if state != StartReady {
			t.Fatalf("%s: state = %v, want StartReady", v, state)
		}
	}
}

// A readable version below the floor refuses the start.
func TestCheckStartBelowFloorRefuses(t *testing.T) {
	for _, v := range []string{"1.9.0", "1.5.3"} {
		_, err := CheckStart(t.Context(), versionClient(t, v))
		if !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("%s: err = %v, want ErrUnsupportedVersion", v, err)
		}
	}
}

// An unreadable version starts the core with the provider redirect methods
// closed (ADR 0012 section 8).
func TestCheckStartUnreachableClosesRedirects(t *testing.T) {
	c := &Client{Endpoint: "http://127.0.0.1:1", ProjectID: "proj", APIKey: "k"}
	state, err := CheckStart(t.Context(), c)
	if err != nil {
		t.Fatalf("unreachable must not refuse the start: %v", err)
	}
	if state != StartRedirectsClosed {
		t.Fatalf("state = %v, want StartRedirectsClosed", state)
	}
}

// An answer that is not a version (a proxy's page, a garbled body) is
// unreadable, not a pass.
func TestCheckStartUnparseableClosesRedirects(t *testing.T) {
	state, err := CheckStart(t.Context(), versionClient(t, "not-a-version"))
	if err != nil {
		t.Fatalf("an unparseable version is unreadable, not below the floor: %v", err)
	}
	if state != StartRedirectsClosed {
		t.Fatalf("state = %v, want StartRedirectsClosed", state)
	}
}

func TestAdmitRefusesMFA(t *testing.T) {
	u := User{ID: "u1", Status: true, EmailVerification: true, MFA: true}
	err := Admit(u, Membership{Roles: []string{"gable-sales"}, Confirmed: true}, true)
	if !errors.Is(err, ErrMFAEnabled) {
		t.Fatalf("Admit() = %v, want ErrMFAEnabled", err)
	}
}
