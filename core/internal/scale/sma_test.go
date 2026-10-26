// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package scale_test

// Golden byte string proofs of the SMA codec (CP-19, ruling D8): the Scale
// Manufacturers Association Standard Scale Communication Protocol framing
// <LF><s><r><n><m><f><xxxxxx.xxx><uuu><CR> with the fixed ten character
// weight field, proven with no hardware and no simulator in the loop.

import (
	"testing"

	"github.com/gablelbm/gable/internal/scale"
)

func TestEncodeCommandFraming(t *testing.T) {
	for cmd, want := range map[scale.Command][]byte{
		scale.CmdWeigh:       []byte("\nW\r"),
		scale.CmdWeighStable: []byte("\nP\r"),
		scale.CmdZero:        []byte("\nZ\r"),
		scale.CmdTare:        []byte("\nT\r"),
	} {
		got := scale.EncodeCommand(cmd)
		if string(got) != string(want) {
			t.Errorf("command %q: got %q, want %q", string(cmd), got, want)
		}
	}
}

func TestParseStableGrossPounds(t *testing.T) {
	r, err := scale.ParseResponse([]byte("\n 1G   12400.500lb \r"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.Status != scale.StatusNone {
		t.Errorf("status: got %v", r.Status)
	}
	if r.Mode != scale.ModeGross || r.Motion {
		t.Errorf("mode/motion: got %v motion=%v", r.Mode, r.Motion)
	}
	if !r.HasWeight || r.WeightMilli != 12400500 {
		t.Errorf("weight: got has=%v milli=%d, want 12400500", r.HasWeight, r.WeightMilli)
	}
	if r.Unit != scale.UnitPound {
		t.Errorf("unit: got %q", r.Unit)
	}
}

func TestParseStableNetKilograms(t *testing.T) {
	r, err := scale.ParseResponse([]byte("\n 1N    8400.000kg \r"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.Mode != scale.ModeNet || r.Unit != scale.UnitKilogram {
		t.Errorf("got mode=%v unit=%q", r.Mode, r.Unit)
	}
	if r.WeightMilli != 8400000 {
		t.Errorf("weight: got %d", r.WeightMilli)
	}
}

func TestParseTareMode(t *testing.T) {
	r, err := scale.ParseResponse([]byte("\n 1T    3200.000lb \r"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.Mode != scale.ModeTare {
		t.Errorf("mode: got %v", r.Mode)
	}
}

func TestParseCenterOfZero(t *testing.T) {
	r, err := scale.ParseResponse([]byte("\nZ1G       0.000lb \r"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.Status != scale.StatusCenterOfZero {
		t.Errorf("status: got %v", r.Status)
	}
	if !r.HasWeight || r.WeightMilli != 0 {
		t.Errorf("weight: got has=%v milli=%d", r.HasWeight, r.WeightMilli)
	}
}

func TestParseMotionFlag(t *testing.T) {
	r, err := scale.ParseResponse([]byte("\n 1GM  12400.500lb \r"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !r.Motion {
		t.Error("motion flag not read")
	}
}

func TestParseNegativeWeight(t *testing.T) {
	r, err := scale.ParseResponse([]byte("\n 1G    -120.250lb \r"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.WeightMilli != -120250 {
		t.Errorf("weight: got %d", r.WeightMilli)
	}
}

// The error statuses: the weight field is not a number and must not be
// parsed as one, so the reading carries no weight.
func TestParseErrorStatusesCarryNoWeight(t *testing.T) {
	cases := map[byte]scale.Status{
		'O': scale.StatusOverCapacity,
		'U': scale.StatusUnderCapacity,
		'E': scale.StatusZeroError,
		'I': scale.StatusInitialZeroError,
		'T': scale.StatusTareError,
	}
	for c, want := range cases {
		frame := []byte("\n" + string(c) + "1G  **********lb \r")
		r, err := scale.ParseResponse(frame)
		if err != nil {
			t.Fatalf("status %c: parse: %v", c, err)
		}
		if r.Status != want {
			t.Errorf("status %c: got %v, want %v", c, r.Status, want)
		}
		if r.HasWeight {
			t.Errorf("status %c: a weight was parsed from a refusal frame", c)
		}
	}
}

func TestParseRejectsBadFraming(t *testing.T) {
	for name, frame := range map[string][]byte{
		"no line feed":       []byte(" 1G   12400.500lb \r"),
		"no carriage return": []byte("\n 1G   12400.500lb "),
		"short body":         []byte("\n 1G  12400.5lb \r"),
		"empty":              []byte(""),
	} {
		if _, err := scale.ParseResponse(frame); err == nil {
			t.Errorf("%s: %q parsed without error", name, frame)
		}
	}
}

func TestParseRejectsUnknownUnit(t *testing.T) {
	if _, err := scale.ParseResponse([]byte("\n 1G   12400.500zz \r")); err == nil {
		t.Error("an unknown unit code parsed without error")
	}
}

// A reading the driver must refuse: in motion, a refusal status, or no
// weight at all. The counter sells the number it is shown, so the refusal
// is the driver's, not the clerk's.
func TestReadingRefusals(t *testing.T) {
	stable := scale.Reading{Status: scale.StatusNone, Mode: scale.ModeGross, HasWeight: true, WeightMilli: 1000, Unit: scale.UnitPound}
	if err := stable.Refusal(); err != nil {
		t.Errorf("a stable gross reading refused: %v", err)
	}
	moving := stable
	moving.Motion = true
	if err := moving.Refusal(); err == nil {
		t.Error("a reading in motion was not refused")
	}
	over := stable
	over.Status = scale.StatusOverCapacity
	over.HasWeight = false
	if err := over.Refusal(); err == nil {
		t.Error("an over capacity reading was not refused")
	}
	weightless := stable
	weightless.HasWeight = false
	if err := weightless.Refusal(); err == nil {
		t.Error("a reading with no weight was not refused")
	}
}

// A kilograms reading converts to pounds exactly: a misread unit field is a
// thousandfold pricing error, so the conversion is the driver's, recorded
// with the unit the indicator sent.
func TestReadingPounds(t *testing.T) {
	kg := scale.Reading{Status: scale.StatusNone, Mode: scale.ModeGross, HasWeight: true, WeightMilli: 1000000, Unit: scale.UnitKilogram}
	lb, err := kg.PoundsMilli()
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	// 1 kg = 2.204623 lb at the reading's milli scale, rounded to the
	// thousandth of a pound.
	if lb != 2204623 {
		t.Errorf("got %d milli lb, want 2204623", lb)
	}
	lbReading := scale.Reading{HasWeight: true, WeightMilli: 5000, Unit: scale.UnitPound}
	got, err := lbReading.PoundsMilli()
	if err != nil || got != 5000 {
		t.Errorf("pounds passthrough: got %d, %v", got, err)
	}
}
