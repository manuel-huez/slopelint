# Embedding comparison, 2026-08-26

## Scope and method

The earlier 25-function raw-code probe did not test today's generated-signature
pipeline. This comparison separates raw-code vectors from frozen Luna signatures.
It tests deployed GGUF artifacts, not every precision or prompting variant of a
model family.

Labels were frozen before candidate scoring: 29 previously inspected calibration
pairs and 23 held-out pairs. Two cases require unavailable helper/package context
and are excluded from model selection. No normalized function appears in both
splits. Related private fixture variants stay in the held-out split. This is a
small, deliberately difficult challenge set, not a representative estimate of
repository warning rates. Most calibration functions fall below the production
50-token minimum, so eligible-only results are reported separately.
Synthetic snippets imply ordinary imports and supporting types; in particular,
`Config` in the JSON cases denotes a struct. Labels describe those intended
contracts, not equivalence under every possible definition of an omitted type.

The public corpus contains 39 synthetic pairs. Thirteen additional private
application pairs remain outside this repository, including four explicit
identifier-renaming controls; these are not independent duplicate discoveries.
Only aggregate private-corpus metrics are published. An extraction audit corrected
method receiver collisions before any held-out scores were examined; the initial
incomplete run was discarded.

Each model receives the same normalized source and the same frozen signatures
(`gpt-5.6-luna`, low reasoning, prompt schema 4). No embedding candidate changes
signature generation. Model-specific prefixes were selected from model
instructions before scoring:

