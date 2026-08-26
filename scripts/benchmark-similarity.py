#!/usr/bin/env python3
"""Compare pinned local embeddings; never write production caches."""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import statistics
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
DATA = ROOT / "internal/lint/testdata/similarity_models"
LLAMA_RELEASE = "b10621"
LLAMA_SHA256 = "91d7b03ddae498a39f28fdb85d84d2b4a0fd3838d10b4f897e0ef8975bb9b583"


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


def download_verified(path, url, digest):
    path.parent.mkdir(parents=True, exist_ok=True)
    source = path if path.exists() else path.with_name(path.name + ".part")
    if source != path:
        urllib.request.urlretrieve(url, source)
    with source.open("rb") as stream:
        if hashlib.file_digest(stream, "sha256").hexdigest() != digest:
            raise ValueError("artifact digest mismatch: " + str(source))
    if source != path:
        source.replace(path)
    return path


def acquire_model(model, cache):
    if "files" in model:
        directory = cache.parent / "models-transformers" / model.get("model_name", model["name"])
        for file in model["files"]:
            url = f'https://huggingface.co/{model["repo"]}/resolve/{model["revision"]}/{file["file"]}'
            download_verified(directory / file["file"], url, file["sha256"])
        return directory

    return download_verified(cache / (model["sha256"] + ".gguf"), model["url"], model["sha256"])


def acquire_llama(cache):
    directory = cache.parent / "engines" / ("llama-" + LLAMA_RELEASE)
    archive = directory.with_suffix(".tar.gz")
    download_verified(archive,
                      f"https://github.com/ggml-org/llama.cpp/releases/download/{LLAMA_RELEASE}/llama-{LLAMA_RELEASE}-bin-ubuntu-x64.tar.gz",
                      LLAMA_SHA256)
    # Re-extract verified bytes, so an existing executable cannot bypass integrity checks.
    with tarfile.open(archive) as bundle:
        bundle.extractall(directory, filter="data")
    return directory / ("llama-" + LLAMA_RELEASE) / "llama-server"


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
        records = Path(temporary) / "records.json"
        for model in models:
            name = model["name"]
            output = args.out / (name + ".json")
            model_digest = hashlib.sha256(json.dumps(model, sort_keys=True).encode()).hexdigest()
            if not args.analyze_only:
                # Any failed attempt, including acquisition, must not leave stale success.
                output.unlink(missing_ok=True)
                model_path = acquire_model(model, args.model_dir)
                config = dict(model, corpus=str(args.corpus.resolve()), output=str(output.resolve()),
                              model_path=str(model_path.resolve()), source_prefix=model["source_prefix"],
                              description_prefix=model["description_prefix"], max_tokens=model["max_tokens"])
                config.setdefault("threads", 2)
                config.setdefault("batch", 4)
                runtime = model.get("runtime", "native")
                if runtime not in {"native", "llama.cpp", "torch-fp32", "onnx-fp32", "onnx-int8", "openvino-fp32", "openvino-int8"}:
                    raise ValueError("unsupported benchmark runtime: " + runtime)
                scorer = [str(binary), "-test.v", "-test.run", "^TestSimilarityModelBenchmark$", "-test.timeout", "2h"]
                command = scorer
                if runtime != "native":
                    if not records.exists():
                        export_config = Path(temporary) / "export.json"
                        export_config.write_text(json.dumps(dict(corpus=config["corpus"], output=str(records))))
                        subprocess.run(command, env={**os.environ, "SLOPELINT_BENCHMARK_CONFIG": str(export_config)}, check=True)
                    config["records"] = str(records)
                if runtime == "llama.cpp":
                    config["engine_path"] = str(acquire_llama(args.model_dir))
                elif runtime.startswith(("onnx", "openvino")):
                    suffix = ".onnx" if runtime.startswith("onnx") else ".xml"
                    artifact_name = "model-int8" if runtime.endswith("int8") else "model"
                    config["artifact_path"] = str((args.out / "artifacts" / name / (artifact_name + suffix)).resolve())
                config_path = Path(temporary) / "config.json"
                config_path.write_text(json.dumps(config))
                if runtime != "native":
                    command = [sys.executable, str(ROOT / "scripts/benchmark-similarity-cpu.py"), str(config_path)]
                # OpenVINO/NNCF disable telemetry in CI; this only affects benchmark children.
                environment = {**os.environ, "GOMAXPROCS": "2", "LLAMA_LOG": "error", "CI": "true",
                               "HF_HUB_OFFLINE": "1", "HF_HUB_DISABLE_TELEMETRY": "1",
                               "TOKENIZERS_PARALLELISM": "false", "SLOPELINT_BENCHMARK_CONFIG": str(config_path)}
                with (args.out / (name + ".log")).open("w") as log:
                    if "artifact_path" in config:
                        preparation = subprocess.run(command + ["--prepare"], env=environment,
                                                     stdout=log, stderr=subprocess.STDOUT)
                        if preparation.returncode:
                            results["models"][name] = {"error": "export failed; see " + name + ".log"}
                            continue
                    run = subprocess.run(command, env=environment, stdout=log, stderr=subprocess.STDOUT)
                    if run.returncode == 0 and runtime != "native":
                        # Reuse production float32 scoring; engines only produce vectors.
                        config_path.write_text(json.dumps(dict(corpus=config["corpus"], output=config["output"],
                                                               vectors=config["output"], source_prefix=model["source_prefix"],
                                                               description_prefix=model["description_prefix"])))
                        run = subprocess.run(scorer, env=environment, stdout=log, stderr=subprocess.STDOUT)
                if run.returncode:
                    output.unlink(missing_ok=True)
                    results["models"][name] = {"error": "candidate failed; see " + name + ".log"}
                    continue
                record = json.loads(output.read_text())
                record["benchmark_model_sha256"] = model_digest
                if runtime == "llama.cpp":
                    record["engine_version"] = LLAMA_RELEASE
                    record["engine_archive_sha256"] = LLAMA_SHA256
                if "artifact_path" in config:
                    artifact = Path(config["artifact_path"])
                    artifacts = [artifact] + ([artifact.with_suffix(".bin")] if runtime.startswith("openvino") else [])
                    record["export_sha256"] = {}
                    for path in artifacts:
                        with path.open("rb") as stream:
                            record["export_sha256"][path.name] = hashlib.file_digest(stream, "sha256").hexdigest()
                output.write_text(json.dumps(record, indent=2) + "\n")
            if output.exists():
                record = json.loads(output.read_text())
                if record.get("corpus_sha256") != results["corpus_sha256"]:
                    raise ValueError("result belongs to another corpus: " + name)
                if record.get("benchmark_model_sha256") != model_digest:
                    raise ValueError("result belongs to another model configuration: " + name)
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
