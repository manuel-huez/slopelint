# Native embedding libraries

`linux_amd64` contains portable static CPU archives for
`github.com/tcpipuk/llama-go` commit `9cd5256084b05c45b9f7816c1fb8b0edfd75450a`.
They make normal `go build` and `go install` produce one self-contained binary;
users do not need a C++ toolchain or a separate inference server.

Rebuild them on Linux amd64 with:

```bash
./scripts/build-llama-linux-amd64.sh
```

The rebuild disables host-specific `GGML_NATIVE` instructions and keeps
llama.cpp's optimized AVX2 CPU baseline. Runtime thread selection uses every
physical CPU core available to the process.
