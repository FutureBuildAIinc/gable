-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 152: the shell's columns leave the pending sign in table; a
-- pending shell sign in is short lived and is dropped with them.

DELETE FROM pending_sign_ins WHERE client_kind = 'shell';
DROP INDEX IF EXISTS pending_sign_ins_code_hash_uniq;
ALTER TABLE pending_sign_ins
    DROP CONSTRAINT IF EXISTS pending_sign_ins_shell_leg,
    DROP CONSTRAINT IF EXISTS pending_sign_ins_code_hash_len,
    DROP COLUMN IF EXISTS code_used,
    DROP COLUMN IF EXISTS code_expires_at,
    DROP COLUMN IF EXISTS code_hash,
    DROP COLUMN IF EXISTS return_target,
    DROP COLUMN IF EXISTS code_challenge;
