#!/usr/bin/env bash
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
#
# App Shape check for Gable. Stand-in for `fb check` (ADR-011) until the
# platform CLI exists. Run from anywhere inside the repository; CI runs it as
# the step "App Shape check". It needs bash, git and python3 only.
#
# It verifies, in order:
#   1. the required paths exist;
#   2. manifest.yaml declares shape, app, tenancy, runtimes and frontends with
#      valid values, and every runtime and frontend path exists unless the
#      manifest marks the entry planned (a planned entry whose path already
#      exists fails, so a mark cannot go stale); a frontend path is relative
#      and has no "..";
#      database.extensions equals the set core/migrations/*.sql creates;
#   3. CLAUDE.md imports AGENTS.md;
#   4. every JSON file parses (tsconfig*.json are JSON with comments and
#      trailing commas, so those are parsed after stripping both);
#   5. no stray artefact sits in the repository root (see STRAY below).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

fail() { echo "check-shape: FAIL: $*" >&2; exit 1; }

# 1. Required paths.
required=(
  manifest.yaml
  AGENTS.md
  CLAUDE.md
  core/cmd/core
  core/api/openapi.yaml
  core/api/ROUTES.txt
  web/apps/desk
  docs/adr
  LICENSE-MAP.md
  REUSE.toml
)
for path in "${required[@]}"; do
  [ -e "$path" ] || fail "missing required path: $path"
done

# 3. CLAUDE.md imports AGENTS.md (a line that is exactly the import).
grep -qx '@AGENTS.md' CLAUDE.md || fail "CLAUDE.md must import AGENTS.md with a line reading exactly @AGENTS.md"

# 5. Stray artefacts. A pull request body, a patch, an editor or merge
# leftover, a log or a coverage dump in the repository root is scratch that
# slipped into a commit. Root only: nested fixtures may legitimately use such
# names. Tracked files and untracked files that are not ignored both count.
STRAY='^(pr[-_ ]?body.*|pr[-_ ]?description.*|.*\.(orig|rej|bak|tmp|swp|patch|diff|log)|.*~|nohup\.out|\.DS_Store|coverage\.out)$'
stray="$(git ls-files -co --exclude-standard | grep -v / | grep -Ei "$STRAY" || true)"
[ -z "$stray" ] || fail "stray artefact in the repository root: $(echo "$stray" | tr '\n' ' ')"

# 2 and 4: manifest and JSON, in python (the manifest parser handles the
# small YAML subset manifest.yaml is kept to).
python3 - <<'PY'
import glob, json, os, re, subprocess, sys

def fail(msg):
    print("check-shape: FAIL: " + msg, file=sys.stderr)
    sys.exit(1)

def scalar(v):
    v = v.strip()
    if v.startswith("[") or v.endswith("]"):
        if not (v.startswith("[") and v.endswith("]")) or v.count("[") != 1 or v.count("]") != 1:
            fail("manifest.yaml has an unbalanced or nested bracket list: %s" % v)
        inner = v[1:-1].strip()
        items = [x.strip() for x in inner.split(",")] if inner else []
        if "" in items:
            fail("manifest.yaml has an empty item in the list: %s" % v)
        dup = sorted({x for x in items if items.count(x) > 1})
        if dup:
            fail("manifest.yaml has a duplicate list item %s in: %s" % (", ".join(dup), v))
        return items
    if v == "true":
        return True
    if v == "false":
        return False
    return v.strip("\"'")

def parse_manifest(path):
    top, frontends, cur, block = {}, [], None, None
    for raw in open(path, encoding="utf-8"):
        line = re.sub(r"(^|\s)#.*$", "", raw).rstrip()
        if not line.strip():
            continue
        indent = len(line) - len(line.lstrip())
        text = line.strip()
        if indent == 0:
            cur = None
            block = None
            key, _, val = text.partition(":")
            if val.strip() == "":
                block = key
                top[key] = [] if key == "frontends" else {}
            else:
                top[key] = scalar(val)
        elif block == "frontends":
            if text.startswith("- "):
                cur = {}
                top["frontends"].append(cur)
                text = text[2:]
            key, _, val = text.partition(":")
            cur[key.strip()] = scalar(val)
        elif block:
            key, _, val = text.partition(":")
            top[block][key.strip()] = scalar(val)
    return top

m = parse_manifest("manifest.yaml")

def only_keys(where, d, allowed):
    extra = sorted(set(d) - set(allowed))
    if extra:
        fail("%s has unknown key(s) %s (allowed: %s)" % (where, ", ".join(extra), ", ".join(sorted(allowed))))

only_keys("manifest.yaml", m, ["shape", "app", "name", "runtimes", "planned", "tenancy", "database", "observability", "frontends"])
for blk, keys in (("database", ["extensions"]), ("observability", ["service"])):
    if blk in m:
        if not isinstance(m[blk], dict):
            fail("manifest.yaml %s must be a block of keys" % blk)
        only_keys("manifest.yaml " + blk, m[blk], keys)

if m.get("shape") != "1":
    fail("manifest.yaml must declare shape: 1")
