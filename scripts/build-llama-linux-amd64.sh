#!/usr/bin/env bash

set -euo pipefail

llama_go_commit="9cd5256084b05c45b9f7816c1fb8b0edfd75450a"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
target_dir="$repo_root/internal/lint/native/linux_amd64"
build_dir="$(mktemp -d "${TMPDIR:-/tmp}/slopelint-llama.XXXXXX")"

cleanup() {
  rm -rf "$build_dir"
}

trap cleanup EXIT

for command in cmake git make; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "missing build command: $command" >&2
    exit 1
  fi
done

git clone --filter=blob:none --no-checkout https://github.com/tcpipuk/llama-go.git "$build_dir/llama-go"
git -C "$build_dir/llama-go" fetch --depth=1 origin "$llama_go_commit"
git -C "$build_dir/llama-go" checkout --detach "$llama_go_commit"
git -C "$build_dir/llama-go" submodule update --init --depth=1

jobs="${SLOPELINT_LLAMA_BUILD_JOBS:-$(getconf _NPROCESSORS_ONLN)}"

cmake -S "$build_dir/llama-go/llama.cpp" -B "$build_dir/llama-go/build" \
  -DGGML_NATIVE=OFF \
  -DBUILD_SHARED_LIBS=OFF \
  -DLLAMA_CURL=OFF
cmake --build "$build_dir/llama-go/build" \
  --config Release \
  --target ggml llama llama-common \
  --parallel "$jobs"
cp "$build_dir/llama-go/build/ggml/src/CMakeFiles/ggml-base.dir/ggml.c.o" \
  "$build_dir/llama-go/llama.cpp/ggml.o"

make -C "$build_dir/llama-go" -j"$jobs" \
  CMAKE_ARGS='-DGGML_NATIVE=OFF -DBUILD_SHARED_LIBS=OFF' \
  libbinding.a

mkdir -p "$target_dir"
find "$target_dir" -maxdepth 1 -type f -name '*.a' -delete

for library in \
  libbinding.a \
  libllama-common.a \
  libllama-common-base.a \
  libllama.a \
  libggml-cpu.a \
  libggml.a \
  libggml-base.a; do
  install -m 0644 "$build_dir/llama-go/$library" "$target_dir/$library"
done

install -m 0644 "$build_dir/llama-go/LICENSE" "$repo_root/internal/lint/native/LICENSE.llama-go"
install -m 0644 "$build_dir/llama-go/llama.cpp/LICENSE" "$repo_root/internal/lint/native/LICENSE.llama.cpp"
