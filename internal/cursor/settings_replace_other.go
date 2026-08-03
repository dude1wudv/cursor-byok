//go:build !windows

package cursor

import "os"

func replaceCursorSettingsFile(source string, target string) error { return os.Rename(source, target) }
