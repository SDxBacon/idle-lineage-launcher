package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

type archiveEntry struct {
	name     string
	body     string
	typeflag byte
	size     int64
}

func TestExistingVersionStartsReadyWithoutNetwork(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	writeValidGame(t, paths.Source)
	writeManifest(t, paths, testCommit)

	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := manager.State()
	if state.Status != StatusReady || state.Commit != testCommit {
		t.Fatalf("unexpected state: %+v", state)
	}
	root, commit, ready := manager.ActiveVersion()
	if !ready || root != paths.Source || commit != testCommit {
		t.Fatalf("unexpected active version: %q %q %v", root, commit, ready)
	}
}

func TestExistingLegacyVersionMigratesToSingleSource(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	legacyRoot := filepath.Join(paths.Game, "versions", testCommit)
	writeValidGame(t, legacyRoot)
	writeValidGame(t, filepath.Join(paths.Game, "versions", strings.Repeat("a", 40)))
	writeManifest(t, paths, testCommit)

	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	root, _, ready := manager.ActiveVersion()
	if !ready || root != paths.Source {
		t.Fatalf("legacy game was not migrated: %q %v", root, ready)
	}
	if err := validateGameRoot(paths.Source); err != nil {
		t.Fatalf("migrated source is invalid: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.Game, "versions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy versions directory still exists: %v", err)
	}
}

func TestMissingManifestRemovesOrphanedLegacyVersions(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	writeValidGame(t, filepath.Join(paths.Game, "versions", testCommit))

	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manager.State().Status != StatusMissing {
		t.Fatalf("unexpected state: %+v", manager.State())
	}
	if _, err := os.Stat(filepath.Join(paths.Game, "versions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned versions directory still exists: %v", err)
	}
}

func TestInstallFromGitHubEndpointsAndServeAssets(t *testing.T) {
	archive := makeArchive(t, validArchiveEntries())
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/commit":
			json.NewEncoder(response).Encode(map[string]string{"sha": testCommit})
		case "/archive/" + testCommit:
			response.Header().Set("Content-Length", "1234")
			response.Write(archive)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	var mu sync.Mutex
	var events []GameState
	manager := testManager(t, server.URL, func(state GameState) {
		mu.Lock()
		events = append(events, state)
		mu.Unlock()
	})
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)

	state := manager.State()
	if state.Commit != testCommit || state.FilesExtracted < 4 || state.ReceivedBytes == 0 {
		t.Fatalf("unexpected ready state: %+v", state)
	}
	manifestContents, err := os.ReadFile(manager.paths.Active)
	if err != nil || !bytes.Contains(manifestContents, []byte(testCommit)) {
		t.Fatalf("active manifest was not switched: %s (%v)", manifestContents, err)
	}
	if err := validateGameRoot(manager.paths.Source); err != nil {
		t.Fatalf("game was not installed in src: %v", err)
	}
	if _, err := os.Stat(filepath.Join(manager.paths.Game, "versions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected versions directory: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !containsStatus(events, StatusInstalling) || !containsStatus(events, StatusReady) {
		t.Fatalf("missing progress events: %+v", events)
	}

	request := httptest.NewRequest(http.MethodGet, "/game/js/app.js", nil)
	response := httptest.NewRecorder()
	serveGameAsset(manager, response, request)
	if response.Code != http.StatusOK || response.Body.String() != "console.log('ready')" {
		t.Fatalf("unexpected asset response: %d %q", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store, max-age=0" {
		t.Fatalf("missing no-store header: %q", response.Header().Get("Cache-Control"))
	}
}

func TestConcurrentInstallRequestsUseOneJob(t *testing.T) {
	archive := makeArchive(t, validArchiveEntries())
	var commitRequests, archiveRequests atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/commit":
			commitRequests.Add(1)
			json.NewEncoder(response).Encode(map[string]string{"sha": testCommit})
		case "/archive/" + testCommit:
			archiveRequests.Add(1)
			<-release
			response.Write(archive)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	manager := testManager(t, server.URL, nil)
	errorsFound := make(chan error, 24)
	var starters sync.WaitGroup
	for range 24 {
		starters.Add(1)
		go func() {
			defer starters.Done()
			errorsFound <- manager.StartInstall()
		}()
	}
	starters.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	waitForCondition(t, func() bool { return archiveRequests.Load() == 1 }, "archive request")
	close(release)
	waitForStatus(t, manager, StatusReady)
	if commitRequests.Load() != 1 || archiveRequests.Load() != 1 {
		t.Fatalf("expected one job, got commit=%d archive=%d", commitRequests.Load(), archiveRequests.Load())
	}
}

func TestCancelCleansStagingAndCanRetry(t *testing.T) {
	archive := makeArchive(t, validArchiveEntries())
	var archiveRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/commit" {
			json.NewEncoder(response).Encode(map[string]string{"sha": testCommit})
			return
		}
		if request.URL.Path != "/archive/"+testCommit {
			http.NotFound(response, request)
			return
		}
		attempt := archiveRequests.Add(1)
		if attempt == 1 {
			response.Header().Set("Content-Type", "application/gzip")
			response.Write(archive[:min(20, len(archive))])
			if flusher, ok := response.(http.Flusher); ok {
				flusher.Flush()
			}
			<-request.Context().Done()
			return
		}
		response.Write(archive)
	}))
	defer server.Close()

	manager := testManager(t, server.URL, nil)
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusInstalling)
	manager.CancelInstall()
	waitForStatus(t, manager, StatusCancelled)
	assertDirectoryEmpty(t, manager.paths.Staging)

	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
}

