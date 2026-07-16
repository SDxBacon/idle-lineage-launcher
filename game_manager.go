package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	gameRepository = "shines871/idle-lineage-class"
	defaultAPIURL  = "https://api.github.com/repos/" + gameRepository + "/commits/main"
	defaultLoadURL = "https://codeload.github.com/" + gameRepository + "/tar.gz/"

	maxArchiveFiles = 500_000
	maxArchiveBytes = int64(2 << 30)
	maxSingleFile   = int64(512 << 20)
	maxDownloadSize = int64(1 << 30)
)

var errDownloadTooLarge = errors.New("archive exceeds the compressed download size limit")

type GameStatus string

const (
	StatusMissing    GameStatus = "missing"
	StatusResolving  GameStatus = "resolving"
	StatusInstalling GameStatus = "installing"
	StatusReady      GameStatus = "ready"
	StatusCancelled  GameStatus = "cancelled"
	StatusError      GameStatus = "error"
)

type GameState struct {
	Status         GameStatus `json:"status"`
	Commit         string     `json:"commit"`
	ReceivedBytes  int64      `json:"receivedBytes"`
	TotalBytes     int64      `json:"totalBytes"`
	FilesExtracted int64      `json:"filesExtracted"`
	Message        string     `json:"message"`
	Error          string     `json:"error"`
}

type activeManifest struct {
	SchemaVersion int       `json:"schemaVersion"`
	Repository    string    `json:"repository"`
	Commit        string    `json:"commit"`
	InstalledAt   time.Time `json:"installedAt"`
}

type stateEmitter func(GameState)

type gameManager struct {
	mu sync.RWMutex

	paths      dataPaths
	client     *http.Client
	apiURL     string
	archiveURL string
	emit       stateEmitter
	state      GameState
	activeRoot string
	cancel     context.CancelFunc
	running    bool
	wg         sync.WaitGroup
	lastUpdate time.Time
}

func newGameManager(paths dataPaths, emit stateEmitter) (*gameManager, error) {
	m := &gameManager{
		paths:      paths,
		client:     defaultHTTPClient(),
		apiURL:     defaultAPIURL,
		archiveURL: defaultLoadURL,
		emit:       emit,
		state: GameState{
			Status:  StatusMissing,
			Message: "尚未安裝遊戲內容",
		},
	}
	if err := m.initialise(); err != nil {
		m.state = GameState{Status: StatusError, Message: "無法載入既有安裝", Error: err.Error()}
	}
	return m, nil
}

func defaultHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 15 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: transport}
}

func (m *gameManager) initialise() error {
	for _, dir := range []string{m.paths.Game, m.paths.Staging} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create data directory: %w", err)
		}
	}
	entries, err := os.ReadDir(m.paths.Staging)
	if err != nil {
		return fmt.Errorf("read staging directory: %w", err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(m.paths.Staging, entry.Name())); err != nil {
			return fmt.Errorf("remove stale staging data: %w", err)
		}
	}

	manifestBytes, err := os.ReadFile(m.paths.Active)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.RemoveAll(filepath.Join(m.paths.Game, "versions")); err != nil {
			return fmt.Errorf("remove orphaned legacy game versions: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read active version: %w", err)
	}
	var manifest activeManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("decode active version: %w", err)
	}
	if manifest.SchemaVersion != 1 || manifest.Repository != gameRepository || !validCommit(manifest.Commit) {
		return errors.New("active version manifest is invalid")
	}
	if err := m.migrateLegacyVersion(manifest.Commit); err != nil {
		return err
	}
	if err := validateGameRoot(m.paths.Source); err != nil {
		return fmt.Errorf("validate active version: %w", err)
	}
	m.activeRoot = m.paths.Source
	m.state = GameState{Status: StatusReady, Commit: manifest.Commit, Message: "遊戲已可離線使用"}
	return nil
}

func (m *gameManager) migrateLegacyVersion(commit string) error {
	legacyVersions := filepath.Join(m.paths.Game, "versions")
	if validateGameRoot(m.paths.Source) != nil {
		legacyRoot := filepath.Join(legacyVersions, commit)
		if err := validateGameRoot(legacyRoot); err == nil {
			if err := os.Rename(legacyRoot, m.paths.Source); err != nil {
				return fmt.Errorf("migrate active game to src: %w", err)
			}
		}
	}
	if err := os.RemoveAll(legacyVersions); err != nil {
		return fmt.Errorf("remove legacy game versions: %w", err)
	}
	return nil
}

func (m *gameManager) State() GameState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

