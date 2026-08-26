#!/usr/bin/env python3
"""Isolated CPU engine comparison producing vectors from Go-owned chunks."""
from contextlib import contextmanager
import hashlib
import json
from pathlib import Path
import resource
import secrets
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

import numpy as np


def encoder(config):
    import torch
    from transformers import AutoModel
    model = AutoModel.from_pretrained(config['model_path'], trust_remote_code=True,
                                     local_files_only=True, torch_dtype=torch.float32,
                                     attn_implementation='eager').eval()

    class PooledEncoder(torch.nn.Module):
        def __init__(self):
            super().__init__()
            self.model = model

        def forward(self, input_ids, attention_mask):
            hidden = self.model(input_ids=input_ids, attention_mask=attention_mask).last_hidden_state
            if config['pooling'] == 'cls':
                return hidden[:, 0]
            if config['pooling'] == 'last':
                # Last non-padding token, for either padding direction.
                positions = torch.arange(attention_mask.shape[1])[None, :]
                last = (positions * attention_mask).max(1).values
                return hidden[torch.arange(hidden.shape[0]), last]
            return (hidden * attention_mask[:, :, None]).sum(1) / attention_mask.sum(1)[:, None]
    return PooledEncoder().eval()


def prepare(config, exported):
    """Export outside measured process; export RAM is not serving RAM."""
    import torch
    from transformers import AutoTokenizer
    torch.set_num_threads(config['threads'])
    tokenizer = AutoTokenizer.from_pretrained(config['model_path'], trust_remote_code=True, local_files_only=True)
    model = encoder(config)
    texts = [config[channel + '_prefix'] + chunk for channel in ('source', 'description')
             for chunks in exported[channel + '_chunks'] for chunk in chunks]
    longest = max(texts, key=lambda text: len(tokenizer(text, truncation=False)['input_ids']))
    example = tokenizer([longest] * 2, padding=True, truncation=False, return_tensors='pt')
    target = Path(config['artifact_path'])
    target.parent.mkdir(parents=True, exist_ok=True)
    with torch.inference_mode():
        # Initialize rotary caches before tracing; cover every benchmark sequence length.
        model(example['input_ids'], example['attention_mask'])
        if config['runtime'].startswith('onnx'):
            import onnxruntime.quantization as quantization
            source = target.with_name('model.onnx')
            torch.onnx.export(model, (example['input_ids'], example['attention_mask']), str(source),
                              input_names=['input_ids', 'attention_mask'], output_names=['embedding'],
                              dynamic_axes={'input_ids': {0: 'batch', 1: 'sequence'},
                                            'attention_mask': {0: 'batch', 1: 'sequence'},
                                            'embedding': {0: 'batch'}}, opset_version=17)
            if config['runtime'] == 'onnx-int8':
                quantization.quantize_dynamic(str(source), str(target), per_channel=True,
                                              reduce_range=True, weight_type=quantization.QuantType.QInt8)
        else:
            import openvino as ov
            converted = ov.convert_model(model, example_input=(example['input_ids'], example['attention_mask']),
                                         input=[ov.PartialShape([-1, -1]), ov.PartialShape([-1, -1])])
            if config['runtime'] == 'openvino-int8':
                import nncf
                corpus = json.loads(Path(config['corpus']).read_text())
                texts = sorted({config[channel + '_prefix'] + chunk
                                for i, pair in enumerate(corpus)
                                if pair['split'] == 'calibration' and not pair['context_only']
                                for channel in ('source', 'description') for side in (0, 1)
                                for chunk in exported[channel + '_chunks'][2 * i + side]}, key=lambda s: (len(s.encode()), s))
                batches = [tokenizer(texts[i:i + config['batch']], padding=True, truncation=False, return_tensors='np')
                           for i in range(0, len(texts), config['batch'])]
                dataset = nncf.Dataset(batches, lambda b: [b['input_ids'], b['attention_mask']])
                converted = nncf.quantize(converted, dataset, preset=nncf.QuantizationPreset.MIXED,
                                          model_type=nncf.ModelType.TRANSFORMER, subset_size=len(batches))
            ov.save_model(converted, target, compress_to_fp16=False)


