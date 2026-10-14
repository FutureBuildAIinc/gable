// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ai

import (
	"errors"
	"fmt"
	"net/http"
)

// CodeAIRefused is the stable wire code every AI feature answers when the
// provider refused the saved key (a 401 or 403 from the provider). It is not
// AI off: a key is saved, and the provider did not accept it. Clients branch
// on the code, never on the message.
const CodeAIRefused = "ai_refused"

// ErrKeyRefused is the sentinel every provider refusal matches (errors.Is).
var ErrKeyRefused = errors.New("the AI provider refused the saved key")

// RefusedError is a provider's refusal of the saved key. Message is the plain
// sentence a person reads (RefusedMessage); Error() is the provider's own
// words for logs and the admin's detail, scrubbed of the key before it lands.
type RefusedError struct {
	Provider string
	Message  string
	Detail   string
}

func (e *RefusedError) Error() string        { return e.Detail }
func (e *RefusedError) Is(target error) bool { return target == ErrKeyRefused }

// refusalWords is what a person reads when provider (a display name) refused
// the saved key.
func refusalWords(provider string) string {
	return fmt.Sprintf("%s refused the saved key. An administrator can enter it again in Tech Admin, AI Settings.", provider)
}

// refusedIfAuth answers a RefusedError for a provider answer that means the
// key is not accepted (401 unauthorized, 403 forbidden), and nil otherwise.
func refusedIfAuth(provider string, status int, detail string) error {
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return nil
	}
	return &RefusedError{Provider: provider, Message: refusalWords(provider), Detail: fmt.Sprintf("%s API error (%d): %s", provider, status, detail)}
}

// RefusedMessage reports whether err is a provider refusal of the saved key
// and the sentence to show. A wrapped error is found through errors.As.
func RefusedMessage(err error) (string, bool) {
	var re *RefusedError
	if errors.As(err, &re) {
		return re.Message, true
	}
	return "", false
}
