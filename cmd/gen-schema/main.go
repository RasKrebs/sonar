// Command gen-schema writes the daemon protocol JSON Schema, generated from
// the Go types in internal/state and internal/daemon/rpc, to disk. It is driven
// by a //go:generate directive in internal/daemon/rpc/schema.go; CI fails when
// the checked-in file differs from what this command produces.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

func main() {
	out := flag.String("o", filepath.Join("docs", "schema", "protocol.schema.json"),
		"path of the schema file to write")
	flag.Parse()

	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "gen-schema:", err)
		os.Exit(1)
	}
}

func run(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, rpc.Marshal(), 0o644)
}
