// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package scale_test

// The driver end to end against the simulator, over TCP with no hardware
// and no network beyond loopback (CP-19, ruling D8): the scenarios the
// driver must survive, and the settle a real truck needs before its weight
// is money.

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/scale"
)

// newSimPair starts a simulator on loopback TCP and dials a driver to it.
func newSimPair(t *testing.T) (*scale.Simulator, scale.Driver) {
	t.Helper()
	sim := scale.NewSimulator()
	addr, err := sim.ListenTCP()
	if err != nil {
		t.Fatalf("simulator listen: %v", err)
	}
	t.Cleanup(func() { sim.Close() })
	drv, err := scale.DialTCP(context.Background(), addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { drv.Close() })
	return sim, drv
}

func TestDriverReadsStableGrossOverTCP(t *testing.T) {
	sim, drv := newSimPair(t)
	sim.SetWeightStableMilli(12400500, scale.UnitPound)
	r, err := drv.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if r.WeightMilli != 12400500 || r.Unit != scale.UnitPound || r.Mode != scale.ModeGross || r.Motion {
		t.Errorf("got %+v", r)
	}
	if sim.LastCommand() != scale.CmdWeigh {
		t.Errorf("the driver sent %q, want the weigh command", sim.LastCommand())
	}
}

func TestDriverRefusesAMovingLoad(t *testing.T) {
	sim, drv := newSimPair(t)
	sim.SetWeightStableMilli(12400500, scale.UnitPound)
	sim.SetMotion(true)
	if _, err := drv.Read(context.Background()); err == nil {
		t.Fatal("a reading in motion was handed over")
	}
}

func TestDriverRefusesOverCapacity(t *testing.T) {
	sim, drv := newSimPair(t)
	sim.SetStatus(scale.StatusOverCapacity)
	if _, err := drv.Read(context.Background()); err == nil {
		t.Fatal("an over capacity reading was handed over")
	}
}

func TestDriverRefusesUnderCapacityAndZeroErrors(t *testing.T) {
	for _, st := range []scale.Status{scale.StatusUnderCapacity, scale.StatusZeroError, scale.StatusInitialZeroError, scale.StatusTareError} {
		sim, drv := newSimPair(t)
		sim.SetStatus(st)
		if _, err := drv.Read(context.Background()); err == nil {
			t.Errorf("status %v: the reading was handed over", st)
		}
		drv.Close()
		sim.Close()
	}
}

func TestDriverReadsKilogramsAsKilograms(t *testing.T) {
	sim, drv := newSimPair(t)
	sim.SetWeightStableMilli(8400000, scale.UnitKilogram)
	r, err := drv.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if r.Unit != scale.UnitKilogram {
		t.Errorf("unit: got %q", r.Unit)
	}
	lb, err := r.PoundsMilli()
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if lb != 18518833 { // 8400 kg at 2.204623 lb/kg, milli scale
		t.Errorf("pounds: got %d", lb)
	}
}

func TestSimulatorTareSwitchesTheMode(t *testing.T) {
	sim, drv := newSimPair(t)
	sim.SetWeightStableMilli(15000000, scale.UnitPound)
	sim.SetTareMilli(3200000)
	sim.SetMode(scale.ModeTare)
	r, err := drv.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if r.Mode != scale.ModeTare || r.WeightMilli != 3200000 {
		t.Errorf("tare mode: got %+v", r)
	}
	sim.SetMode(scale.ModeNet)
	r, err = drv.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if r.Mode != scale.ModeNet || r.WeightMilli != 11800000 {
		t.Errorf("net mode: got %+v", r)
	}
}

// A truck settles: the weight climbs, the reading is unstable for a while,
// then it stabilises, and the stable command blocks until it does.
func TestReadStableBlocksUntilTheLoadSettles(t *testing.T) {
	sim, drv := newSimPair(t)
	sim.SetSettleDelay(150 * time.Millisecond)
	start := time.Now()
	sim.SetWeightMilli(12400500, scale.UnitPound)
	r, err := drv.ReadStable(context.Background())
	if err != nil {
		t.Fatalf("read stable: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("the stable read came back in %v, before the load settled", elapsed)
	}
	if r.Motion || r.WeightMilli != 12400500 {
		t.Errorf("got %+v", r)
	}
	if sim.LastCommand() != scale.CmdWeighStable {
		t.Errorf("the driver sent %q, want the stable weigh command", sim.LastCommand())
	}
}

// The control surface proves the driver issued the command, not just
// received a number.
func TestSimulatorRecordsTheLastCommand(t *testing.T) {
	sim, drv := newSimPair(t)
	sim.SetWeightStableMilli(1000, scale.UnitPound)
	if _, err := drv.Read(context.Background()); err != nil {
		t.Fatalf("read: %v", err)
	}
	if sim.LastCommand() != scale.CmdWeigh {
	 t.Errorf("got %q", sim.LastCommand())
	}
}
