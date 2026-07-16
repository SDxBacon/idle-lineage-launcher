package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestGameAssetHandlerRequiresReadyVersion(t *testing.T) {
	manager, err := newGameManager(makeDataPaths(t.TempDir()), nil)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	serveGameAsset(manager, response, httptest.NewRequest(http.MethodGet, "/game/index.html", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
}

func TestGameAssetHandlerRejectsTraversalAndDirectoryListing(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	repository := newLocalGameRepository(t, paths.Source)
	if err := os.WriteFile(filepath.Join(paths.Source, "assets", "visible.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path string
		want int
	}{
		{path: "/game/index.html?version=" + repository.head(t), want: http.StatusOK},
		{path: "/game/../active.json", want: http.StatusBadRequest},
		{path: "/game/assets", want: http.StatusNotFound},
		{path: "/game/assets/visible.txt", want: http.StatusOK},
		{path: "/game/.git/config", want: http.StatusBadRequest},
		{path: "/game/.GIT/config", want: http.StatusBadRequest},
		{path: "/game/missing.txt", want: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://launcher.local"+test.path, nil)
			response := httptest.NewRecorder()
			serveGameAsset(manager, response, request)
			if response.Code != test.want {
				t.Fatalf("expected %d, got %d", test.want, response.Code)
			}
		})
	}
}

func TestGameMiddlewarePassesFrontendRequestsThrough(t *testing.T) {
	manager, err := newGameManager(makeDataPaths(t.TempDir()), nil)
	if err != nil {
		t.Fatal(err)
	}
	middleware := gameAssetMiddleware(manager)
	handler := middleware(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusTeapot)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusTeapot {
		t.Fatalf("frontend handler was not called: %d", response.Code)
	}
}
