// A deliberately broken signed release for the Linux update E2E test.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

var version string

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		os.Exit(2)
	}
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	directory := flags.String("data-dir", "", "disposable test store")
	flags.String("listen", "", "unused")
	flags.String("public-url", "", "unused")
	_ = flags.Parse(os.Args[2:])
	if *directory == "" {
		fmt.Fprintln(os.Stderr, "a disposable test data directory is required")
		os.Exit(2)
	}
	store, err := storage.Open(context.Background(), *directory)
	must(err)
	var marker string
	must(store.SystemDB().QueryRow(`SELECT value FROM gateway_metadata WHERE key='cli-e2e'`).Scan(&marker))
	if marker != "preserved" {
		fmt.Fprintln(os.Stderr, "refusing to modify a store without the test marker")
		os.Exit(2)
	}
	_, err = store.SystemDB().Exec(`UPDATE gateway_metadata SET value='broken-release' WHERE key='cli-e2e'`)
	must(err)
	_, err = store.DataDB().Exec(`UPDATE projection_metadata SET value='broken-release' WHERE key='cli-e2e'`)
	must(err)
	must(store.Close())
	must(os.WriteFile(filepath.Join(*directory, "master.key"), bytes.Repeat([]byte{42}, 32), 0o600))
	fmt.Fprintln(os.Stderr, "forced readiness failure")
	os.Exit(1)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
