package main

import (
	"example.com/defenselint"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(defenselint.Analyzer)
}
