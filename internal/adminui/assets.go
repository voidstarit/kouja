package adminui

import (
	"embed"
	"io/fs"
)

//go:embed all:assets
var embeddedAssets embed.FS

//go:embed fallback.html
var fallbackHTML []byte

type assetFS struct {
	root fs.FS
}

func defaultAssets() *assetFS {
	root, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		return &assetFS{root: embeddedAssets}
	}
	return &assetFS{root: root}
}

func newAssetFS(root fs.FS) *assetFS {
	return &assetFS{root: root}
}

func (a *assetFS) read(name string) ([]byte, error) {
	if a == nil || a.root == nil {
		return nil, fs.ErrNotExist
	}
	return fs.ReadFile(a.root, name)
}

func (a *assetFS) readAsset(name string) ([]byte, error) {
	if a == nil || a.root == nil {
		return nil, fs.ErrNotExist
	}
	if data, err := fs.ReadFile(a.root, "assets/"+name); err == nil {
		return data, nil
	}
	return fs.ReadFile(a.root, name)
}

func (a *assetFS) readFallback() ([]byte, error) {
	if len(fallbackHTML) == 0 {
		return nil, fs.ErrNotExist
	}
	return fallbackHTML, nil
}
