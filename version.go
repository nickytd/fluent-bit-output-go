// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package main

import "runtime/debug"

func buildInfo() (version, commit string) {
	version, commit = "dev", "unknown"
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = info.Main.Version
	}
	var rev, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
			if len(rev) > 10 {
				rev = rev[:10]
			}
		case "vcs.modified":
			if s.Value == "true" {
				modified = "+dirty"
			}
		}
	}
	if rev != "" {
		commit = rev + modified
	}
	return
}
