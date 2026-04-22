package main

import (
	"github.com/wesm/fotobank/tools/testifyhelpercheck"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(testifyhelpercheck.Analyzer)
}
