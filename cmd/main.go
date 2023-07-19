package main

import (
	"containerd-hvf/pkg/hvf"
	"github.com/containerd/containerd/runtime/v2/shim"
)

func main() {
	shim.Run("io.containerd.hvf.v2", hvf.Init)
}
