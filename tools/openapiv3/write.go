package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func writeAtomically(filename string, contents []byte) error {
	// Same-directory rename prevents readers from observing a partially written generated artifact.
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return fmt.Errorf("create OpenAPI v3 directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(filename), "."+filepath.Base(filename)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary OpenAPI v3 document: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)

	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return fmt.Errorf("set temporary OpenAPI v3 permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary OpenAPI v3 document: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary OpenAPI v3 document: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary OpenAPI v3 document: %w", err)
	}
	if err := os.Rename(temporaryName, filename); err != nil {
		return fmt.Errorf("replace OpenAPI v3 document: %w", err)
	}
	return nil
}
