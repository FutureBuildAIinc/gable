-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 089_events_outbox.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand:
--
--   DROP TABLE IF EXISTS event_subscriber_parked;
--   DROP TABLE IF EXISTS event_subscriber_cursors;
--   DROP TABLE IF EXISTS events_outbox;
--   DROP SEQUENCE IF EXISTS events_outbox_position_seq;
--
-- The drop loses every undelivered event and every subscriber's cursor, so
-- consumers restart from the head of a new feed. Apply only when the outbox
-- itself is being removed from the product.

DROP TABLE IF EXISTS event_subscriber_parked;
DROP TABLE IF EXISTS event_subscriber_cursors;
DROP TABLE IF EXISTS events_outbox;
DROP SEQUENCE IF EXISTS events_outbox_position_seq;
