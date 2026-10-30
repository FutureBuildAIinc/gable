// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package scale

// The SMA wire codec: a request is <LF>, the command letter and <CR>; a
// response is <LF>, six status and mode characters, a fixed ten character
// weight field, a three character unit field and <CR>
// (<LF><s><r><n><m><f><xxxxxx.xxx><uuu><CR>). The serial default is 9600
// baud, eight data bits, no parity and one stop bit.

import (
	"fmt"
	"strconv"
	"strings"
)

// Command is an SMA request letter.
type Command byte

const (
	// CmdWeigh returns the current weight at once, stable or not.
	CmdWeigh Command = 'W'
	// CmdWeighStable returns the weight once the load is stable: the
	// command a ticket flow wants.
	CmdWeighStable Command = 'P'
	// CmdZero zeroes the indicator.
	CmdZero Command = 'Z'
	// CmdTare tares the indicator.
	CmdTare Command = 'T'
)

// EncodeCommand frames one request: <LF>, the letter, <CR>.
func EncodeCommand(cmd Command) []byte {
	return []byte{'\n', byte(cmd), '\r'}
}

// responseBodyLen is the fixed body between the framing bytes: the five
// status and mode characters <s><r><n><m><f>, ten of weight, three of unit.
const responseBodyLen = 5 + 10 + 3

// ParseResponse decodes one SMA response frame. A response whose status
// refuses the weight parses with HasWeight false: the weight field is not a
// number there and must not be parsed as one.
func ParseResponse(frame []byte) (Reading, error) {
	var r Reading
	if len(frame) != responseBodyLen+2 {
		return r, fmt.Errorf("an SMA response is %d bytes on the wire, got %d", responseBodyLen+2, len(frame))
	}
	if frame[0] != '\n' || frame[len(frame)-1] != '\r' {
		return r, fmt.Errorf("an SMA response is framed by a line feed and a carriage return")
	}
	body := string(frame[1 : len(frame)-1])

	status, err := parseStatus(body[0])
	if err != nil {
		return r, err
	}
	r.Status = status

	if body[1] < '1' || body[1] > '9' {
		return r, fmt.Errorf("range character %q is not a range number", body[1])
	}

	mode, err := parseMode(body[2])
	if err != nil {
		return r, err
	}
	r.Mode = mode

	switch body[3] {
	case 'M':
		r.Motion = true
	case ' ':
	default:
		return r, fmt.Errorf("motion character %q is neither motion nor stable", body[3])
	}

	// body[4] is reserved for future use by the standard.

	weightField := body[5:15]
	if status.Refused() {
		// An error status sends no number; the field is not parsed.
		return r, nil
	}
	milli, ok, err := parseWeightField(weightField)
	if err != nil {
		return r, err
	}
	r.HasWeight = ok
	r.WeightMilli = milli

	unit, err := parseUnit(body[15:18])
	if err != nil {
		return r, err
	}
	r.Unit = unit
	return r, nil
}

func parseStatus(c byte) (Status, error) {
	switch c {
	case ' ':
		return StatusNone, nil
	case 'Z':
		return StatusCenterOfZero, nil
	case 'O':
		return StatusOverCapacity, nil
	case 'U':
		return StatusUnderCapacity, nil
	case 'E':
		return StatusZeroError, nil
	case 'I':
		return StatusInitialZeroError, nil
	case 'T':
		return StatusTareError, nil
	}
	return StatusNone, fmt.Errorf("status character %q is not one the standard names", c)
}

func parseMode(c byte) (Mode, error) {
	switch c {
	case 'G', 'g':
		return ModeGross, nil
	case 'T':
		return ModeTare, nil
	case 'N', 'n':
		return ModeNet, nil
	}
	return ModeGross, fmt.Errorf("mode character %q is not gross, tare or net", c)
}

// parseWeightField reads the fixed ten character field: leading spaces, an
// optional sign and a decimal with up to three fraction digits. ok is false
// when the field carries no number at all (spaces or stars), which is how an
// indicator says the weight is not there.
func parseWeightField(field string) (milli int64, ok bool, err error) {
	trimmed := strings.TrimSpace(field)
	if trimmed == "" || strings.Contains(trimmed, "*") {
		return 0, false, nil
	}
	neg := false
	if strings.HasPrefix(trimmed, "-") {
		neg = true
		trimmed = strings.TrimPrefix(trimmed, "-")
	} else if strings.HasPrefix(trimmed, "+") {
		trimmed = strings.TrimPrefix(trimmed, "+")
	}
	intPart, fracPart, hasFrac := strings.Cut(trimmed, ".")
	if intPart == "" && fracPart == "" {
		return 0, false, fmt.Errorf("weight field %q holds no digits", field)
	}
	intVal, err := strconv.ParseInt(intPart, 10, 64)
	if intPart == "" {
		intVal, err = 0, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("weight field %q: %v", field, err)
	}
	milli = intVal * 1000
	if hasFrac {
		if len(fracPart) > 3 {
			return 0, false, fmt.Errorf("weight field %q carries more than three fraction digits", field)
		}
		for len(fracPart) < 3 {
			fracPart += "0"
		}
		fracVal, err := strconv.ParseInt(fracPart, 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("weight field %q: %v", field, err)
		}
		milli += fracVal
	}
	if neg {
		milli = -milli
	}
	return milli, true, nil
}

func parseUnit(field string) (Unit, error) {
	switch strings.TrimSpace(field) {
	case "lb":
		return UnitPound, nil
	case "kg":
		return UnitKilogram, nil
	}
	return "", fmt.Errorf("unit code %q is not one the yard weighs in", field)
}

// FormatWeightField renders thousandths of a unit into the fixed ten
// character weight field, for the simulator side of the wire.
func FormatWeightField(milli int64) string {
	neg := milli < 0
	if neg {
		milli = -milli
	}
	s := strconv.FormatInt(milli/1000, 10) + "." + fmt.Sprintf("%03d", milli%1000)
	if neg {
		s = "-" + s
	}
	if len(s) > 10 {
		return "**********"
	}
	return strings.Repeat(" ", 10-len(s)) + s
}
