// Package main implements a tiny copy tool intended to run as an initContainer
// entrypoint. It copies /plugin/go-out.so (baked into the plugin image) into a
// user-supplied destination directory that is expected to be a shared volume
// mounted into both this container and the fluent-bit container.
//
// The scratch base image has no shell and no /bin/cp, so we ship this static
// binary instead. It intentionally does one thing.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

func main() {
	src := flag.String("src", "/plugin/go-out.so", "path to the plugin shared library baked into this image")
	dst := flag.String("dst", "/output", "destination directory on the shared volume; must exist and be writable")
	perm := flag.Uint("perm", 0o755, "octal file mode for the copied plugin")
	flag.Parse()

	if err := run(*src, *dst, os.FileMode(*perm)); err != nil {
		log.Fatalf("copy-plugin: %v", err)
	}
}

func run(src, dst string, perm os.FileMode) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat src %s: %w", src, err)
	}
	if info.IsDir() {
		return fmt.Errorf("src %s is a directory, expected a file", src)
	}

	dstInfo, err := os.Stat(dst)
	if err != nil {
		return fmt.Errorf("stat dst %s: %w", dst, err)
	}
	if !dstInfo.IsDir() {
		return fmt.Errorf("dst %s is not a directory", dst)
	}

	dstPath := filepath.Join(dst, filepath.Base(src))
	if err := copyFile(src, dstPath, perm); err != nil {
		return fmt.Errorf("copy %s -> %s: %w", src, dstPath, err)
	}
	log.Printf("copied %s -> %s (%d bytes, mode %o)", src, dstPath, info.Size(), perm)
	return nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	// Write to a sibling temp file and rename so a partial copy is never
	// observable by the fluent-bit container waiting on the shared volume.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".copy-plugin-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we fail before rename.
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}
