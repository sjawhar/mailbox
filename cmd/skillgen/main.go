package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sjawhar/mailbox/internal/cli"
)

func main() {
	out := flag.String("out", "", "output path")
	flag.Parse()
	if *out == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: skillgen -out PATH")
		os.Exit(2)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	file, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := cli.WriteAgentSkill(file); err != nil {
		file.Close()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := file.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
