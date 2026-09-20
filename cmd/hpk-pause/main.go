package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"hpk/pkg/version"
)

func main() {
	versionFlag := flag.Bool("version", false, "Print version and exit")
	// Accept legacy flags if passed to avoid errors
	flag.String("pod", "", "legacy pod flag")
	flag.String("namespace", "", "legacy namespace flag")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("hpk-pause version: %s (built: %s)\n", version.Version, version.BuildTime)
		os.Exit(0)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
}
