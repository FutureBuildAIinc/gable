---
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
name: licensing-check
description: Answer "which license governs this file?" and explain the OpenLBM Standard's per-component model and eligibility gates in plain language. Use when the user says "what license is this file", "which license applies", "can I use this commercially", "do I have to open-source my changes", "what SPDX header do I need", "is this GPL", "can my company use Gable", "what's the difference between Commons and Connector", "/license-of", or is adding a new file and needs the right header.
---

# licensing-check — which license governs this, and what it means

Two different questions get asked here. Work out which one they're asking before answering.

1. **Mechanical:** "what SPDX header does this file need?" → §1, answerable exactly.
2. **Substantive:** "may my company use this / do I have to publish my changes?" → §3, where
   you explain the model and then decline to give legal advice.

---

## 1 · Which license governs a given file

Authoritative sources, in order: the file's own `SPDX-License-Identifier` header →
[`REUSE.toml`](../../../REUSE.toml) → [`LICENSE-MAP.md`](../../../LICENSE-MAP.md).

```bash
head -3 <path/to/file>            # the file's own header, if it has one
```

The directory mapping:

| Path prefix | SPDX identifier | Profile |
|---|---|---|
| `backend/internal/` | `LicenseRef-OpenLBM-Commons-1.0` | Commons |
| `backend/pkg/` *(except `backend/pkg/apps/`)* | `LicenseRef-OpenLBM-Commons-1.0` | Commons |
| `backend/cmd/` | `LicenseRef-OpenLBM-Commons-1.0` | Commons |
| `backend/migrations/` | `LicenseRef-OpenLBM-Commons-1.0` | Commons |
| **`backend/pkg/apps/`** | **`LicenseRef-OpenLBM-Connector-1.0`** | **Connector** |
| `app/` | `LicenseRef-OpenLBM-Surface-1.0` | Surface |
| `docs/` | `LicenseRef-OpenLBM-Docs-1.0` | Docs |
| `.claude/` | `LicenseRef-OpenLBM-Docs-1.0` | Docs |

**Precedence: the most specific path wins.** `backend/pkg/apps/` is deliberately carved out of
the `backend/pkg/` Commons default, so a file there is **Connector**, not Commons. This is the
single most common mistake — and it matters, because Connector is the permissive seam that
lets third parties plug in without copyleft crossing the boundary.

The same ordering rule is encoded in `REUSE.toml`: when a path matches more than one
`[[annotations]]` block, the **last** matching block wins, which is why the `backend/pkg/apps`
block sits after the broader `backend/pkg` one. Don't reorder it.

`LICENSE-MAP.md` also notes two licenses that are **not** directory-scoped and so don't appear
in the table: `LicenseRef-OpenLBM-Community-Source-1.0` (applied per-work, by version notice —
used by community satellites like AI_LM) and `LicenseRef-OpenLBM-Trademark` (a brand-use
policy, not a code license).

### Header formats

<!-- REUSE-IgnoreStart -->

```go
// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
```
```ts
// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
```
```sql
-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
```
```markdown
<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->
```

<!-- REUSE-IgnoreEnd -->

