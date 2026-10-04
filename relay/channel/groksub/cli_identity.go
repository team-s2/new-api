package groksub

import "runtime"

// CLIUserAgent mirrors the official Grok CLI User-Agent format:
// grok-pager/{version} grok-shell/{version} ({platform}; {arch})
func CLIUserAgent() string {
	platform := "linux"
	switch runtime.GOOS {
	case "darwin":
		platform = "macos"
	case "windows":
		platform = "windows"
	}
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	case "386":
		arch = "x86"
	}
	return "grok-pager/" + cliClientVersion + " grok-shell/" + cliClientVersion + " (" + platform + "; " + arch + ")"
}