if m.get("app") != "gable":
    fail("manifest.yaml must declare app: gable")
if m.get("tenancy") not in ("single-org", "multi-org"):
    fail("manifest.yaml tenancy must be single-org or multi-org")
if m.get("tenancy") != "multi-org":
    fail("manifest.yaml tenancy must be multi-org for gable (one database per dealer)")

KNOWN_RUNTIMES = {"core": "core/cmd/core", "web": "web", "tauri": "tauri", "agents": "agents"}
runtimes = m.get("runtimes")
if not isinstance(runtimes, list) or not runtimes:
    fail("manifest.yaml runtimes must be a non-empty list")
for r in runtimes:
    if r not in KNOWN_RUNTIMES:
        fail("manifest.yaml runtime %r is not one of %s" % (r, sorted(KNOWN_RUNTIMES)))
for r in ("core", "web"):
    if r not in runtimes:
        fail("manifest.yaml runtimes must include " + r)
planned_rt = m.get("planned", [])
if not isinstance(planned_rt, list):
    fail("manifest.yaml planned must be a list of runtimes")
for r in planned_rt:
    if r not in runtimes:
        fail("manifest.yaml planned names runtime %r that runtimes does not list" % r)
for r in runtimes:
    p = KNOWN_RUNTIMES[r]
    if r in planned_rt:
        if os.path.exists(p):
            fail("runtime %s is marked planned but %s exists; remove it from planned" % (r, p))
    elif not os.path.exists(p):
        fail("runtime %s: path %s does not exist (mark it planned if it has not landed)" % (r, p))

# database.extensions must equal the set the migrations create.
migrated = set(re.findall(
    r"CREATE\s+EXTENSION\s+(?:IF\s+NOT\s+EXISTS\s+)?[\"']?([A-Za-z0-9_-]+)",
    "".join(open(f, encoding="utf-8").read()
            for f in sorted(glob.glob("core/migrations/*.sql"))),
    re.IGNORECASE))
declared = m.get("database", {}).get("extensions")
if not isinstance(declared, list):
    fail("manifest.yaml database.extensions must be a list")
if set(declared) != migrated:
    fail("manifest.yaml database.extensions %s does not match the extensions core/migrations create %s"
         % (sorted(declared), sorted(migrated)))

fes = m.get("frontends")
if not isinstance(fes, list) or not fes:
    fail("manifest.yaml frontends must be a non-empty list")
ids = []
for fe in fes:
    only_keys("manifest.yaml frontend entry", fe, ["id", "name", "path", "planned"])
    fid = fe.get("id")
    if not fid or not re.fullmatch(r"[a-z][a-z0-9-]*", fid):
        fail("manifest.yaml frontend id %r is missing or not kebab-case" % fid)
    if fid in ids:
        fail("manifest.yaml frontend id %s is listed twice" % fid)
    ids.append(fid)
    p = fe.get("path")
    if not p or not isinstance(p, str):
        fail("manifest.yaml frontend %s has no path" % fid)
    if p.startswith("/") or ".." in p.split("/"):
        fail("manifest.yaml frontend %s: path %r must be relative and stay inside the repository" % (fid, p))
    if fe.get("planned", False) not in (True, False):
        fail("manifest.yaml frontend %s: planned must be true or false" % fid)
    if fe.get("planned") is True:
        if os.path.exists(p):
            fail("frontend %s is marked planned but %s exists; drop planned: true" % (fid, p))
    elif not os.path.exists(p):
        fail("frontend %s: path %s does not exist (mark it planned: true if it has not landed)" % (fid, p))
for need in ("front-door", "desk"):
    if need not in ids:
        fail("manifest.yaml frontends must include " + need)

# JSON files. tsconfig*.json are JSONC: strip comments and trailing commas
# outside strings, then parse.
def strip_jsonc(src):
    out, i, n, in_str = [], 0, len(src), False
    while i < n:
        c = src[i]
        if in_str:
            out.append(c)
            if c == "\\":
                out.append(src[i + 1]); i += 1
            elif c == '"':
                in_str = False
        elif c == '"':
            in_str = True; out.append(c)
        elif src.startswith("//", i):
            while i < n and src[i] != "\n":
                i += 1
            continue
        elif src.startswith("/*", i):
            j = src.find("*/", i + 2)
            i = n if j < 0 else j + 2
            continue
        else:
            out.append(c)
        i += 1
    return re.sub(r",(\s*[}\]])", r"\1", "".join(out))

files = subprocess.run(
    ["git", "ls-files", "-co", "--exclude-standard", "-z", "--", "*.json"],
    check=True, capture_output=True, text=True).stdout.split("\0")
for f in filter(None, files):
    if not os.path.isfile(f):
        continue
    try:
        text = open(f, encoding="utf-8").read()
        if os.path.basename(f).startswith("tsconfig"):
            text = strip_jsonc(text)
        json.loads(text)
    except Exception as e:
        fail("invalid JSON: %s (%s)" % (f, e))
PY

echo "check-shape: OK"