@contextmanager
def llama_engine(config, metadata):
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    key = secrets.token_urlsafe(32)

    def call(path, body=None):
        request = urllib.request.Request('http://127.0.0.1:' + str(port) + path,
                                         data=None if body is None else json.dumps(body).encode(),
                                         headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + key})
        with urllib.request.urlopen(request, timeout=1800) as response:
            return json.load(response)

    with tempfile.TemporaryDirectory(prefix='slopelint-cpu-') as temporary:
        keyfile = Path(temporary) / 'key'
        keyfile.write_text(key)
        keyfile.chmod(0o600)
        with Path(config['output']).with_suffix('.server.log').open('w') as log:
            command = [config['engine_path'], '-m', config['model_path'], '-ngl', '0',
                       '-t', str(config['threads']), '-tb', str(config['threads']),
                       '-c', '4096', '-b', '2048', '-ub', '2048', '-np', '4', '--embedding',
                       '--pooling', config['pooling'], '--host', '127.0.0.1', '--port', str(port),
                       '--no-warmup', '--cache-ram', '0', '--api-key-file', str(keyfile),
                       '--slot-save-path', temporary]
            process = subprocess.Popen(command, stdout=log, stderr=subprocess.STDOUT)
            try:
                start = time.monotonic()
                while True:
                    if process.poll() is not None:
                        raise RuntimeError('llama-server exited; inspect server log')
                    try:
                        if call('/health').get('status') == 'ok':
                            break
                    except (OSError, ValueError):
                        pass
                    if time.monotonic() - start > 120:
                        raise TimeoutError('llama-server startup')
                    time.sleep(0.1)
                maps = Path(f'/proc/{process.pid}/maps').read_text().splitlines()
                metadata['cpu_libraries'] = sorted({line.split()[-1] for line in maps if 'libggml-cpu' in line})

                def embed(texts):
                    # This release ignores cache_prompt in embedding requests; erase each slot.
                    for slot in range(4):
                        cleared = call(f'/slots/{slot}?action=erase', {})
                        if cleared['id_slot'] != slot or cleared['n_erased'] < 0:
                            raise ValueError('slot erase failed')
                        metadata['slot_erases'] = metadata.get('slot_erases', 0) + 1
                        metadata['erased_tokens'] = metadata.get('erased_tokens', 0) + cleared['n_erased']
                    response = call('/v1/embeddings', {'input': texts})
                    entries = sorted(response['data'], key=lambda entry: entry['index'])
                    if len(entries) != len(texts):
                        raise ValueError('missing embeddings')
                    return np.asarray([entry['embedding'] for entry in entries], dtype=np.float32)
                yield embed, lambda text: len(call('/tokenize', {'content': text, 'add_special': True})['tokens'])
            finally:
                process.terminate()
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                metadata['server_peak_rss_kib'] = resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss


@contextmanager
def hf_engine(config, metadata):
    import torch
    import transformers
    torch.set_num_threads(config['threads'])
    torch.set_num_interop_threads(1)
    tokenizer = transformers.AutoTokenizer.from_pretrained(config['model_path'], trust_remote_code=True,
                                                           local_files_only=True)
    metadata['transformers'] = transformers.__version__
    runtime = config['runtime']
    if runtime.startswith('openvino'):
        import openvino as ov
        core = ov.Core()
        compiled = core.compile_model(config['artifact_path'], 'CPU', {
            'INFERENCE_NUM_THREADS': config['threads'], 'NUM_STREAMS': 1, 'PERFORMANCE_HINT': 'LATENCY',
            'INFERENCE_PRECISION_HINT': 'f32', 'ENABLE_CPU_PINNING': False})
        metadata['engine_version'] = ov.__version__
        run = lambda batch: compiled([batch['input_ids'], batch['attention_mask']])[0]
    elif runtime.startswith('onnx'):
        import onnxruntime as ort
        options = ort.SessionOptions()
        options.intra_op_num_threads = config['threads']
        options.inter_op_num_threads = 1
        options.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
        session = ort.InferenceSession(config['artifact_path'], sess_options=options, providers=['CPUExecutionProvider'])
        metadata['engine_version'] = ort.__version__
        run = lambda batch: session.run(None, {k: batch[k] for k in ('input_ids', 'attention_mask')})[0]
    else:
        model = encoder(config)
        metadata['engine_version'] = torch.__version__
        def run(batch):
            with torch.inference_mode():
                return model(torch.from_numpy(batch['input_ids']), torch.from_numpy(batch['attention_mask'])).numpy()
    yield lambda texts: run(tokenizer(texts, padding=True, truncation=False, return_tensors='np')), \
        lambda text: len(tokenizer(text, truncation=False)['input_ids'])