func (m *gameManager) ActiveVersion() (root, commit string, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.state.Status != StatusReady || m.activeRoot == "" {
		return "", "", false
	}
	return m.activeRoot, m.state.Commit, true
}

func (m *gameManager) StartInstall() error {
	m.mu.Lock()
	if m.running || m.state.Status == StatusReady {
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.running = true
	m.lastUpdate = time.Time{}
	m.state = GameState{Status: StatusResolving, Message: "正在取得官方 main 版本…"}
	state := m.state
	m.wg.Add(1)
	m.mu.Unlock()
	m.publish(state)

	go func() {
		defer m.wg.Done()
		err := m.install(ctx)
		m.finishInstall(err)
	}()
	return nil
}

func (m *gameManager) CancelInstall() {
	m.mu.RLock()
	cancel := m.cancel
	m.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (m *gameManager) Shutdown() {
	m.CancelInstall()
	m.wg.Wait()
}

func (m *gameManager) finishInstall(err error) {
	m.mu.Lock()
	m.running = false
	m.cancel = nil
	if err != nil {
		if errors.Is(err, context.Canceled) {
			m.state.Status = StatusCancelled
			m.state.Message = "已取消安裝；可隨時重新開始"
			m.state.Error = ""
		} else {
			m.state.Status = StatusError
			m.state.Message = "安裝失敗"
			m.state.Error = err.Error()
		}
	}
	state := m.state
	m.mu.Unlock()
	m.publish(state)
}

func (m *gameManager) install(ctx context.Context) error {
	sha, err := m.resolveCommit(ctx)
	if err != nil {
		return err
	}

	staging, err := os.MkdirTemp(m.paths.Staging, sha+"-")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.archiveURL+sha, nil)
	if err != nil {
		return fmt.Errorf("create archive request: %w", err)
	}
	request.Header.Set("User-Agent", "IdleLineageLauncher/0.1.0")
	response, err := m.client.Do(request)
	if err != nil {
		return fmt.Errorf("download game archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download game archive: GitHub returned %s", response.Status)
	}
	if response.ContentLength > maxDownloadSize {
		return fmt.Errorf("download game archive: %w", errDownloadTooLarge)
	}

	total := response.ContentLength
	if total < 0 {
		total = 0
	}
	m.updateState(func(state *GameState) {
		state.Status = StatusInstalling
		state.Commit = sha
		state.TotalBytes = total
		state.Message = "正在下載並安全解壓遊戲內容…"
		state.Error = ""
	}, true)

	reader := &progressReader{reader: response.Body, limit: maxDownloadSize, update: func(received int64) {
		m.updateProgress(received, -1)
	}}
	if err := extractTarGz(ctx, reader, staging, func(files int64) {
		m.updateProgress(reader.received, files)
	}); err != nil {
		return fmt.Errorf("extract game archive: %w", err)
	}
	if err := validateGameRoot(staging); err != nil {
		return fmt.Errorf("validate downloaded game: %w", err)
	}

	if err := m.replaceSource(staging); err != nil {
		return err
	}
	return m.activate(sha, m.paths.Source)
}

func (m *gameManager) replaceSource(staging string) error {
	backup := filepath.Join(m.paths.Staging, ".previous-src")
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("clean previous game backup: %w", err)
	}

	hadSource := validateGameRoot(m.paths.Source) == nil
	if hadSource {
		if err := os.Rename(m.paths.Source, backup); err != nil {
			return fmt.Errorf("prepare current game replacement: %w", err)
		}
	}
	if err := os.Rename(staging, m.paths.Source); err != nil {
		if hadSource {
			_ = os.Rename(backup, m.paths.Source)
		}
		return fmt.Errorf("install game source: %w", err)
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("remove previous game source: %w", err)
	}
	return nil
}

