# Embedding benchmark

Compare raw code and frozen descriptions with local CPU models. The runner uses
production Go scoring, keeps calibration and held-out pairs separate, and does
not change production models or caches. Normal Go tests skip inference.

## Run

Requires Linux amd64, CGO, Go, and Python 3.12+. Run from the repository root:

```sh
python3 -m venv /tmp/slopelint-bench-env
/tmp/slopelint-bench-env/bin/pip install torch==2.6.0 --index-url https://download.pytorch.org/whl/cpu
/tmp/slopelint-bench-env/bin/pip install transformers==4.51.3 einops==0.8.1 numpy==2.4.6 onnx==1.22.0 onnxruntime==1.29.0 openvino==2026.3.1 nncf==3.3.0
/tmp/slopelint-bench-env/bin/python scripts/benchmark-similarity.py --out /tmp/slopelint-model-results
```

The runner downloads digest-pinned artifacts from [models.json](models.json).
Allow tens of GB for weights and graph exports. Inference stays local; frozen
descriptions require no Codex calls.

Use `--models PATH` to select configurations, `--corpus PATH` for another labeled
corpus with frozen descriptions, and `--analyze-only` to recompute metrics from
saved runs without inference. Changed corpus or model configuration requires a fresh run.

Keep generated results, logs, exports, and private fixtures outside the checkout.
The checked-in [corpus.json](corpus.json) contains public synthetic fixtures only.
