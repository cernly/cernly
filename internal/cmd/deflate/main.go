// Command deflate writes a file as raw DEFLATE (RFC 1951) at the best level,
// for the local viewer's build: compress/flate gives the same bytes for the
// same input and Go release, whatever the machine, so the page's published
// hash can be reproduced. The page inflates with DecompressionStream.
//
//	go run ./internal/cmd/deflate <in> <out>
package main

import (
	"compress/flate"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: deflate <in> <out>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "deflate:", err)
		os.Exit(1)
	}
}

func run(in, out string) error {
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	w, err := flate.NewWriter(f, flate.BestCompression)
	if err != nil {
		f.Close()
		return err
	}
	if _, err := w.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := w.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