func (m *gameManager) resolveCommit(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("create GitHub API request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "IdleLineageLauncher/0.1.0")
	response, err := m.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("resolve main commit: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("resolve main commit: GitHub returned %s", response.Status)
	}
	var payload struct {
		SHA string `json:"sha"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&payload); err != nil {
		return "", fmt.Errorf("decode GitHub response: %w", err)
	}
	if !validCommit(payload.SHA) {
		return "", errors.New("resolve main commit: GitHub returned an invalid commit SHA")
	}
	return strings.ToLower(payload.SHA), nil
}

func (m *gameManager) activate(sha, root string) error {
	manifest := activeManifest{
		SchemaVersion: 1,
		Repository:    gameRepository,
		Commit:        sha,
		InstalledAt:   time.Now().UTC(),
	}
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode active version: %w", err)
	}
	if err := atomicWriteFile(m.paths.Active, append(contents, '\n'), 0o644); err != nil {
		return fmt.Errorf("switch active game version: %w", err)
	}
	m.mu.Lock()
	m.activeRoot = root
	m.state.Status = StatusReady
	m.state.Commit = sha
	m.state.Message = "安裝完成，可離線啟動"
	m.state.Error = ""
	state := m.state
	m.mu.Unlock()
	m.publish(state)
	return nil
}

func (m *gameManager) updateState(change func(*GameState), publish bool) {
	m.mu.Lock()
	change(&m.state)
	state := m.state
	m.mu.Unlock()
	if publish {
		m.publish(state)
	}
}

func (m *gameManager) updateProgress(received, files int64) {
	m.mu.Lock()
	if received >= 0 {
		m.state.ReceivedBytes = received
	}
	if files >= 0 {
		m.state.FilesExtracted = files
	}
	if !m.lastUpdate.IsZero() && time.Since(m.lastUpdate) < 100*time.Millisecond {
		m.mu.Unlock()
		return
	}
	m.lastUpdate = time.Now()
	state := m.state
	m.mu.Unlock()
	m.publish(state)
}

func (m *gameManager) publish(state GameState) {
	if m.emit != nil {
		m.emit(state)
	}
}

func validCommit(sha string) bool {
	if len(sha) != 40 {
		return false
	}
	_, err := hex.DecodeString(sha)
	return err == nil
}

func validateGameRoot(root string) error {
	info, err := os.Stat(filepath.Join(root, "index.html"))
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("index.html is missing")
	}
	for _, name := range []string{"assets", "css", "js"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || !info.IsDir() {
			return fmt.Errorf("required asset directory %q is missing", name)
		}
	}
	return nil
}

type progressReader struct {
	reader   io.Reader
	received int64
	limit    int64
	update   func(int64)
	last     time.Time
}

func (p *progressReader) Read(buffer []byte) (int, error) {
	if p.limit > 0 {
		remaining := p.limit - p.received
		if remaining < 0 {
			return 0, errDownloadTooLarge
		}
		if int64(len(buffer)) > remaining+1 {
			buffer = buffer[:remaining+1]
		}
	}
	n, err := p.reader.Read(buffer)
	p.received += int64(n)
	if n > 0 && p.update != nil && (p.last.IsZero() || time.Since(p.last) >= 100*time.Millisecond) {
		p.last = time.Now()
		p.update(p.received)
	}
	if p.limit > 0 && p.received > p.limit {
		return n, errDownloadTooLarge
	}
	return n, err
}

func extractTarGz(ctx context.Context, source io.Reader, destination string, progress func(int64)) error {
	gzipReader, err := gzip.NewReader(source)
	if err != nil {
		return err
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	var prefix string
	var files, written int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		// GitHub codeload archives may begin with a POSIX PAX global
		// metadata header. It is not a filesystem entry and must not
		// participate in root-path or file-type validation.
		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name, currentPrefix, err := safeArchiveName(header.Name)
		if err != nil {
			return err
		}
		if prefix == "" {
			prefix = currentPrefix
		} else if currentPrefix != prefix {
			return errors.New("archive has multiple top-level directories")
		}
		if name == "" {
			if header.Typeflag != tar.TypeDir {
				return errors.New("archive root is not a directory")
			}
			continue
		}

		target := filepath.Join(destination, filepath.FromSlash(name))
		if !pathInside(destination, target) {
			return fmt.Errorf("archive path escapes destination: %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			files++
			if files > maxArchiveFiles {
				return errors.New("archive contains too many files")
			}
			if header.Size < 0 || header.Size > maxSingleFile || written > maxArchiveBytes-header.Size {
				return errors.New("archive exceeds the uncompressed size limit")
			}
			written += header.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, tarReader, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if progress != nil {
				progress(files)
			}
		default:
			return fmt.Errorf("archive contains unsupported file type for %q", header.Name)
		}
	}
	if prefix == "" {
		return errors.New("archive is empty")
	}
	return nil
}

func safeArchiveName(name string) (relative, prefix string, err error) {
	if name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return "", "", fmt.Errorf("unsafe archive path %q", name)
	}
	parts := strings.Split(strings.TrimSuffix(name, "/"), "/")
	if len(parts) == 0 || parts[0] == "" || parts[0] == "." || parts[0] == ".." {
		return "", "", fmt.Errorf("unsafe archive path %q", name)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", "", fmt.Errorf("unsafe archive path %q", name)
		}
	}
	prefix = parts[0]
	if len(parts) == 1 {
		return "", prefix, nil
	}
	return strings.Join(parts[1:], "/"), prefix, nil
}

func pathInside(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
