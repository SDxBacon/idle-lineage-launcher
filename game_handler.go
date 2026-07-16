package main

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func gameAssetMiddleware(manager *gameManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/game" && !strings.HasPrefix(request.URL.Path, "/game/") {
				next.ServeHTTP(response, request)
				return
			}
			serveGameAsset(manager, response, request)
		})
	}
}

func serveGameAsset(manager *gameManager, response http.ResponseWriter, request *http.Request) {
	setNoStoreHeaders(response)
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		slog.Warn("game asset request rejected", "reason", "method", "method", request.Method, "path", request.URL.Path)
		response.Header().Set("Allow", "GET, HEAD")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	root, _, ready := manager.ActiveVersion()
	if !ready {
		slog.Warn("game asset request rejected", "reason", "not_installed", "path", request.URL.Path)
		http.Error(response, "game is not installed", http.StatusServiceUnavailable)
		return
	}
	relative := strings.TrimPrefix(request.URL.Path, "/game")
	relative = strings.TrimPrefix(relative, "/")
	if relative == "" {
		relative = "index.html"
	}
	if !safeRequestPath(relative) {
		slog.Warn("game asset request rejected", "reason", "unsafe_path", "path", request.URL.Path)
		http.Error(response, "invalid game path", http.StatusBadRequest)
		return
	}

	target := filepath.Join(root, filepath.FromSlash(relative))
	if !pathInside(root, target) {
		http.Error(response, "invalid game path", http.StatusBadRequest)
		return
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	targetResolved, err := filepath.EvalSymlinks(target)
	if err != nil || !pathInside(rootResolved, targetResolved) {
		http.NotFound(response, request)
		return
	}
	info, err := os.Stat(targetResolved)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	if info.IsDir() {
		targetResolved = filepath.Join(targetResolved, "index.html")
		info, err = os.Stat(targetResolved)
	}
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(response, request)
		return
	}
	file, err := os.Open(targetResolved)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	defer file.Close()
	http.ServeContent(response, request, info.Name(), info.ModTime(), file)
}

func setNoStoreHeaders(response http.ResponseWriter) {
	response.Header().Set("Cache-Control", "no-store, max-age=0")
	response.Header().Set("Pragma", "no-cache")
	response.Header().Set("Expires", "0")
	response.Header().Set("X-Content-Type-Options", "nosniff")
}

func safeRequestPath(relative string) bool {
	if relative == "" || strings.ContainsRune(relative, 0) || strings.Contains(relative, "\\") || strings.HasPrefix(relative, "/") {
		return false
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

func pathInside(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