For a file that can't hold a comment (PNG, JSON, binary), REUSE uses a sidecar: create
`<filename>.license` beside it containing the two `SPDX-` lines. For a YAML or Markdown file
that starts with `---` frontmatter, put the tags as `#` comments **inside** the frontmatter
block so the frontmatter still parses (that's what the files in `.claude/skills/` do).

Verify with the machine checker if it's installed — a `reuse lint` CI gate is being added:

```bash
reuse lint            # or: pipx run reuse lint
```

## 2 · The full license texts

All eight live in [`LICENSES/`](../../../LICENSES/):

```bash
ls LICENSES/
```

`LicenseRef-OpenLBM-{Commons,Surface,Connector,Community-Source,Docs,Trademark}` plus
`GPL-3.0-or-later` and `AGPL-3.0-or-later` (present for third-party compatibility, not as the
project's own grant).

> **Status you must state whenever this comes up:** the OpenLBM Standard is **published and effective at version 1.0**. The canonical texts live at <https://github.com/FutureBuildAIinc/openlbm>; the copies under `LICENSES/` in each repo are vendored so the repository is self-contained and REUSE-compliant offline. Where a vendored copy and the published Standard ever disagree, **the published Standard governs**.
> The copies are not a second source of truth — always point the reader at the canonical repo.

## 3 · The model, in plain language

OpenLBM is a **fair-source** framework written for this project — not an OSI-approved license.
One Standard, several **Profiles**, one shared **Definitions Core**.

**Why per-component?** Different parts of the system need different bargains:

| Profile | Covers | The bargain, roughly |
|---|---|---|
| **Commons** | The core ERP (`backend/internal`, `backend/pkg`, `backend/cmd`, migrations) | Reciprocity that scales with your size — improvements to the commons flow back |
| **Surface** | The client apps (`app/`) | Reciprocity triggered by distribution of the surface |
| **Connector** | The plug-in seam (`backend/pkg/apps/`) | Permissive, no copyleft — build on it without your code being pulled in |
| **Community-Source** | Community satellites (e.g. AI_LM) | Free for community members, fee for others |
| **Docs** | Documentation and specs | Docs-appropriate terms |
| **Trademark** | The Gable marks | A brand-use policy — it grants **no** code rights, and no code license grants trademark rights |

**Three eligibility gates** determine what a given party may do. All three are separate tests;
failing any one changes the answer:

1. **Size** — *Independent Operator* versus *Large Operator*. The commons is built for
   independent dealers; consolidators are gated (contribute back or pay).
2. **Field-of-use** — a *Competing Vendor / Competing Use* exclusion. A legacy LBM software
   vendor is gated **regardless of size**. Being small doesn't help if you're building a
   competing product with it.
3. **Participation** — *Community Member* status. Important: participation governs
   **governance and joint-venture rights**, **not** the underlying grant. Don't tell anyone
   they must join something to use the software.

The canonical definitions of all three live in the OpenLBM repository
(<https://github.com/FutureBuildAIinc/openlbm>) — that text supersedes any summary, including
this one.

## 4 · Common questions, answered honestly

**"Is this GPL / open source?"**
No. It is *fair source*. The code is public, forkable, and self-hostable, but the grants are
conditional — they are not OSI-approved open-source licenses. Say so plainly; people make
real decisions on this.

**"Can my company use it?"**
Depends on all three gates, and on which components you touch. Walk them through §3, point at
the canonical text, and say explicitly: *this is not legal advice — have counsel read the
Standard.*

**"If I write a plug-in, do I have to publish it?"**
The `backend/pkg/apps/` Connector seam exists precisely so the answer can be no. That's the
design intent. But whether *your* plug-in stays on the permissive side of the boundary depends
on what it links to — a plug-in that imports `backend/internal/...` has reached across into
Commons. Point at the boundary, then point at counsel.

**"What license is my contribution under?"**
The same license that governs the file(s) you changed — inbound matches the component. Because
these are custom licenses rather than an off-the-shelf inbound=outbound license, there's a
**Contributor License Agreement**: you'll be asked to agree before your first merge. See
`CONTRIBUTING.md` § "Licensing of contributions".

**"Can I use the Gable name or logo?"**
That's `LicenseRef-OpenLBM-Trademark`, a separate brand-use policy. **No code license grants
trademark rights.** Assets and the policy live in the `brand` repository.

**"Can I copy in code from elsewhere?"**
Only if you have the right to license it under the component license it would land in, and you
say where it came from in the PR. Never paste in competitor documentation, schemas, or API
specifications — that's an IP problem this project has explicitly cleaned up once already.

## 5 · If the mapping itself looks wrong

If a file's header disagrees with `LICENSE-MAP.md`/`REUSE.toml`, or a directory isn't covered
at all, that's a real finding worth reporting. Note that `REUSE.toml` currently annotates
`backend/**`, `app/**`, and `docs/**` — root-level markdown and `.claude/**` are **not**
covered by annotations, which is why files in those places carry their headers inline.

Do **not** edit `REUSE.toml` or `LICENSE-MAP.md` to "fix" it yourself — those are maintainer-
owned. Use **`report-an-issue`** and include the exact file, the header it has, and the header
the map implies.

---

## Ground rules

- **You are not counsel and neither is this repo.** Explain the model; for any decision with
  money attached, point at the canonical Standard and a lawyer.
- **Always state the draft status** (§2) when quoting a license text from `LICENSES/`.
- **Never call this "open source"** without the fair-source qualifier.
- **Most specific path wins** — `backend/pkg/apps/` is Connector, not Commons.
- **Trademark is separate.** No code license grants rights in the marks.
