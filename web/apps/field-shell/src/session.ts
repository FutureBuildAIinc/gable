// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The session handoff between the field shell and the apps it frames.
//
// Gable's custody (web/packages/auth) writes the session to the tab's
// sessionStorage under one key at sign in, and every bundle on this origin
// adopts it at boot. A same origin frame shares that store, so the handoff
// needs no copy and no rewrite: the framed app reads the same session, the
// same role, and (through localStorage) the same yard. What this module adds
// is only the read side: the custody's session mapped onto the host's own
// shape.

import { authCustody } from '@gable/auth';
import type { HostSession } from './host-state.ts';

/** Capitalise a role slug (warehouse -> Warehouse). The same rule the desk's header uses. */
export function roleInWords(role: string): string {
  if (role.length === 0) return '';
  return role[0]!.toUpperCase() + role.slice(1);
}

/** The custody's session as the host holds it, or null when nobody is signed in. */
export function sessionFromCustody(custody: typeof authCustody = authCustody): HostSession | null {
  const session = custody.session;
  if (session === null) return null;
  const personaRole = custody.personaRole;
  const tokenRoles = custody.roles;
  const roles = personaRole !== null ? [personaRole] : tokenRoles;
  const roleLabel =
    personaRole !== null
      ? roleInWords(personaRole)
      : tokenRoles.length > 0
        ? tokenRoles.map(roleInWords).join(' · ')
        : 'local development';
  return { name: custody.displayName ?? '', roleLabel, roles };
}