func TestInstallFailuresAreRecoverable(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		archive    []byte
	}{
		{name: "HTTP error", statusCode: http.StatusBadGateway},
		{name: "corrupt archive", statusCode: http.StatusOK, archive: []byte("not gzip")},
		{name: "truncated archive", statusCode: http.StatusOK, archive: makeArchive(t, validArchiveEntries())[:18]},
		{name: "missing index", statusCode: http.StatusOK, archive: makeArchive(t, []archiveEntry{
			{name: "repo/assets/a.png", body: "a"},
			{name: "repo/css/a.css", body: "a"},
			{name: "repo/js/a.js", body: "a"},
		})},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/commit" {
					json.NewEncoder(response).Encode(map[string]string{"sha": testCommit})
					return
				}
				response.WriteHeader(test.statusCode)
				response.Write(test.archive)
			}))
			defer server.Close()
			manager := testManager(t, server.URL, nil)
			if err := manager.StartInstall(); err != nil {
				t.Fatal(err)
			}
			waitForStatus(t, manager, StatusError)
			if manager.State().Error == "" {
				t.Fatal("expected a useful error")
			}
			assertDirectoryEmpty(t, manager.paths.Staging)
		})
	}
}

func TestInitialiseRemovesStaleStaging(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	stale := filepath.Join(paths.Staging, "interrupted", "file")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manager.State().Status != StatusMissing {
		t.Fatalf("unexpected state: %+v", manager.State())
	}
	assertDirectoryEmpty(t, paths.Staging)
}

func TestSecureArchiveExtraction(t *testing.T) {
	tests := []struct {
		name    string
		entries []archiveEntry
	}{
		{name: "path traversal", entries: []archiveEntry{{name: "repo/../escape", body: "x"}}},
		{name: "absolute path", entries: []archiveEntry{{name: "/escape", body: "x"}}},
		{name: "backslash path", entries: []archiveEntry{{name: `repo\escape`, body: "x"}}},
		{name: "symlink", entries: []archiveEntry{{name: "repo/link", typeflag: tar.TypeSymlink}}},
		{name: "hard link", entries: []archiveEntry{{name: "repo/link", typeflag: tar.TypeLink}}},
		{name: "special file", entries: []archiveEntry{{name: "repo/fifo", typeflag: tar.TypeFifo}}},
		{name: "archive bomb", entries: []archiveEntry{{name: "repo/huge", typeflag: tar.TypeReg, size: maxSingleFile + 1}}},
		{name: "multiple roots", entries: []archiveEntry{{name: "one/a", body: "a"}, {name: "two/b", body: "b"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := makeArchive(t, test.entries)
			err := extractTarGz(context.Background(), bytes.NewReader(archive), t.TempDir(), nil)
			if err == nil {
				t.Fatal("expected extraction to reject archive")
			}
		})
	}
}

func TestExtractHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := extractTarGz(ctx, bytes.NewReader(makeArchive(t, validArchiveEntries())), t.TempDir(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestProgressReaderEnforcesCompressedLimit(t *testing.T) {
	reader := &progressReader{reader: strings.NewReader("12345"), limit: 4, update: func(int64) {}}
	_, err := io.ReadAll(reader)
	if !errors.Is(err, errDownloadTooLarge) {
		t.Fatalf("expected compressed size limit, got %v", err)
	}
}

func testManager(t *testing.T, serverURL string, emit stateEmitter) *gameManager {
	t.Helper()
	manager, err := newGameManager(makeDataPaths(t.TempDir()), emit)
	if err != nil {
		t.Fatal(err)
	}
	manager.client = &http.Client{Timeout: 5 * time.Second}
	manager.apiURL = serverURL + "/commit"
	manager.archiveURL = serverURL + "/archive/"
	return manager
}

func validArchiveEntries() []archiveEntry {
	return []archiveEntry{
		{name: "pax_global_header", typeflag: tar.TypeXGlobalHeader},
		{name: "repo/index.html", body: "<!doctype html><title>game</title>"},
		{name: "repo/assets/image.png", body: "image"},
		{name: "repo/css/app.css", body: "body{}"},
		{name: "repo/js/app.js", body: "console.log('ready')"},
	}
}

func makeArchive(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		size := entry.size
		if size == 0 && (typeflag == tar.TypeReg || typeflag == tar.TypeRegA) {
			size = int64(len(entry.body))
		}
		header := &tar.Header{Name: entry.name, Mode: 0o644, Size: size, Typeflag: typeflag}
		if typeflag == tar.TypeXGlobalHeader {
			header = &tar.Header{
				Name:       entry.name,
				Typeflag:   typeflag,
				PAXRecords: map[string]string{"comment": "test global metadata"},
			}
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		// For security-limit tests we only need the oversized header. Closing
		// the gzip stream directly preserves it without allocating the payload.
		if size > int64(len(entry.body)) {
			if err := gzipWriter.Close(); err != nil {
				t.Fatal(err)
			}
			return buffer.Bytes()
		}
		if entry.body != "" {
			if _, err := io.Copy(tarWriter, strings.NewReader(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func writeValidGame(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"assets", "css", "js"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("game"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeManifest(t *testing.T, paths dataPaths, commit string) {
	t.Helper()
	contents, err := json.Marshal(activeManifest{SchemaVersion: 1, Repository: gameRepository, Commit: commit, InstalledAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Active), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Active, contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitForStatus(t *testing.T, manager *gameManager, status GameStatus) {
	t.Helper()
	waitForCondition(t, func() bool { return manager.State().Status == status }, string(status))
}

func waitForCondition(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func assertDirectoryEmpty(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected %s to be empty, found %v", directory, entries)
	}
}

func containsStatus(states []GameState, status GameStatus) bool {
	for _, state := range states {
		if state.Status == status {
			return true
		}
	}
	return false
}
