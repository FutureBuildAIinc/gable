// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package scale is the counter's scale link (CP-19, ruling D8): a reading
// type that carries the weight, the unit, the status flags and the mode, a
// codec for the Scale Manufacturers Association Standard Scale Communication
// Protocol (SMA), a driver interface small enough that a second protocol
// drops in without touching the counter, and a simulator that proves the
// driver with no hardware. The driver lives in the core so the weight, the
// ticket number and the sale line are written in one transaction and the
// read path survives a lost counter terminal.
package scale

import "fmt"

// Unit is the weight unit the indicator reported. The unit code rides every
// reading: a pounds and a kilograms reading are never confused.
type Unit string

const (
	UnitPound    Unit = "lb"
	UnitKilogram Unit = "kg"
)

// Mode is what the reported weight measures.
type Mode int

const (
	ModeGross Mode = iota
	ModeTare
	ModeNet
)

// Status is the scale condition the first character of an SMA response
// carries. StatusNone (a space on the wire) and StatusCenterOfZero leave the
// weight parseable; the rest are refusals whose weight field is not a
// number.
type Status int

const (
	StatusNone Status = iota
	StatusCenterOfZero
	StatusOverCapacity
	StatusUnderCapacity
	StatusZeroError
	StatusInitialZeroError
	StatusTareError
)

func (s Status) String() string {
	switch s {
	case StatusNone:
		return "ok"
	case StatusCenterOfZero:
		return "center of zero"
	case StatusOverCapacity:
		return "over capacity"
	case StatusUnderCapacity:
		return "under capacity"
	case StatusZeroError:
		return "zero error"
	case StatusInitialZeroError:
		return "initial zero error"
	case StatusTareError:
		return "tare error"
	}
	return "unknown"
}

// Refused reports whether the status refuses to hand over a weight.
func (s Status) Refused() bool {
	switch s {
	case StatusOverCapacity, StatusUnderCapacity, StatusZeroError, StatusInitialZeroError, StatusTareError:
		return true
	}
	return false
}

// Reading is one answer from the indicator. WeightMilli is the weight in
// thousandths of Unit, meaningful only when HasWeight: an error status sends
// no number, and a driver must never turn that into a zero.
type Reading struct {
	WeightMilli int64
	Unit        Unit
	Mode        Mode
	Motion      bool
	Status      Status
	HasWeight   bool
}

// Refusal is why a reading cannot become money, or nil when it can. The
// rules are the legal ones: a load in motion is never sold, an error status
// is never sold, and a response without a weight is a refusal, not a zero.
func (r Reading) Refusal() error {
	if r.Status.Refused() {
		return fmt.Errorf("the scale refuses the reading: %s", r.Status)
	}
	if !r.HasWeight {
		return fmt.Errorf("the scale sent no weight")
	}
	if r.Motion {
		return fmt.Errorf("the load is in motion")
	}
	return nil
}

// PoundsMilli is the weight in thousandths of a pound, converting kilograms
// at the exact ratio 1 kg = 2.204623 lb (the reading's milli scale, rounded
// to the nearest thousandth of a pound).
func (r Reading) PoundsMilli() (int64, error) {
	if !r.HasWeight {
		return 0, fmt.Errorf("the reading carries no weight")
	}
	switch r.Unit {
	case UnitPound:
		return r.WeightMilli, nil
	case UnitKilogram:
		// 2_204_623 micro-pounds per milli-kilogram: kg milli * 2204623 /
		// 1_000_000, rounded half away from zero.
		num := r.WeightMilli * 2_204_623
		if num >= 0 {
			return (num + 500_000) / 1_000_000, nil
		}
		return (num - 500_000) / 1_000_000, nil
	}
	return 0, fmt.Errorf("unknown unit %q", r.Unit)
}
