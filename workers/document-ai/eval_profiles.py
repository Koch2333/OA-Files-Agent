"""Keyword recall of every OCR profile on tests/samples/degraded/ (requires make_degraded.py output).

Run:  .venv\\Scripts\\python.exe eval_profiles.py [--json out.json]
(optional: --only rot90,skew8 to restrict degradations)
Runs extract_core in-process (same code the child runs). Recall = share of manifest keywords found in the text.
"""
import json
import os
import sys
import time

import extract_core

HERE = os.path.dirname(os.path.abspath(__file__))
DEGRADED = os.path.normpath(os.path.join(HERE, "..", "..", "tests", "samples", "degraded"))


def recall(text, keywords):
    flat = "".join(text.split())
    return sum(1 for k in keywords if "".join(k.split()) in flat) / float(len(keywords))


def evaluate(profiles=extract_core.PROFILES, only=None):
    with open(os.path.join(DEGRADED, "manifest.json"), encoding="utf-8") as f:
        entries = json.load(f)["files"]
    rows = []
    for entry in entries:
        if only and entry["degradation"] not in only:
            continue
        path = os.path.join(DEGRADED, entry["file"])
        for profile in profiles:
            started = time.time()
            res = extract_core.extract(path, ".png", True, profile)
            print(entry["file"], profile, round(time.time() - started, 1), file=sys.stderr, flush=True)
            rows.append({"file": entry["file"], "degradation": entry["degradation"], "profile": profile,
                         "recall": recall(res["text"], entry["keywords"]),
                         "mean_confidence": (res["quality"] or {}).get("mean_confidence"),
                         "rotation": (res["quality"] or {}).get("rotation"),
                         "seconds": round(time.time() - started, 1)})
    return rows


def table(rows):
    degs = []
    for r in rows:
        if r["degradation"] not in degs:
            degs.append(r["degradation"])
    profiles = list(extract_core.PROFILES)
    lines = ["| degradation | " + " | ".join(profiles) + " |", "|---|" + "---|" * len(profiles)]
    for d in degs:
        cells = []
        for p in profiles:
            sel = [r["recall"] for r in rows if r["degradation"] == d and r["profile"] == p]
            cells.append("%.2f" % (sum(sel) / len(sel)))
        lines.append("| " + d + " | " + " | ".join(cells) + " |")
    avg = []
    for p in profiles:
        sel = [r["recall"] for r in rows if r["profile"] == p]
        avg.append("%.2f" % (sum(sel) / len(sel)))
    lines.append("| **all** | " + " | ".join(avg) + " |")
    secs = []
    for p in profiles:
        sel = [r["seconds"] for r in rows if r["profile"] == p]
        secs.append("%.1f" % (sum(sel) / len(sel)))
    lines.append("| mean seconds/file | " + " | ".join(secs) + " |")
    return "\n".join(lines)


if __name__ == "__main__":
    only = sys.argv[sys.argv.index("--only") + 1].split(",") if "--only" in sys.argv else None
    data = evaluate(only=only)
    print(table(data))
    if "--json" in sys.argv:
        with open(sys.argv[sys.argv.index("--json") + 1], "w", encoding="utf-8") as f:
            json.dump(data, f, ensure_ascii=False, indent=1)
