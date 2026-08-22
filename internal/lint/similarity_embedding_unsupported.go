//go:build !linux || !amd64 || !cgo

package lint

import (
	"errors"
	"runtime"
)

func newNativeSimilarityEmbedder(string) (similarityEmbedder, error) {
	return nil, errors.New(
		"local semantic similarity requires Linux amd64 with CGO; " +
			"CI stamp validation remains available on " + runtime.GOOS + "/" + runtime.GOARCH,
	)
}
