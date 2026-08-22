//go:build linux && amd64 && cgo

package lint

/*
#cgo LDFLAGS: -L${SRCDIR}/native/linux_amd64
*/
import "C"
