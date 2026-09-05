package store_test

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/raskrebs/sonar/internal/state"
	"github.com/raskrebs/sonar/internal/store"
)

// A rename recorded against a port's cwd: match key is still there after the
// daemon restarts, which is what `sonar rename 3000 storefront` followed by
// `sonar daemon restart` relies on.
func Example_renameSurvivesRestart() {
	dir, err := os.MkdirTemp("", "sonar-store-example")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	dbPath := filepath.Join(dir, "sonar.db")

	// The port as the scanner sees it: a node process in /code/shop on 3000.
	root := filepath.FromSlash("/code/shop")
	p := state.Port{
		Port:        3000,
		BindAddress: "127.0.0.1",
		PID:         48211,
		Process:     "node",
		Cwd:         filepath.Join(root, "api"),
		ProjectRoot: &root,
	}

	// First daemon run: record the rename under the port's primary match key.
	first, err := store.Open(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	key := store.PrimaryKey(p)
	if err := first.SetRename(key, "storefront"); err != nil {
		log.Fatal(err)
	}
	if err := first.Close(); err != nil {
		log.Fatal(err)
	}

	// Restart: same project, new PID. Resolve finds the rename again.
	second, err := store.Open(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = second.Close() }()

	p.PID = 51002
	name, ok, err := second.ResolveRename(p)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("key: ", filepath.ToSlash(key))
	fmt.Println("name:", name, ok)
	fmt.Println("reset happened:", second.ResetHappened())

	// Output:
	// key:  cwd:/code/shop:3000
	// name: storefront true
	// reset happened: false
}
