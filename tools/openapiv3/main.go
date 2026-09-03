package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	sourcePath := flag.String("source", openAPIV2DefaultPath, "path to the finalized Swagger 2 document")
	outputPath := flag.String("output", openAPIV3DefaultPath, "path for the generated OpenAPI 3 document")
	flag.Parse()

	summary, sourceHash, err := run(*sourcePath, *outputPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf(
		"Generated OpenAPI %s: %d paths, %d operations, %d schemas, %d examples, %d internal refs\n",
		openAPIV3TargetVersion,
		summary.paths,
		summary.operations,
		summary.schemas,
		summary.examples,
		summary.refs,
	)
	fmt.Printf("OpenAPI v2 SHA-256 unchanged: %s\n", sourceHash)
}
