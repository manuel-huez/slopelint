package main

import (
	"example.com/slopelint"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(slopelint.Analyzer)
}
