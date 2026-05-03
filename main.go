package main

import (
	"github.com/penny-vault/leveraged-trend-ensemble/lte"
	"github.com/penny-vault/pvbt/cli"
)

func main() {
	cli.Run(&lte.LeveragedTrendEnsemble{})
}
