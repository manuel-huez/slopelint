# Embedding comparison, 2026-08-26

## Scope and method

The earlier 25-function raw-code probe did not test today's generated-signature
pipeline. This comparison separates raw-code vectors from frozen Luna signatures.
The initial run tested deployed GGUF artifacts. The expanded CPU run also tests
original HF weights, graph engines, and selected INT8 variants. Neither run claims
to cover every precision or prompting variant of a model family.

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
signature generation. Frozen signatures isolate differences between embedding
candidates; they do not turn generator omissions into correct descriptions.
Model-specific prefixes were selected from model instructions before scoring:

- [Jina code model](https://huggingface.co/jinaai/jina-embeddings-v2-base-code): plain input, mean pooling.
- [MiniLM](https://ollama.com/library/all-minilm): plain input, mean pooling.
- [EmbeddingGemma](https://huggingface.co/google/embeddinggemma-300m): sentence-similarity prefix, mean pooling and learned projection.
- [Qwen3 0.6B](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B): fixed behavior-equivalence instruction in its documented `Instruct`/`Query` format, last-token pooling.

The expanded [models.json](models.json) pins prefixes, URLs, sizes, and SHA-256 digests. The initial four-model run below used the earlier native-runtime configuration.
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

## Expanded code-model candidates

The additional models use one symmetric vector per block. Retrieval query/passage
pairs are not interchangeable with this duplicate detector's symmetric index.
Prefixes were frozen before labels were scored, separately for code and descriptions:

- [CodeRankEmbed](https://huggingface.co/nomic-ai/CodeRankEmbed): plain input, CLS pooling.
- [CodeSage Small](https://huggingface.co/codesage/codesage-small-v2) and [Base v2](https://huggingface.co/codesage/codesage-base-v2): EOS token, masked mean pooling.
- [Jina code 0.5B](https://huggingface.co/jinaai/jina-code-embeddings-0.5b-GGUF): distinct candidate-code and candidate-comment prefixes, last-token pooling. Its NC license did not exclude it from this noncommercial test.

Jina code 1.5B, BGE code v1 1.5B, and Nomic Embed Code 7B were removed from the
final run queue at the user's request: the target is faster operation on this
CPU. Their initial cache-affected measurements and interrupted retries are not
published as completed comparisons. The final comparison covers nine checkpoints
and eighteen engine/precision configurations.

[Mistral Codestral Embed](https://mistral.ai/news/codestral-embed/) is a hosted
embedding offering, with no public downloadable weights found for this local CPU
comparison. No private code was sent to a hosted embedding provider.

The CPU probe compares PyTorch/MKL FP32, ONNX Runtime FP32/dynamic INT8,
OpenVINO FP32/calibrated INT8, and current llama.cpp where each architecture is
supported. The Xeon D-2123IT supports AVX-512 but not VNNI/BF16/AMX. Loaded
`libggml-cpu-skylakex.so` confirms llama.cpp selected its AVX-512 backend.
ONNX uses all graph optimizations. OpenVINO uses one CPU stream, two threads,
FP32 precision hints, and no CPU pinning. See the
[Sentence Transformers engine guidance](https://www.sbert.net/docs/sentence_transformer/usage/efficiency.html).
Engine choice must preserve pooling, special tokens, and numerical behavior;
quantization is measured as a separate quality configuration.

The 0.5B FP32 export probe used about 8 GiB at its high point. Larger decoder
exports were not attempted on this shared 31 GiB host. The final retained models
use original FP32 references and optimized CPU runs without risking the other
workload's memory. This is a bounded comparison of suitable engines, not
proof of a globally optimal engine or quantization for every CPU.

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

Linux amd64, CGO, Go, and Python 3.12+ are required. The expanded manifest
compares nine model checkpoints across eighteen engine/precision configurations. Each model
runs locally. Downloads, graph export, and model loading are separate from timed
inference. Allow tens of GB of model and export storage. The runner pins every
model file and the llama.cpp release archive by SHA-256; it never installs a
production model. Frozen descriptions require no Codex calls:

```sh
python3 -m venv /tmp/slopelint-bench-env
/tmp/slopelint-bench-env/bin/pip install torch==2.6.0 --index-url https://download.pytorch.org/whl/cpu
/tmp/slopelint-bench-env/bin/pip install transformers==4.51.3 einops==0.8.1 numpy==2.4.6 onnx==1.22.0 onnxruntime==1.29.0 openvino==2026.3.1 nncf==3.3.0
/tmp/slopelint-bench-env/bin/python scripts/benchmark-similarity.py --out /tmp/slopelint-model-results
```

Use `--corpus PATH` for another already-labeled, frozen corpus; `--models PATH`
selects a benchmark manifest. Keep confidential fixtures and output directories
outside a public checkout. `--analyze-only` recomputes calibration/held-out metrics
from existing per-model result files without inference. Results bind both corpus
bytes and the exact model manifest, including per-channel prefixes, engine, precision, threads, and batch size;
changing either requires another run. The public subset cannot
reproduce the private aggregate rows.

The opt-in Go test reads `SLOPELINT_BENCHMARK_CONFIG`. Its JSON contains `corpus`,
`output`, `model_path`, `source_prefix`, `description_prefix`, and `max_tokens`. Omitting `model_path` explicitly
runs generation for missing signatures; copy the returned records into the corpus
and freeze them before any model comparison. Normal `go test` skips this probe.
The same export supplies normalized chunks to the CPU worker. Its saved float32
vectors return to Go through the test's `vectors` field: production matrix packing,
dot products, chunk reduction, and thresholds score every engine. Imports reject
missing, non-unit, or dimensionally inconsistent vectors and preserve engine
timing/RSS metadata. Python does not copy production scoring math or policy.
`runtime` selects `native`, `llama.cpp`, `torch-fp32`, `onnx-fp32`,
`onnx-int8`, `openvino-fp32`, or `openvino-int8` in a benchmark manifest. HF exports
include model-specific pooling. ONNX INT8 uses dynamic per-channel reduced-range
quantization. OpenVINO INT8 uses NNCF mixed transformer quantization, calibrated
only on the calibration split, never held-out inputs. All benchmark inference is
offline after pinned artifacts are downloaded.

HF exports run in a separate process, so export memory is excluded from inference
RSS. The llama.cpp server binds only loopback, uses an ephemeral API key, and stops
after each candidate. This release's [embedding route](https://github.com/ggml-org/llama.cpp/blob/b10621/tools/server/server-context.cpp#L5262)
ignores `cache_prompt=false`, so the worker explicitly erases all four slots before every batch and
checks each acknowledgment. Erasure time is included. No slot files are saved.
The initial decoder measurements were discarded and rerun with this correction.
Its RSS is the sum of client and
server high-water marks, a conservative upper bound rather than simultaneous
resident memory. All runs use two CPU threads and batches of four. Python/runtime
memory remains included. These figures are not pure model-memory measurements.

## Initial four-model results and decision

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
interchangeable with the native measurements. The initial native runner reported that
load failure instead of silently switching engines. The expanded manifest names
each runtime explicitly and tests compatible ggml-org Gemma Q8/QAT Q4 exports
with current llama.cpp. The old Ollama BF16 file also fails in that engine.

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

## Initial-run verification

The full repository health gate passes: Go tests, vet, golangci-lint, and the
slopelint self-check. Additional isolated checks verify that already-frozen
signatures need no Codex process, oversized inputs fail before embedding, and
analysis rejects scores from another corpus. Re-running MiniLM with the final
runner preserved all 52 pair decisions and every calibrated confusion matrix;
maximum cosine drift was 0.000091. The final Qwen smoke agrees within 0.001 cosine.
All four GGUF file digests were verified. Production model/policy identity and
accepted-pair IDs are unchanged.

## Expanded CPU results

Same 22 scored held-out pairs: 10 equivalents, 12 different contracts. Thresholds
are fitted on calibration only. `TP/FP/FN` means found equivalents / false matches /
missed equivalents. These selected challenge cases are not a general accuracy estimate.

### Raw code versus frozen descriptions

The main rows use FP32 graph engines for CodeRank, CodeSage, and Jina 0.5B.
Their other engine/precision variants remain in the complete tables below.

| Model | Raw TP/FP/FN | Description TP/FP/FN | Raw AUC | Description AUC | Current OR TP/FP/FN |
|---|---:|---:|---:|---:|---:|---:|
| jina | 8/12/2 | 3/2/7 | 0.000 | 0.583 | 0/4/10 |
| minilm | 10/12/0 | 8/5/2 | 0.183 | 0.600 | 0/2/10 |
| coderank-openvino-fp32 | 10/12/0 | 8/8/2 | 0.000 | 0.625 | 0/2/10 |
| codesage-small-onnx-fp32 | 10/12/0 | 6/6/4 | 0.083 | 0.567 | 0/1/10 |
| codesage-base | 9/12/1 | 7/7/3 | 0.083 | 0.633 | 0/2/10 |
| gemma | 10/12/0 | 8/9/2 | 0.100 | 0.700 | 5/2/5 |
| gemma-qat-q4 | 10/12/0 | 8/6/2 | 0.083 | 0.708 | 5/2/5 |
| jina-code-0.5b-openvino-fp32 | 8/12/2 | 1/1/9 | 0.000 | 0.650 | 0/4/10 |
| qwen | 9/12/1 | 5/2/5 | 0.033 | 0.700 | 0/4/10 |

The two separate-channel count columns use each channel's calibration-best-F1
threshold. The last column keeps production thresholds and their OR rule. Full
eligible-only, strict-calibration, and public-only results remain in `results.json`.

### Production-length eligible subset

Fourteen held-out pairs meet the production token minimum: five equivalents and
nine different contracts. Separate channels still use calibration-best-F1 thresholds;
strict OR uses zero-calibration-false-positive thresholds. Neither is a proposed policy.

| Model | Raw TP/FP/FN | Description TP/FP/FN | Current OR TP/FP/FN | Strict OR TP/FP/FN |
|---|---:|---:|---:|---:|---:|
| jina | 4/9/1 | 2/1/3 | 0/2/5 | 0/0/5 |
| minilm | 5/9/0 | 5/3/0 | 0/1/5 | 0/2/5 |
| coderank-openvino-fp32 | 5/9/0 | 5/7/0 | 0/2/5 | 0/0/5 |
| codesage-small-onnx-fp32 | 5/9/0 | 5/6/0 | 0/0/5 | 0/0/5 |
| codesage-base | 4/9/1 | 5/6/0 | 0/1/5 | 0/0/5 |
| gemma | 5/9/0 | 5/7/0 | 3/1/2 | 2/1/3 |
| gemma-qat-q4 | 5/9/0 | 5/4/0 | 3/1/2 | 2/1/3 |
| jina-code-0.5b-openvino-fp32 | 4/9/1 | 1/1/4 | 0/2/5 | 0/0/5 |
| qwen | 5/9/0 | 4/2/1 | 0/2/5 | 0/0/5 |

### CPU time and memory

Loaded-model seconds average two complete uncached-vector passes per channel.
Downloads, graph export, model load, and input-limit validation are excluded.
The HF worker includes tokenization, inference, and normalization in each pass.
Candidates run sequentially; stocks work and brief Go lint checks share the host.
Treat times as approximate workload measurements, not isolated hardware claims.

| Configuration | Raw seconds | Description seconds | Total seconds | Peak RSS MiB |
|---|---:|---:|---:|---:|
| jina (llama.cpp) | 28.17 | 14.44 | 42.62 | 749 |
| minilm (llama.cpp) | 3.80 | 1.92 | 5.72 | 311 |
| coderank (llama.cpp) | 22.96 | 12.22 | 35.18 | 578 |
| codesage-small (openvino-fp32) | 13.79 | 7.07 | 20.87 | 1337 |
| codesage-base (openvino-fp32) | 41.92 | 22.65 | 64.58 | 3107 |
| codesage-small-torch-fp32 (torch-fp32) | 13.38 | 7.05 | 20.43 | 1134 |
| codesage-small-onnx-fp32 (onnx-fp32) | 11.67 | 6.02 | 17.69 | 1083 |
| codesage-small-onnx-int8 (onnx-int8) | 6.76 | 3.55 | 10.30 | 617 |
| codesage-small-openvino-int8 (openvino-int8) | 6.43 | 3.43 | 9.86 | 1167 |
| coderank-openvino-fp32 (openvino-fp32) | 17.95 | 8.41 | 26.36 | 1664 |
| coderank-onnx-int8 (onnx-int8) | 10.78 | 5.12 | 15.90 | 685 |
| gemma (llama.cpp) | 33.54 | 22.78 | 56.33 | 1692 |
| jina-code-0.5b (llama.cpp) | 104.45 | 64.23 | 168.68 | 1848 |
| qwen (llama.cpp) | 154.39 | 110.35 | 264.74 | 2469 |
| jina-native (native llama-go) | 27.79 | 20.54 | 48.32 | 703 |
| coderank-native (native llama-go) | 34.53 | 14.92 | 49.45 | 520 |
| jina-code-0.5b-openvino-fp32 (openvino-fp32) | 44.09 | 26.13 | 70.22 | 4021 |
| gemma-qat-q4 (llama.cpp) | 29.08 | 13.02 | 42.11 | 1694 |

### Engine and precision quality controls

| Configuration | Raw AUC | Description AUC | Description TP/FP/FN | Current OR TP/FP/FN |
|---|---:|---:|---:|---:|
| coderank | 0.000 | 0.625 | 8/8/2 | 0/2/10 |
| codesage-small | 0.083 | 0.567 | 6/6/4 | 0/1/10 |
| codesage-small-torch-fp32 | 0.083 | 0.567 | 6/6/4 | 0/1/10 |
| codesage-small-onnx-int8 | 0.083 | 0.525 | 7/8/3 | 0/1/10 |
| codesage-small-openvino-int8 | 0.092 | 0.575 | 6/5/4 | 0/1/10 |
| coderank-onnx-int8 | 0.000 | 0.642 | 7/6/3 | 0/0/10 |
| jina-code-0.5b | 0.000 | 0.633 | 1/1/9 | 0/4/10 |
| jina-native | 0.000 | 0.583 | 3/2/7 | 0/4/10 |
| coderank-native | 0.000 | 0.625 | 8/8/2 | 0/2/10 |

Full-input vector and decision comparisons are recorded under `full_engine_parity`.
CodeSage FP32 engine checks cover all 95 source and 93 description inputs, including varied
padding and sequence lengths. The CodeRank exporter initializes its rotary cache
with the longest tokenized input before tracing; disabling trace verification was
not used to hide the initial export failure.

### Decision for this CPU

**No production model, thresholds, reasoning setting, or cache identity changed.**
The remaining large models were stopped; this work does not recommend larger
models for the target hardware.

- MiniLM is the speed leader: 5.72s and 311 MiB, versus current native Jina's
  48.32s and 703 MiB. Its strict calibration policy still produces two held-out
  false matches. Speed is established; a safe drop-in replacement is not.
- Gemma QAT Q4 is the most useful follow-up candidate under current thresholds
  on this challenge set. It finds three of five eligible equivalents with one
  false match among nine different contracts; Jina finds none with two false
  matches. Its 42.11s is close to Jina's time on this shared host, while its
  1694 MiB RSS is about 2.4 times larger. This small selected set is insufficient
  to approve a default switch, and current Go-native compatibility is untested.
- Engine choice has measurable value. CodeRank OpenVINO takes 26.36s versus
  native's 49.45s, with unchanged current decisions on all 52 pairs. CodeSage
  Small ONNX FP32 takes 17.69s versus PyTorch's 20.43s; maximum labeled-pair
  cosine drift is below 0.0000006, with unchanged current decisions.
- INT8 is not an automatic quality-preserving change. Dynamic ONNX INT8 changes
  six CodeSage channel decisions across all 52 pairs, with labeled-pair drift
  up to 0.147. Calibrated OpenVINO INT8 retains current decisions here, but changes
  vectors and calibrated results. Both remain separate measured configurations.
- Even with suitable engines, Jina 0.5B FP32 costs 70.22s and 4021 MiB; Qwen
  costs 264.74s and 2469 MiB. Neither fits the goal of faster, lighter operation.
  Qwen's bounded original-FP32 check also shows that engine/quantization changes
  cannot be assumed to preserve exact scores or threshold decisions.
- Descriptions rank equivalents above different contracts better than raw code
  for every retained configuration on these cases. The raw-code OR branch can
  still report a boundary change that the description distinguishes correctly.
  A model swap alone does not repair that policy or missing description context.

### Expanded-run verification

All eighteen retained configurations completed both uncached passes for both
channels. Every CPU vector set was scored through the production Go owner;
corpus/config bindings and all published aggregates were independently rechecked.
Every llama.cpp result records 384 acknowledged slot erasures. The separate
slot test confirms prior token state is removed and repeated vectors agree.

The full code-health gate passed: Go tests, vet, golangci-lint, and semantic
self-check. The self-check generated seven signatures for changed benchmark Go
code; the comparison's frozen corpus and descriptions were not regenerated.
Additional checks reject changed normalized source, wrong corpus/prefix,
missing or invalid vectors, inconsistent dimensions, oversized inputs, and
corrupt artifacts. Rescoring is idempotent and preserves vectors, timing, and RSS.
Cancellation and input-limit failure stop the worker's temporary model server.

Public scores cover only the 39 public pairs. Private names, source, descriptions,
and vectors are absent from the report. Only aggregate private metrics remain.
The generated self-check stamp retains its model/policy identity and accepted IDs.
