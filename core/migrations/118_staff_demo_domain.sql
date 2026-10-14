-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 118_staff_demo_domain
-- Description: migration 080 seeded three demo staff rows on a real domain
-- (gable.com). A demo address belongs on a reserved .example domain, so this
-- rewrites exactly those three values and nothing else. A row is left alone
-- when the new address is already taken, and the staff id (so every module
-- grant) is unchanged.

UPDATE staff SET email = 'dispatcher@gablebuildingsupply.example'
WHERE email = 'dispatcher@gable.com'
  AND NOT EXISTS (SELECT 1 FROM staff WHERE email = 'dispatcher@gablebuildingsupply.example');

UPDATE staff SET email = 'yard@gablebuildingsupply.example'
WHERE email = 'yard@gable.com'
  AND NOT EXISTS (SELECT 1 FROM staff WHERE email = 'yard@gablebuildingsupply.example');

UPDATE staff SET email = 'admin@gablebuildingsupply.example'
WHERE email = 'admin@gable.com'
  AND NOT EXISTS (SELECT 1 FROM staff WHERE email = 'admin@gablebuildingsupply.example');
