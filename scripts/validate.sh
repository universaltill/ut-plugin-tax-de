#!/usr/bin/env bash
# Validates the tax/export-plugin manifest: marketplace-required fields
# (id/name/semver/permissions/locales), asset-only runtime, canonical_type
# "tax" (ADR-0025), and one entry each of type="tax" and type="export".
set -euo pipefail
cd "$(dirname "$0")/.."
python3 - <<'PY'
import json, os, re, sys
m = json.load(open("manifest.json"))
errs = []
if not re.match(r'^[a-z0-9]+([.-][a-z0-9]+)*$', m.get("id","")): errs.append("bad id")
if not m.get("name"): errs.append("missing name")
if not re.match(r'^\d+\.\d+\.\d+', m.get("version","")): errs.append("bad version")
if not m.get("permissions"): errs.append("missing permissions")
if not m.get("locales"): errs.append("missing locales")
if m.get("runtime") not in ("none", "wasm"): errs.append("runtime must be 'none' or 'wasm' (ADR-0001)")
if m.get("runtime") == "wasm":
    ep = (m.get("entrypoint") or "").lstrip("./")
    if not ep.endswith(".wasm"): errs.append("wasm runtime needs a .wasm entrypoint")
    elif not os.path.isfile(ep): errs.append(f"module not found: {ep} (run scripts/build.sh)")
if m.get("device_arch") != "any": errs.append("device_arch must be 'any'")
if m.get("canonical_type") != "tax": errs.append("canonical_type must be 'tax' (ADR-0025)")
if m.get("countries") != ["DE"]: errs.append("countries must be ['DE'] (ADR-0025 decision 3 — forward-looking metadata field, not yet enforced by the marketplace schema)")
types = [e.get("type") for e in m.get("entries", [])]
if "tax" not in types: errs.append("expected an entries[] item with type=tax (TSE signing)")
if "export" not in types: errs.append("expected an entries[] item with type=export (DSFinV-K export)")
if errs:
    print("FAIL: " + "; ".join(errs)); sys.exit(1)
print(f"ok {m['id']} v{m['version']}")
PY
