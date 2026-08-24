package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/neutree-ai/neutree/pkg/releaseprofile"
)

func main() {
	output := flag.String("output", "", "generated artifact output directory")
	flag.Parse()

	if err := run(*output); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(output string) error {
	if output == "" {
		return fmt.Errorf("generated artifact output directory is required")
	}

	return releaseprofile.WritePackageArtifacts(output, releaseprofile.NewBuilder())
}
