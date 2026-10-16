// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ai

import "errors"

// CodeAIOff is the stable wire code every AI feature answers when AI cannot
// run: no key for the chosen provider, or the chosen provider does not offer
// the feature. Clients branch on it, never on the message.
const CodeAIOff = "ai_off"

// AIOffMessage is the plain sentence a person sees when AI is off.
const AIOffMessage = "AI is off. An administrator turns it on in Tech Admin, AI, by choosing a provider and entering its key."

// ErrAIOff is the sentinel every AI off error matches (errors.Is). The
// concrete error carries the sentence a person reads (see OffError).
var ErrAIOff = errors.New(AIOffMessage)

// OffError is an AI off answer with its own plain message (for example image
// generation, which the Anthropic provider does not offer). It matches
// ErrAIOff.
type OffError struct{ Message string }

func (e *OffError) Error() string        { return e.Message }
func (e *OffError) Is(target error) bool { return target == ErrAIOff }

// NewOff builds an AI off error with a message of its own.
func NewOff(message string) error { return &OffError{Message: message} }

// OffMessage reports whether err is an AI off answer and the sentence to
// show. A wrapped error is found through errors.As.
func OffMessage(err error) (string, bool) {
	var oe *OffError
	if errors.As(err, &oe) {
		return oe.Message, true
	}
	if errors.Is(err, ErrAIOff) {
		return AIOffMessage, true
	}
	return "", false
}

// ErrUnsupportedType is the sentinel for an upload the chosen provider cannot
// read (for example a BMP under Anthropic). The concrete error carries the
// words a person reads (see UnsupportedTypeMessage); nothing answers a canned
// result for it.
var ErrUnsupportedType = errors.New("unsupported file type")

// UnsupportedTypeError is an unsupported type with its own plain message.
type UnsupportedTypeError struct{ Message string }

func (e *UnsupportedTypeError) Error() string        { return e.Message }
func (e *UnsupportedTypeError) Is(target error) bool { return target == ErrUnsupportedType }

// UnsupportedTypeMessage reports whether err is an unsupported type answer
// and the sentence to show. A wrapped error is found through errors.As.
func UnsupportedTypeMessage(err error) (string, bool) {
	var ue *UnsupportedTypeError
	if errors.As(err, &ue) {
		return ue.Message, true
	}
	return "", false
}
