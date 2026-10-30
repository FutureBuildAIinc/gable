// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package staffauth is the core's staff session layer (ADR 0012 section 2):
// it signs a person in through Appwrite server side, keeps the core's own
// session in staff_sessions and resolves a presented session id to the
// claims the guards read. Nothing Appwrite issued outlives the sign in.
package staffauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"sort"

	"github.com/golang-jwt/jwt/v5"

	"github.com/google/uuid"
)

// subjectNamespace is the fixed Gable namespace of the name based subject
// UUID. It is a constant of the product, not a secret.
var subjectNamespace = uuid.MustParse("6f1d2b7e-52a4-4a0c-9d3e-8c1b7a4e5f10")

// Subject is the version 5 UUID over the fixed namespace and
// "<projectId>:<userId>": the same Appwrite user always yields the same sub.
func Subject(projectID, userID string) uuid.UUID {
	return uuid.NewSHA1(subjectNamespace, []byte(projectID+":"+userID))
}

// roleOrder is the fixed order of Gable roles; a session's role list is held
// in it so that two reads of the same membership are equal.
var roleOrder = []string{"owner", "admin", "sales", "finance", "purchasing", "warehouse", "cashier", "driver"}

func sortRoles(roles []string) []string {
	rank := make(map[string]int, len(roleOrder))
	for i, r := range roleOrder {
		rank[r] = i
	}
	out := append([]string(nil), roles...)
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i]] < rank[out[j]] })
	return out
}

func equalRoles(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// newSessionID returns a fresh 256 bit random session id.
func newSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashID is the key of a session row: the SHA-256 of the id.
func hashID(id string) []byte {
	h := sha256.Sum256([]byte(id))
	return h[:]
}

func jwtSubject(sub string) jwt.RegisteredClaims { return jwt.RegisteredClaims{Subject: sub} }
