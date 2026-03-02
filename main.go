package main

import "github.com/wirya/greenlight/cmd"

// version is injected at build time via ldflags:
//
//	go build -ldflags "-X main.version=v1.0.0" .
var version = "dev"

func main() {
	cmd.SetVersion(version)
	cmd.Execute()
}
