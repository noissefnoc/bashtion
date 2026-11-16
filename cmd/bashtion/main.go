//go:build unix

package main

import (
	"github.com/noissefnoc/bashtion/internal/bashtion"
	"github.com/noissefnoc/bashtion/internal/cli"
)

func main() {
	cli.Run(bashtion.MainCommandByArgs)
}
