#!/usr/bin/env python3
"""Run the opt-in native embedding comparison; never write production caches."""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import statistics
import subprocess
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
DATA = ROOT / "internal/lint/testdata/similarity_models"


def metrics(pairs, scores, accept):
    counts = dict(tp=0, fp=0, tn=0, fn=0)
    for pair, score in zip(pairs, scores, strict=True):
        predicted = accept(score)
        counts[("t" if predicted == pair["same"] else "f") + ("p" if predicted else "n")] += 1
    tp, fp, fn = counts["tp"], counts["fp"], counts["fn"]
    return dict(counts, precision=tp / (tp + fp) if tp + fp else None,
                recall=tp / (tp + fn) if tp + fn else None,
                f1=2 * tp / (2 * tp + fp + fn) if 2 * tp + fp + fn else None)


def calibrate(pairs, scores, channel):
    negatives = [score[channel] for pair, score in zip(pairs, scores, strict=True) if not pair["same"]]
    zero_fp = math.nextafter(max(negatives), math.inf)
    candidates = sorted({score[channel] for score in scores} | {zero_fp})
    def rank(threshold):
        result = metrics(pairs, scores, lambda s: s[channel] >= threshold)
        return result["f1"] or 0, -result["fp"], threshold
    return {"zero_calibration_fp": zero_fp, "best_calibration_f1": max(candidates, key=rank)}


def summarize(corpus, result):
    scores = result["scores"]
    if [p["name"] for p in corpus] != [s["name"] for s in scores]:
        raise ValueError("corpus/result pair order differs")
    calibration = [i for i, p in enumerate(corpus) if p["split"] == "calibration" and not p["context_only"]]
    thresholds = {channel: calibrate([corpus[i] for i in calibration], [scores[i] for i in calibration], channel)
                  for channel in ("source", "description")}
    summary = {"thresholds": thresholds, "groups": {}}
    for split in ("calibration", "heldout"):
        for eligible in (False, True):
            selected = [i for i, p in enumerate(corpus) if p["split"] == split and not p["context_only"]
                        and (not eligible or p["eligible"])]
            pairs, values = [corpus[i] for i in selected], [scores[i] for i in selected]
            group = {"count": len(pairs)}
            for channel in ("source", "description"):
                group[channel + "_current"] = metrics(pairs, values, lambda s: s[channel + "_match"])
                for policy in ("zero_calibration_fp", "best_calibration_f1"):
                    threshold = thresholds[channel][policy]
                    group[channel + "_" + policy] = metrics(pairs, values, lambda s: s[channel] >= threshold)
                positive = [s[channel] for p, s in zip(pairs, values, strict=True) if p["same"]]
                negative = [s[channel] for p, s in zip(pairs, values, strict=True) if not p["same"]]
                group[channel + "_auc"] = (sum((a > b) + 0.5 * (a == b) for a in positive for b in negative)
                                          / (len(positive) * len(negative))) if positive and negative else None
            group["or_current"] = metrics(pairs, values, lambda s: s["source_match"] or s["description_match"])
            group["or_zero_calibration_fp"] = metrics(pairs, values, lambda s: any(
                s[c] >= thresholds[c]["zero_calibration_fp"] for c in ("source", "description")))
            summary["groups"][split + ("_eligible" if eligible else "_all")] = group
    summary["warm_seconds"] = sum(statistics.median(result[c + "_seconds"]) for c in ("source", "description"))
    summary["peak_rss_mib"] = result["peak_rss_kib"] / 1024
    return summary


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--corpus", type=Path, default=DATA / "corpus.json")
    parser.add_argument("--models", type=Path, default=DATA / "models.json")
    parser.add_argument("--model-dir", type=Path, default=Path.home() / ".cache/slopelint-benchmark/models-gguf")
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--analyze-only", action="store_true")
    args = parser.parse_args()
    corpus = json.loads(args.corpus.read_text())
    # Validate frozen bytes and split isolation before downloading or scoring.
    by_split = {}
    for pair in corpus:
        for side in ("left", "right"):
            if hashlib.sha256(pair[side].encode()).hexdigest() != pair[side + "_sha256"]:
                raise ValueError("changed fixture: " + pair["name"])
            if not pair[side + "_description"]:
                raise ValueError("freeze signatures before model comparison")
            by_split.setdefault(pair["split"], set()).add(pair[side + "_normalized_sha256"])
    if by_split["calibration"] & by_split["heldout"]:
        raise ValueError("shared normalized source across calibration and heldout")
    args.out.mkdir(parents=True, exist_ok=True)
    models = json.loads(args.models.read_text())
    results = {"corpus_sha256": hashlib.sha256(args.corpus.read_bytes()).hexdigest(), "models": {}}
    with tempfile.TemporaryDirectory(prefix="slopelint-model-benchmark-") as temporary:
        binary = Path(temporary) / "benchmark.test"
        if not args.analyze_only:
            subprocess.run(["go", "test", "-c", "-o", str(binary), "./internal/lint"], cwd=ROOT, check=True)
        for model in models:
            name = model["name"]
            output = args.out / (name + ".json")
            if not args.analyze_only:
                args.model_dir.mkdir(parents=True, exist_ok=True)
                model_path = args.model_dir / (model["sha256"] + ".gguf")
                if not model_path.exists():
                    part = model_path.with_suffix(".part")
                    urllib.request.urlretrieve(model["url"], part)
                    part.rename(model_path)
                with model_path.open("rb") as stream:
                    if hashlib.file_digest(stream, "sha256").hexdigest() != model["sha256"]:
                        raise ValueError("model digest mismatch: " + name)
                config = dict(corpus=str(args.corpus.resolve()), output=str(output.resolve()),
                              model_path=str(model_path.resolve()), prefix=model["prefix"], max_tokens=model["max_tokens"])
                config_path = Path(temporary) / "config.json"
                config_path.write_text(json.dumps(config))
                # Failed candidate must not leave a stale successful result.
                output.unlink(missing_ok=True)
                with (args.out / (name + ".log")).open("w") as log:
                    run = subprocess.run([str(binary), "-test.v", "-test.run", "^TestSimilarityModelBenchmark$", "-test.timeout", "30m"],
                                         env={**os.environ, "GOMAXPROCS": "2", "LLAMA_LOG": "error",
                                              "SLOPELINT_BENCHMARK_CONFIG": str(config_path)}, stdout=log, stderr=subprocess.STDOUT)
                if run.returncode:
                    results["models"][name] = {"error": "candidate failed; see " + name + ".log"}
                    continue
            if output.exists():
                record = json.loads(output.read_text())
                if record.get("corpus_sha256") != results["corpus_sha256"]:
                    raise ValueError("result belongs to another corpus: " + name)
                expected_tokens = [p[side + "_tokens"] for p in corpus for side in ("left", "right")]
                if record.get("tokens") != expected_tokens:
                    raise ValueError("fixture token counts changed: " + name)
                results["models"][name] = summarize(corpus, record)
            else:
                results["models"][name] = {"error": "no successful result"}
            print(name, results["models"][name].get("warm_seconds", "failed"), flush=True)
    (args.out / "summary.json").write_text(json.dumps(results, indent=2) + "\n")
    if not any("groups" in result for result in results["models"].values()):
        raise SystemExit("No model completed; inspect candidate logs")


if __name__ == "__main__":
    main()
