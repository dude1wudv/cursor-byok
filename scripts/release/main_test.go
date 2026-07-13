package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewUpdateManifestForThreePlatforms(t *testing.T) {
	t.Parallel()

	assets, err := releaseAssetsFor("macos-arm64,macos-amd64,windows-amd64")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, asset := range assets {
		filename := "cursor-byok-0.0.46-" + asset.platform + asset.suffix
		if err := os.WriteFile(filepath.Join(dir, filename), []byte(asset.platform), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	manifest, err := newUpdateManifest("0.0.46", "notes", dir, "owner/repo", "cursor-byok", assets, time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Platforms) != 3 {
		t.Fatalf("platform count = %d, want 3", len(manifest.Platforms))
	}
	if _, ok := manifest.Platforms["linux-amd64"]; ok {
		t.Fatal("manifest unexpectedly includes linux-amd64")
	}
}

func TestVerifyReleaseAssetsFailsForMissingAsset(t *testing.T) {
	t.Parallel()

	assets, err := releaseAssetsFor("windows-amd64,macos-arm64")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cursor-byok-0.0.46-windows-amd64.zip"), []byte("windows"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = verifyReleaseAssets(dir, "cursor-byok", "0.0.46", assets)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want missing asset error", err)
	}
}

func TestReleaseAssetsForDefaultIncludesFourPlatforms(t *testing.T) {
	t.Parallel()

	assets, err := releaseAssetsFor(defaultReleasePlatforms)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 4 {
		t.Fatalf("platform count = %d, want 4", len(assets))
	}
	if assets[3].platform != "linux-amd64" {
		t.Fatalf("last platform = %q, want linux-amd64", assets[3].platform)
	}
}
