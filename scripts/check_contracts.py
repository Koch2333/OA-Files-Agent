"""Validate the OpenAPI source, the embedded copy, real handler responses and (optionally) recorded e2e responses.

  python scripts/check_contracts.py                         # spec + embedded copy + health/readiness handler samples
  python scripts/check_contracts.py --recorded data/e2e-samples.json
      # additionally validates every response recorded by `go run ./cmd/e2e -record ...` against the documented schema
"""
import argparse
import json
import re
from pathlib import Path
import subprocess
import sys

import yaml
from jsonschema import Draft202012Validator
from openapi_spec_validator import validate_spec
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "contracts/openapi.yaml"
EMBEDDED = ROOT / "backend/internal/api/assets/openapi.yaml"
METHODS = ("get", "post", "put", "patch", "delete")
HANDLER_SAMPLED = {"/healthz", "/readyz"}  # covered by cmd/contract-samples without a database


def resolve(spec, node):
    while isinstance(node, dict) and "$ref" in node:
        target = spec
        for part in node["$ref"].lstrip("#/").split("/"):
            target = target[part.replace("~1", "/").replace("~0", "~")]
        node = target
    return node


def response_schema(spec, path, method, status):
    op = spec["paths"][path][method]
    resp = resolve(spec, op["responses"].get(str(status)))
    if resp is None:
        return None, f"{method.upper()} {path} has no documented {status} response"
    content = resp.get("content", {}).get("application/json")
    if content is None:
        return {}, None  # e.g. 204 or non-JSON
    return content["schema"], None


def template_regex(path):
    return re.compile("^" + re.sub(r"\{[^/]+\}", "[^/]+", re.escape(path).replace(r"\{", "{").replace(r"\}", "}")) + "$")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--recorded")
    args = ap.parse_args()
    if SOURCE.read_bytes() != EMBEDDED.read_bytes():
        raise ValueError("Embedded contract is stale; run python scripts/sync_contract.py")
    spec = yaml.safe_load(SOURCE.read_text(encoding="utf-8"))
    validate_spec(spec)
    registry = Registry().with_resource("urn:oa:openapi", Resource.from_contents(spec, default_specification=DRAFT202012))

    # Inline schemas use document-local refs; point them at the registered full document.
    def check_schema(schema, body):
        s = json.loads(json.dumps(schema).replace('"$ref": "#/', '"$ref": "urn:oa:openapi#/'))
        Draft202012Validator(s, registry=registry).validate(body)

    result = subprocess.run(["go.exe" if sys.platform == "win32" else "go", "run", "./cmd/contract-samples"], cwd=ROOT / "backend",
                            check=True, capture_output=True, text=True, encoding="utf-8")
    samples = json.loads(result.stdout)
    covered = set()
    for s in samples:
        path, method, status = s["path"], s["method"].lower(), str(s["status"])
        if status == "405":
            method = "get"
        if path == "/missing":
            check_schema({"$ref": "#/components/schemas/Error"}, s["body"])
        else:
            schema, err = response_schema(spec, path, method, status)
            if err:
                raise ValueError(err)
            check_schema(schema, s["body"])
            covered.add((path, method, status))
        headers = {k.lower(): v for k, v in s["headers"].items()}
        if not headers.get("x-request-id") or headers.get("content-type") != "application/json":
            raise ValueError(f"Missing response headers for {path}")
        if status == "405" and headers.get("allow") != "GET":
            raise ValueError("405 must advertise Allow: GET")
    expected = {(p, m, st) for p, item in spec["paths"].items() if p in HANDLER_SAMPLED for m, op in item.items() if m in METHODS for st in op["responses"]}
    if expected - covered:
        raise ValueError(f"Missing response coverage: {expected - covered}")
    msg = f"OpenAPI valid; embedded copy current; {len(samples)} handler responses validated"

    if args.recorded:
        recorded = json.loads(Path(args.recorded).read_text(encoding="utf-8"))
        routes = sorted(((template_regex(p), p) for p in spec["paths"]), key=lambda x: -len(x[1]))
        seen = set()
        for r in recorded:
            if r.get("body") is None:
                continue
            path = r["path"].split("?")[0]
            match = next((p for rx, p in routes if rx.match(path)), None)
            if match is None:
                raise ValueError(f"Recorded call {r['method']} {path} is not in the contract")
            method = r["method"].lower()
            if method not in spec["paths"][match]:
                if r["status"] == 405:
                    continue
                raise ValueError(f"{r['method']} {match} not documented")
            schema, err = response_schema(spec, match, method, r["status"])
            if err:
                raise ValueError(err + f" (recorded body: {str(r['body'])[:200]})")
            if schema:
                try:
                    check_schema(schema, r["body"])
                except Exception as e:
                    raise ValueError(f"{r['method']} {match} -> {r['status']} violates schema: {str(e)[:400]}")
            seen.add((match, method, str(r["status"])))
        documented = {(p, m, st) for p, item in spec["paths"].items() for m, op in item.items() if m in METHODS for st in op["responses"]}
        missing = sorted(documented - seen - expected)
        msg += f"; {len(recorded)} recorded e2e responses validated ({len(seen)} operation/status pairs)"
        if missing:
            msg += f"\n  documented but not exercised by e2e ({len(missing)}): " + ", ".join(f"{m.upper()} {p} {s}" for p, m, s in missing)
    print(msg)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, subprocess.CalledProcessError) as error:
        print(f"Contract check failed: {error}", file=sys.stderr)
        sys.exit(1)
