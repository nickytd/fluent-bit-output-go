// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package main

import "runtime/debug"

// version and commit are overridable at build time via:
//
//	-ldflags "-X main.version=<v> -X main.commit=<sha>"
//
// Left at their defaults (a plain `go build`/`go install`), buildInfo() falls
// back to runtime/debug.ReadBuildInfo() VCS stamping.
var (
	version = "dev"
	commit  = "unknown"
)

func buildInfo() (v, c string) {
	v, c = version, commit
	if v != "dev" && c != "unknown" {
		return v, c // ldflags injected real values — they win.
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return v, c
	}

	if v == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		v = info.Main.Version
	}

	if c == "unknown" {
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
			c = rev + modified
		}
	}

	return v, c
}