- [Jina code model](https://huggingface.co/jinaai/jina-embeddings-v2-base-code): plain input, mean pooling.
- [MiniLM](https://ollama.com/library/all-minilm): plain input, mean pooling.
- [EmbeddingGemma](https://huggingface.co/google/embeddinggemma-300m): sentence-similarity prefix, mean pooling and learned projection.
- [Qwen3 0.6B](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B): fixed behavior-equivalence instruction in its documented `Instruct`/`Query` format, last-token pooling.

The exact prefixes, URLs, sizes, and SHA-256 digests are in [models.json](models.json).
Jina and MiniLM use F16, Gemma BF16, and Qwen Q8_0. Qwen uses the
[official GGUF artifact](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B-GGUF).

Native runs use the existing llama-go backend, CPU only, `GOMAXPROCS=2`, four
sequences, 4096 context, and 2048 batch/micro-batch tokens. The host has an Intel
Xeon D-2123IT at 2.20 GHz and eight allowed logical CPUs. Candidates run sequentially,
but other host workloads are present. Timings are approximate loaded-model
measurements, not isolated hardware benchmarks. Model loading, downloads, and Luna
generation are excluded from embedding time. The OS file cache is not flushed.
Each source/signature timing averages two uncached-vector passes in one process;
peak RSS covers that process. No vectors enter production caches.

Current 2000-byte overlapping chunks are preserved. The benchmark checks actual
model token counts against both the model limit and native per-sequence capacity,
and rejects over-limit input instead of dropping content or
changing chunks for one model. All tested native inputs fit without extra splits. Pair score is the maximum cosine
over chunk pairs, matching the current detector; this can hide a boundary change
in another chunk. This benchmark scores labeled pairs, not discovery/grouping of
all possible pairs in a repository.

## Calibration policies

1. **Current:** unchanged production thresholds, including locality and test role;
   either raw code or signature can report a pair.
2. **Zero calibration false positives:** for each channel, take the next float
   above its largest negative calibration score. Report each channel and their OR.
3. **Best calibration F1:** optimize each channel on calibration pairs only;
   ties prefer fewer false positives, then the higher threshold.

Policies 2 and 3 use one threshold per channel without role/locality offsets. They
are comparison policies, not proposed production settings. Held-out labels never
select thresholds. Zero calibration false positives do not establish zero future
false positives.

## Reproduce

Linux amd64, CGO, Go, and Python 3.11+ are required. The runner downloads the pinned
models into a separate benchmark cache and checks each SHA-256. It does not install
or select a production model. Frozen signatures mean no Codex calls are needed:

```sh
python3 scripts/benchmark-similarity.py --out /tmp/slopelint-model-results
```

Use `--corpus PATH` for another already-labeled, frozen corpus; `--models PATH`
selects a benchmark manifest. Keep confidential fixtures and output directories
outside a public checkout. `--analyze-only` recomputes calibration/held-out metrics
from existing per-model result files without inference. The public subset cannot
reproduce the private aggregate rows.

The opt-in Go test reads `SLOPELINT_BENCHMARK_CONFIG`. Its JSON contains `corpus`,
`output`, `model_path`, `prefix`, and `max_tokens`. Omitting `model_path` explicitly
runs generation for missing signatures; copy the returned records into the corpus
and freeze them before any model comparison. Normal `go test` skips this probe.

## Results and decision

**Keep Jina for now; do not treat this as proof that Jina is optimal.** No model,
threshold, reasoning setting, or cache identity changed. No cache rebuild needed.
The tested alternatives did not establish a safe replacement across precision,
speed, and native-runtime compatibility. This benchmark is not a sign-off on
semantic warning precision.

Full held-out set: 10 equivalent and 12 different pairs; context-only case excluded.
`TP/FP/FN` means found equivalents / false warnings / missed equivalents.

| Model | Source + signature seconds | Peak RSS MiB | Signature TP/FP/FN, calibration-best F1 | Current OR TP/FP/FN | Strict OR TP/FP/FN |
|---|---:|---:|---:|---:|---:|
| jina | 43.37 | 703 | 3/2/7 | 0/4/10 | 0/0/10 |
| minilm | 5.60 | 245 | 8/5/2 | 0/2/10 | 0/2/10 |
| qwen | 331.91 | 2204 | 7/2/3 | 0/3/10 | 0/0/10 |
| gemma-reference | 43.37 | 1215 | 8/9/2 | 5/2/5 | 2/1/8 |

Strict OR uses the zero-calibration-false-positive thresholds. Gemma is a
**separate Ollama 0.32.15 reference**, not a native-runtime result. Its RSS covers
server plus runner, excluding the Python driver. Two CPU threads, one server
slot, `num_ctx=2048`, `truncate=false`, and the same chunks/prefix were used.
Its startup includes a warm-up embedding. Reference timing/RSS is not directly
interchangeable with the native measurements. The public runner reports the
native load failure instead of silently switching engines.

Production-eligible held-out subset: 5 equivalent and 9 different pairs.

| Model | Signature TP/FP/FN, calibration-best F1 | Current OR TP/FP/FN | Strict OR TP/FP/FN |
|---|---:|---:|---:|
| jina | 2/1/3 | 0/2/5 | 0/0/5 |
| minilm | 5/3/0 | 0/1/5 | 0/2/5 |
| qwen | 5/2/0 | 0/1/5 | 0/0/5 |
| gemma-reference | 5/7/0 | 3/1/2 | 2/1/3 |

Channel ranking on all 22 scored held-out pairs:

| Model | Raw-code ROC AUC | Signature ROC AUC |
|---|---:|---:|
| jina | 0.000 | 0.583 |
| minilm | 0.183 | 0.600 |
| qwen | 0.033 | 0.700 |
| gemma-reference | 0.100 | 0.708 |

Raw-code ranking is poor on this challenge set: a changed boundary inside nearly
identical code often scores higher than equivalent code with different names or
control flow. The tiny, selected set does not estimate general repository AUC.

Findings:

- MiniLM is about 7.8 times faster and uses less memory, but trades known matches
  and adds two held-out false warnings under its strict calibration policy. It
  is a useful speed candidate, not a proven safe substitution.
- Qwen improves signature recall at its calibration-best-F1 point, but still
  produces two false warnings. It costs about 7.7 times Jina's embedding time
  and 3.1 times its RSS. Only the recorded Q8/instruction configuration was tested.
- Gemma gives promising signature ranking and finds more held-out equivalents
  at current thresholds. Its native load fails with the tensor-layout error
  recorded in `results.json`; the separate reference still has false warnings.
  Native support and a broader precision check are prerequisites to promotion.
- Jina's raw-code channel reports `duration-units` at 0.99649 and
  `json-unknown-fields` at 0.97848. Luna preserved the relevant distinctions;
  their signature scores are only 0.89222 and 0.85947. Because channels are ORed,
  a better signature model alone cannot remove these raw-code warnings.
- An identical wrapper with different unseen package-local sink types remains
  indistinguishable from its isolated function text. Context-only cases are
  excluded, not counted as model-selection wins or losses.
- Low reasoning remains unchanged. These embedding comparisons reuse identical
  signatures; they cannot establish low/medium equivalence. The earlier bounded
  reasoning comparison and its generation defects remain documented in the root
  README. Do not attribute every false match to reasoning effort.

[results.json](results.json) contains full aggregate metrics, exact calibrated
thresholds, per-phase timings, and public per-pair scores. Public-only summaries
allow the synthetic subset to be checked independently. Private snippets,
signatures, case names, and per-pair results are deliberately absent.

## Verification

The full repository health gate passes: Go tests, vet, golangci-lint, and the
slopelint self-check. Additional isolated checks verify that already-frozen
signatures need no Codex process, oversized inputs fail before embedding, and
analysis rejects scores from another corpus. Re-running MiniLM with the final
runner preserved all 52 pair decisions and every calibrated confusion matrix;
maximum cosine drift was 0.000091. The final Qwen smoke agrees within 0.001 cosine.
All four GGUF file digests were verified. Production model/policy identity and
accepted-pair IDs are unchanged.
