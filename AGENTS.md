# AGENTS.md

Ask before changing the `.slopelint.json` stamp format or cache format.

## Release

- Releases are Go module tags. Tag only a commit that passed the full health
  gate; push the tag and check the remote tag resolves to that commit.
- Untagged commits never become `@latest`. Install with
  `go install github.com/manuel-huez/slopelint/cmd/slopelint@<tag>`; if a proxy
  returns an older tag, retry with `GOPROXY=direct`.
- Update every active install (`~/.local/bin/slopelint`,
  `$(go env GOPATH)/bin/slopelint`; set `GOBIN` per destination). Check each with
  `go version -m`, compare SHA-256 hashes, and run it on this repository.