def main():
    # Let context-manager cleanup stop the local server on cancellation.
    def terminate(signum, frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGTERM, terminate)
    config = json.loads(Path(sys.argv[1]).read_text())
    if config['pooling'] not in {'mean', 'cls', 'last'}:
        raise ValueError('unsupported pooling')
    if config['threads'] < 1 or config['batch'] < 1:
        raise ValueError('threads and batch must be positive')
    corpus_bytes = Path(config['corpus']).read_bytes()
    corpus = json.loads(corpus_bytes)
    exported = json.loads(Path(config['records']).read_text())
    digest = hashlib.sha256(corpus_bytes).hexdigest()
    if exported['corpus_sha256'] != digest:
        raise ValueError('export belongs to another corpus')
    if [p['name'] for p in corpus] != [p['name'] for p in exported['thresholds']]:
        raise ValueError('export pair order differs')
    if len(sys.argv) == 3 and sys.argv[2] == '--prepare':
        prepare(config, exported)
        return
    result = dict(corpus_sha256=digest, tokens=exported['tokens'], runtime=config['runtime'],
                  threads=config['threads'], batch=config['batch'])
    start = time.monotonic()
    engine = llama_engine if config['runtime'] == 'llama.cpp' else hf_engine
    with engine(config, result) as (embed, count):
        result['load_seconds'] = time.monotonic() - start
        for channel in ('source', 'description'):
            raw = exported[channel + '_chunks']
            prefix = config[channel + '_prefix']
            inputs = sorted({prefix + chunk for chunks in raw for chunk in chunks}, key=lambda s: (len(s.encode()), s))
            counts = [count(text) for text in inputs]
            if not counts or max(counts) > min(config['max_tokens'], 1024):
                raise ValueError('input exceeds per-sequence token limit')
            timings = []
            for repeat in range(2):
                vectors = []
                start = time.monotonic()
                for index in range(0, len(inputs), config['batch']):
                    pooled = np.asarray(embed(inputs[index:index + config['batch']]), dtype=np.float64)
                    norms = np.linalg.norm(pooled, axis=1, keepdims=True)
                    if not np.isfinite(pooled).all() or (norms == 0).any():
                        raise ValueError('invalid embedding')
                    vectors.extend((pooled / norms).astype(np.float32).tolist())
                    if (index // config['batch'] + 1) % 4 == 0:
                        print(channel, repeat + 1, 'inputs', min(index + config['batch'], len(inputs)), '/', len(inputs), flush=True)
                timings.append(time.monotonic() - start)
                print(channel, repeat + 1, timings[-1], flush=True)
            result[channel + '_seconds'] = timings
            result[channel + '_inputs'] = len(inputs)
            result[channel + '_max_tokens'] = max(counts)
            result[channel + '_dimensions'] = len(vectors[0])
            result[channel + '_vectors'] = {hashlib.sha256(text.encode()).hexdigest(): vector
                                            for text, vector in zip(inputs, vectors, strict=True)}
    # Sum process high-water marks: conservative upper bound, not simultaneous RSS.
    result['peak_rss_kib'] = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss + result.get('server_peak_rss_kib', 0)
    Path(config['output']).write_text(json.dumps(result, indent=2) + '\n')


if __name__ == '__main__':
    main()
