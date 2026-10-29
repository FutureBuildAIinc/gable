// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package scale

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"time"
)

// Driver is the whole seam between the counter and an indicator: open an
// endpoint, read a reading, close. Everything else, the protocol codec and
// the transport included, is an implementation detail, so a second protocol
// drops in behind this interface without touching the counter.
type Driver interface {
	// Read asks the indicator for the current weight at once (the SMA W
	// command) and enforces the refusal rules: a load in motion, an error
	// status or a response with no weight is an error, never a number.
	Read(ctx context.Context) (Reading, error)
	// ReadStable asks for the weight once the load is stable (the SMA P
	// command): the command a ticket flow takes.
	ReadStable(ctx context.Context) (Reading, error)
	Close() error
}

// tcpDriver speaks SMA over a TCP socket: to an indicator with an Ethernet
// port of its own, or to a serial to Ethernet bridge in front of one that
// has only RS-232.
type tcpDriver struct {
	conn          net.Conn
	reader        *bufio.Reader
	readTimeout   time.Duration
	stableTimeout time.Duration
}

// DialTCP opens the driver end of an SMA endpoint ("host:port").
func DialTCP(ctx context.Context, addr string) (Driver, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial the scale at %s: %w", addr, err)
	}
	return &tcpDriver{conn: conn, reader: bufio.NewReader(conn),
		readTimeout: 5 * time.Second, stableTimeout: 30 * time.Second}, nil
}

func (d *tcpDriver) Close() error { return d.conn.Close() }

func (d *tcpDriver) Read(ctx context.Context) (Reading, error) {
	return d.exchange(ctx, CmdWeigh, d.readTimeout)
}

func (d *tcpDriver) ReadStable(ctx context.Context) (Reading, error) {
	return d.exchange(ctx, CmdWeighStable, d.stableTimeout)
}

// exchange sends one command and reads one framed response, then applies the
// refusal rules so the counter never sees a number it must not sell.
func (d *tcpDriver) exchange(ctx context.Context, cmd Command, window time.Duration) (Reading, error) {
	deadline := time.Now().Add(window)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := d.conn.SetDeadline(deadline); err != nil {
		return Reading{}, err
	}
	if _, err := d.conn.Write(EncodeCommand(cmd)); err != nil {
		return Reading{}, fmt.Errorf("write the weigh command: %w", err)
	}
	frame, err := d.reader.ReadBytes('\r')
	if err != nil {
		return Reading{}, fmt.Errorf("read the scale's answer: %w", err)
	}
	r, err := ParseResponse(frame)
	if err != nil {
		return Reading{}, err
	}
	if err := r.Refusal(); err != nil {
		return Reading{}, err
	}
	return r, nil
}
