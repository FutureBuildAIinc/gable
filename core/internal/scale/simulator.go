// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package scale

// The simulator proves the driver with no hardware: it speaks SMA exactly,
// reproduces the things that break drivers (motion, overload, a refusal
// with no weight, both unit codes, the gross, tare and net modes), settles
// like a truck, and carries a control surface a test drives: set the
// weight, set the motion flag, force an error, and read back the last
// command the driver sent.

import (
	"bufio"
	"fmt"
	"net"
	"sync"
	"time"
)

// Simulator is an in-process SMA indicator. Its control methods are the
// test's hands on the scale.
type Simulator struct {
	mu       sync.Mutex
	settled  *sync.Cond
	weight   int64
	tare     int64
	unit     Unit
	mode     Mode
	motion   bool
	status   Status
	settleIn time.Duration
	last     Command
	ln       net.Listener
	closed   bool
}

// NewSimulator answers a simulator at center of zero in pounds.
func NewSimulator() *Simulator {
	s := &Simulator{unit: UnitPound, mode: ModeGross, settleIn: 100 * time.Millisecond}
	s.settled = sync.NewCond(&s.mu)
	return s
}

// ListenTCP starts the simulator's indicator socket on loopback and answers
// its address.
func (s *Simulator) ListenTCP() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("simulator listen: %w", err)
	}
	s.ln = ln
	go s.acceptLoop()
	return ln.Addr().String(), nil
}

// Addr is the simulator's socket address once it listens.
func (s *Simulator) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// SetWeightMilli puts a load on the deck: the reading is in motion for the
// settle delay, then stable, the way a truck climbs on and settles.
func (s *Simulator) SetWeightMilli(milli int64, unit Unit) {
	s.mu.Lock()
	s.weight = milli
	s.unit = unit
	s.motion = true
	settle := s.settleIn
	s.mu.Unlock()
	time.AfterFunc(settle, func() {
		s.mu.Lock()
		s.motion = false
		s.settled.Broadcast()
		s.mu.Unlock()
	})
}

// SetWeightStableMilli puts a load on the deck already settled: the control
// for a test that is not proving the settle.
func (s *Simulator) SetWeightStableMilli(milli int64, unit Unit) {
	s.mu.Lock()
	s.weight = milli
	s.unit = unit
	s.motion = false
	s.settled.Broadcast()
	s.mu.Unlock()
}

// SetMotion pins the motion flag directly.
func (s *Simulator) SetMotion(moving bool) {
	s.mu.Lock()
	s.motion = moving
	if !moving {
		s.settled.Broadcast()
	}
	s.mu.Unlock()
}

// SetStatus forces an indicator status; StatusNone clears it.
func (s *Simulator) SetStatus(st Status) {
	s.mu.Lock()
	s.status = st
	s.mu.Unlock()
}

// SetTareMilli stores a tare on the indicator.
func (s *Simulator) SetTareMilli(milli int64) {
	s.mu.Lock()
	s.tare = milli
	s.mu.Unlock()
}

// SetMode switches the indicator between gross, tare and net.
func (s *Simulator) SetMode(m Mode) {
	s.mu.Lock()
	s.mode = m
	s.mu.Unlock()
}

// SetSettleDelay sets how long a new load stays in motion.
func (s *Simulator) SetSettleDelay(d time.Duration) {
	s.mu.Lock()
	s.settleIn = d
	s.mu.Unlock()
}

// LastCommand is the last command letter a driver sent, so a test proves
// the driver issued a command and did not just receive a number.
func (s *Simulator) LastCommand() Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// Close stops the simulator.
func (s *Simulator) Close() error {
	s.mu.Lock()
	s.closed = true
	s.settled.Broadcast()
	s.mu.Unlock()
	if s.ln != nil {
		return s.ln.Close()
	}
	return nil
}

func (s *Simulator) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(conn)
	}
}

// serve answers one driver's commands until it hangs up.
func (s *Simulator) serve(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		frame, err := reader.ReadBytes('\r')
		if err != nil {
			return
		}
		if len(frame) != 3 || frame[0] != '\n' {
			continue
		}
		cmd := Command(frame[1])
		s.mu.Lock()
		s.last = cmd
		s.mu.Unlock()
		switch cmd {
		case CmdWeigh:
			if _, err := conn.Write(s.response()); err != nil {
				return
			}
		case CmdWeighStable:
			// The stable command answers once the load settles.
			s.mu.Lock()
			for s.motion && !s.closed {
				s.settled.Wait()
			}
			s.mu.Unlock()
			if _, err := conn.Write(s.response()); err != nil {
				return
			}
		case CmdZero:
			s.mu.Lock()
			s.weight = 0
			s.mu.Unlock()
			if _, err := conn.Write(s.response()); err != nil {
				return
			}
		case CmdTare:
			s.mu.Lock()
			s.tare = s.weight
			s.mode = ModeNet
			s.mu.Unlock()
			if _, err := conn.Write(s.response()); err != nil {
				return
			}
		}
	}
}

// response frames the current state as one exact SMA response.
func (s *Simulator) response() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.status
	weight := s.weight
	switch s.mode {
	case ModeTare:
		weight = s.tare
	case ModeNet:
		weight = s.weight - s.tare
	}
	if status == StatusNone && weight == 0 && s.mode == ModeGross {
		status = StatusCenterOfZero
	}
	statusChar := byte(' ')
	switch status {
	case StatusCenterOfZero:
		statusChar = 'Z'
	case StatusOverCapacity:
		statusChar = 'O'
	case StatusUnderCapacity:
		statusChar = 'U'
	case StatusZeroError:
		statusChar = 'E'
	case StatusInitialZeroError:
		statusChar = 'I'
	case StatusTareError:
		statusChar = 'T'
	}
	modeChar := byte('G')
	switch s.mode {
	case ModeTare:
		modeChar = 'T'
	case ModeNet:
		modeChar = 'N'
	}
	motionChar := byte(' ')
	if s.motion {
		motionChar = 'M'
	}
	weightField := FormatWeightField(weight)
	if status.Refused() {
		weightField = "**********"
	}
	unit := string(s.unit)
	for len(unit) < 3 {
		unit += " "
	}
	return []byte("\n" + string([]byte{statusChar, '1', modeChar, motionChar, ' '}) + weightField + unit + "\r")
}
