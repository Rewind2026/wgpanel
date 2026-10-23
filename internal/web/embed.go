package web

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// distFS 承载构建期由 `npm run build` 产出的前端资源。
//
// 目录中始终保留一个占位文件，这样即使全新克隆、尚未构建前端，
// go:embed 的编译期约束也能满足（embed 不允许匹配到空目录）。
//
//go:embed all:dist
var distFS embed.FS

// FS 返回可用的前端文件系统。
// 优先使用外部目录（开发模式），否则回退到嵌入产物。
func FS(externalDir string) (fs.FS, error) {
	if externalDir != "" {
		if st, err := os.Stat(externalDir); err == nil && st.IsDir() {
			if _, err := fs.Stat(os.DirFS(externalDir), "index.html"); err == nil {
				return os.DirFS(externalDir), nil
			}
			return nil, fmt.Errorf("web: %s has no index.html", externalDir)
		}
		return nil, fmt.Errorf("web: %s is not a directory", externalDir)
	}

	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, fmt.Errorf("web: open embedded dist: %w", err)
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("web: embedded assets are empty; run `pnpm build` in web/ first")
		}
		return nil, fmt.Errorf("web: stat embedded index.html: %w", err)
	}
	return sub, nil
}

// Embedded 报告编译期内是否包含前端产物。
func Embedded() bool {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return false
	}
	_, err = fs.Stat(sub, "index.html")
	return err == nil
}
