// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"fmt"

	"github.com/gablelbm/gable/internal/scale"
	"github.com/gablelbm/gable/pkg/database"
)

// wireSimulatedScale starts the SMA simulator on loopback, dials the driver
// to it, and writes each branch's device row for it, so every ticket the
// demo takes names its device (CP-19, ruling D8). The endpoint marker
// sim://loopback is the row's identity: rewiring on a restart keeps one row
// per branch.
func wireSimulatedScale(ctx context.Context, db *database.DB) (*scale.Simulator, scale.Driver, error) {
	sim := scale.NewSimulator()
	addr, err := sim.ListenTCP()
	if err != nil {
		return nil, nil, err
	}
	driver, err := scale.DialTCP(ctx, addr)
	if err != nil {
		sim.Close()
		return nil, nil, err
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO scale_devices (branch_id, name, protocol, endpoint, certificate_no, location_note)
		SELECT l.id, 'Yard scale (simulated)', 'SMA', 'sim://loopback', 'SIMULATED', 'the simulator on this host'
		FROM locations l WHERE l.type = 'BRANCH'
			AND NOT EXISTS (SELECT 1 FROM scale_devices d WHERE d.branch_id = l.id AND d.endpoint = 'sim://loopback')`); err != nil {
		driver.Close()
		sim.Close()
		return nil, nil, fmt.Errorf("the simulated scale's device rows: %w", err)
	}
	return sim, driver, nil
}
