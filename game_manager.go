package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

const (
	gameRepository    = "shines871/idle-lineage-class"
	gameRepositoryURL = "https://github.com/" + gameRepository + ".git"
	gameBranch        = "main"
)

var (
	gameBranchReference = plumbing.NewBranchReferenceName(gameBranch)
	gameRemoteReference = plumbing.NewRemoteReferenceName("origin", gameBranch)
	gameFetchRefSpec    = config.RefSpec("+refs/heads/" + gameBranch + ":refs/remotes/origin/" + gameBranch)
)

type GameStatus string

const (
	StatusMissing         GameStatus = "missing"
	StatusResolving       GameStatus = "resolving"
	StatusInstalling      GameStatus = "installing"
	StatusReady           GameStatus = "ready"
	StatusChecking        GameStatus = "checking"
	StatusUpdateAvailable GameStatus = "update_available"
	StatusUpdating        GameStatus = "updating"
	StatusCancelled       GameStatus = "cancelled"
	StatusError           GameStatus = "error"
)

type GameState struct {
	Status          GameStatus `json:"status"`
	Commit          string     `json:"commit"`
	RemoteCommit    string     `json:"remoteCommit"`
	UpdateAvailable bool       `json:"updateAvailable"`
	ProgressPhase   string     `json:"progressPhase"`
	ProgressText    string     `json:"progressText"`
	ProgressPercent int        `json:"progressPercent"`
	ProgressSeconds int64      `json:"progressSeconds"`
	Message         string     `json:"message"`
	Error           string     `json:"error"`
}

type legacyManifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Repository    string `json:"repository"`
	Commit        string `json:"commit"`
}

type stateEmitter func(GameState)

type gameManager struct {
	mu sync.RWMutex

	paths         dataPaths
	repositoryURL string
	initialCommit string
	logger        *slog.Logger
	emit          stateEmitter
	state         GameState
	activeRoot    string
	legacyInstall bool
	cancel        context.CancelFunc
	running       bool
	wg            sync.WaitGroup
	lastProgress  time.Time
}

func newGameManager(paths dataPaths, emit stateEmitter) (*gameManager, error) {
	logger := slog.Default().With("component", "game_manager")
	m := &gameManager{
		paths:         paths,
		repositoryURL: gameRepositoryURL,
		initialCommit: developmentInitialGameCommit,
		logger:        logger,
		emit:          emit,
		state: GameState{
			Status:  StatusMissing,
			Message: "尚未安裝遊戲內容",
		},
	}
	logger.Info("initializing game manager", "root", paths.Root, "source", paths.Source, "development_commit", developmentInitialGameCommit)
	if err := m.initialise(); err != nil {
		logger.Error("game manager initialization failed", "error", err)
		m.state = GameState{Status: StatusError, Message: "無法載入既有安裝", Error: err.Error()}
	} else {
		logger.Info("game manager initialized", "status", m.state.Status, "commit", m.state.Commit, "legacy", m.legacyInstall)
	}
	return m, nil
}

func (m *gameManager) initialise() error {
	m.logger.Info("preparing game data directories", "game", m.paths.Game, "staging", m.paths.Staging)
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
		m.logger.Info("removing stale staging entry", "entry", entry.Name())
		if err := os.RemoveAll(filepath.Join(m.paths.Staging, entry.Name())); err != nil {
			return fmt.Errorf("remove stale staging data: %w", err)
		}
	}

	repository, err := git.PlainOpen(m.paths.Source)
	if err == nil {
		m.logger.Info("found installed Git working tree", "source", m.paths.Source)
		if err := validateGameRoot(m.paths.Source); err != nil {
			return fmt.Errorf("validate installed game: %w", err)
		}
		head, err := repository.Head()
		if err != nil {
			return fmt.Errorf("read installed Git revision: %w", err)
		}
		_ = os.Remove(legacyManifestPath(m.paths))
		_ = os.RemoveAll(filepath.Join(m.paths.Game, "versions"))
		m.activeRoot = m.paths.Source
		m.state = GameState{Status: StatusReady, Commit: head.Hash().String(), Message: "遊戲已可離線使用"}
		m.logger.Info("loaded installed Git revision", "commit", head.Hash().String())
		return nil
	}
	if !errors.Is(err, git.ErrRepositoryNotExists) {
		return fmt.Errorf("open installed Git repository: %w", err)
	}

	manifest, err := readLegacyManifest(m.paths)
	if errors.Is(err, os.ErrNotExist) {
		m.logger.Info("no installed game found")
		if err := os.RemoveAll(filepath.Join(m.paths.Game, "versions")); err != nil {
			return fmt.Errorf("remove orphaned legacy game versions: %w", err)
		}
		if err := os.RemoveAll(m.paths.Source); err != nil {
			return fmt.Errorf("remove game source without Git metadata: %w", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := m.migrateLegacyVersion(manifest.Commit); err != nil {
		return err
	}
	if err := validateGameRoot(m.paths.Source); err != nil {
		return fmt.Errorf("validate legacy game: %w", err)
	}
	m.legacyInstall = true
	m.logger.Info("loaded legacy game installation", "commit", manifest.Commit)
	m.activeRoot = m.paths.Source
	m.state = GameState{Status: StatusReady, Commit: manifest.Commit, Message: "遊戲已可離線使用"}
	return nil
}

func readLegacyManifest(paths dataPaths) (legacyManifest, error) {
	contents, err := os.ReadFile(legacyManifestPath(paths))
	if err != nil {
		return legacyManifest{}, err
	}
	var manifest legacyManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return legacyManifest{}, fmt.Errorf("decode legacy active version: %w", err)
	}
	if manifest.SchemaVersion != 1 || manifest.Repository != gameRepository || !validCommit(manifest.Commit) {
		return legacyManifest{}, errors.New("legacy active version manifest is invalid")
	}
	return manifest, nil
}

func legacyManifestPath(paths dataPaths) string {
	return filepath.Join(paths.Game, "active.json")
}

func (m *gameManager) migrateLegacyVersion(commit string) error {
	m.logger.Info("checking legacy game layout", "commit", commit)
	legacyVersions := filepath.Join(m.paths.Game, "versions")
	if validateGameRoot(m.paths.Source) != nil {
		legacyRoot := filepath.Join(legacyVersions, commit)
		if err := validateGameRoot(legacyRoot); err == nil {
			m.logger.Info("migrating legacy version directory", "from", legacyRoot, "to", m.paths.Source)
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
	if m.activeRoot == "" {
		return "", "", false
	}
	return m.activeRoot, m.state.Commit, true
}

func (m *gameManager) StartInstall() error {
	m.logger.Info("install requested")
	m.mu.Lock()
	if m.running || m.activeRoot != "" {
		m.logger.Info("install request ignored", "running", m.running, "installed", m.activeRoot != "")
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.running = true
	m.state = GameState{Status: StatusInstalling, ProgressPhase: "準備", ProgressText: "正在連線 Git server…", ProgressPercent: -1, Message: "正在 clone 官方 main 分支…"}
	state := m.state
	m.wg.Add(1)
	m.mu.Unlock()
	m.publish(state)

	go func() {
		defer m.wg.Done()
		fallback := GameState{Status: StatusMissing, Message: "尚未安裝遊戲內容"}
		m.finishJob("install", m.cloneAndActivate(ctx), fallback, "安裝失敗", "已取消安裝；可隨時重新開始")
	}()
	return nil
}

func (m *gameManager) StartCheckForUpdate() error {
	m.logger.Info("update check requested")
	m.mu.Lock()
	if m.activeRoot == "" {
		m.mu.Unlock()
		return errors.New("game is not installed")
	}
	if m.running {
		m.logger.Info("update check request ignored because another Git job is running")
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	fallback := m.state
	fallback.Status = StatusReady
	fallback.Error = ""
	m.cancel = cancel
	m.running = true
	m.state.Status = StatusChecking
	m.state.Message = "正在 fetch 官方 main 分支…"
	m.state.Error = ""
	m.state.ProgressPhase = "準備"
	m.state.ProgressText = "正在連線 Git server…"
	m.state.ProgressPercent = -1
	m.state.ProgressSeconds = 0
	state := m.state
	m.wg.Add(1)
	m.mu.Unlock()
	m.publish(state)

	go func() {
		defer m.wg.Done()
		m.finishJob("fetch", m.checkForUpdate(ctx), fallback, "檢查更新失敗；目前版本仍可使用", "已取消檢查更新")
	}()
	return nil
}

func (m *gameManager) StartUpdate() error {
	m.logger.Info("update requested")
	m.mu.Lock()
	if m.activeRoot == "" {
		m.mu.Unlock()
		return errors.New("game is not installed")
	}
	if m.running {
		m.logger.Info("update request ignored because another Git job is running")
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	fallback := m.state
	if fallback.Status != StatusUpdateAvailable {
		fallback.Status = StatusReady
	}
	m.cancel = cancel
	m.running = true
	m.state.Status = StatusUpdating
	m.state.Message = "正在 pull 最新遊戲內容…"
	m.state.Error = ""
	m.state.ProgressPhase = "準備"
	m.state.ProgressText = "正在連線 Git server…"
	m.state.ProgressPercent = -1
	m.state.ProgressSeconds = 0
	state := m.state
	m.wg.Add(1)
	m.mu.Unlock()
	m.publish(state)

	go func() {
		defer m.wg.Done()
		m.finishJob("pull", m.update(ctx), fallback, "更新失敗；目前版本仍可使用", "已取消更新；目前版本仍可使用")
	}()
	return nil
}

func (m *gameManager) CancelInstall() {
	m.logger.Info("Git job cancellation requested")
	m.mu.RLock()
	cancel := m.cancel
	m.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (m *gameManager) Shutdown() {
	m.logger.Info("shutting down game manager")
	m.CancelInstall()
	m.wg.Wait()
}

func (m *gameManager) finishJob(operation string, err error, fallback GameState, failureMessage, cancelledMessage string) {
	m.mu.Lock()
	m.running = false
	m.cancel = nil
	if err != nil {
		if m.activeRoot != "" {
			m.state = fallback
			if errors.Is(err, context.Canceled) {
				m.state.Message = cancelledMessage
				m.state.Error = ""
			} else {
				m.state.Message = failureMessage
				m.state.Error = err.Error()
			}
		} else if errors.Is(err, context.Canceled) {
			m.state = GameState{Status: StatusCancelled, Message: cancelledMessage}
		} else {
			m.state = GameState{Status: StatusError, Message: failureMessage, Error: err.Error()}
		}
	}
	state := m.state
	m.mu.Unlock()
	if err == nil {
		m.logger.Info("Git job completed", "operation", operation, "status", state.Status, "commit", state.Commit)
	} else if errors.Is(err, context.Canceled) {
		m.logger.Warn("Git job cancelled", "operation", operation)
	} else {
		m.logger.Error("Git job failed", "operation", operation, "error", err)
	}
	m.publish(state)
}

func (m *gameManager) cloneAndActivate(ctx context.Context) error {
	m.logger.Info("starting repository clone", "url", m.repositoryURL, "branch", gameBranch, "depth", 1)
	progress := newGitProgressReporter(m, "clone", "正在連線並下載 Git objects…")
	defer progress.Close()
	staging, err := os.MkdirTemp(m.paths.Staging, "clone-")
	if err != nil {
		return fmt.Errorf("create clone directory: %w", err)
	}
	defer os.RemoveAll(staging)

	repository, err := git.PlainCloneContext(ctx, staging, false, &git.CloneOptions{
		URL:           m.repositoryURL,
		ReferenceName: gameBranchReference,
		SingleBranch:  true,
		Depth:         1,
		Tags:          git.NoTags,
		Progress:      progress,
	})
	if err != nil {
		return fmt.Errorf("clone game repository: %w", err)
	}
	m.logger.Info("repository clone transfer completed", "staging", staging)
	progress.Stage("準備版本", "正在選擇初始 commit…")
	if err := m.checkoutInitialCommit(ctx, repository, progress); err != nil {
		return err
	}
	progress.Stage("驗證檔案", "正在驗證遊戲檔案…")
	if err := validateGameRoot(staging); err != nil {
		return fmt.Errorf("validate cloned game: %w", err)
	}
	head, err := repository.Head()
	if err != nil {
		return fmt.Errorf("read cloned revision: %w", err)
	}
	m.logger.Info("cloned revision validated", "commit", head.Hash().String())
	progress.Stage("啟用版本", "正在切換遊戲 working tree…")
	if err := m.replaceSource(staging); err != nil {
		return err
	}
	m.mu.Lock()
	m.legacyInstall = false
	m.mu.Unlock()
	_ = os.Remove(legacyManifestPath(m.paths))
	return m.activate(head.Hash().String(), "安裝完成，可離線啟動")
}

func (m *gameManager) checkoutInitialCommit(ctx context.Context, repository *git.Repository, progress *gitProgressReporter) error {
	if m.initialCommit == "" {
		m.logger.Info("using cloned main tip")
		return nil
	}
	m.logger.Info("preparing development initial commit", "commit", m.initialCommit)
	if !validCommit(m.initialCommit) {
		return errors.New("development initial commit is invalid")
	}
	hash := plumbing.NewHash(m.initialCommit)
	if _, err := repository.CommitObject(hash); err != nil {
		progress.Stage("取得測試版本", "正在下載 development 初始 commit…")
		developmentReference := plumbing.NewRemoteReferenceName("origin", "development-base")
		refSpec := config.RefSpec(m.initialCommit + ":" + developmentReference.String())
		fetchErr := repository.FetchContext(ctx, &git.FetchOptions{
			RemoteName: "origin",
			RefSpecs:   []config.RefSpec{refSpec},
			Depth:      1,
			Tags:       git.NoTags,
			Progress:   progress,
		})
		if fetchErr != nil && !errors.Is(fetchErr, git.NoErrAlreadyUpToDate) {
			// Local/file transports do not advertise exact-SHA wants. Deepening
			// main is a development-only fallback; GitHub normally takes the
			// single-commit path above.
			fetchErr = repository.FetchContext(ctx, &git.FetchOptions{
				RemoteName: "origin",
				RefSpecs:   []config.RefSpec{gameFetchRefSpec},
				Depth:      1_000_000,
				Tags:       git.NoTags,
				Progress:   progress,
			})
		}
		if fetchErr != nil && !errors.Is(fetchErr, git.NoErrAlreadyUpToDate) {
			return fmt.Errorf("fetch development initial commit: %w", fetchErr)
		}
		if _, err := repository.CommitObject(hash); err != nil {
			return fmt.Errorf("read development initial commit: %w", err)
		}
		_ = repository.Storer.RemoveReference(developmentReference)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		return fmt.Errorf("open cloned working tree: %w", err)
	}
	progress.Stage("Checkout", "正在 checkout development 初始 commit…")
	if err := worktree.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: hash}); err != nil {
		return fmt.Errorf("checkout development initial commit: %w", err)
	}
	// The initial shallow clone may have marked today's main tip as the first
	// shallow boundary. Once we deliberately rewind, the pinned commit is the
	// only meaningful boundary for later fast-forward checks and pulls.
	if err := repository.Storer.SetShallow([]plumbing.Hash{hash}); err != nil {
		return fmt.Errorf("set development shallow baseline: %w", err)
	}
	if err := repository.Storer.SetReference(plumbing.NewHashReference(gameRemoteReference, hash)); err != nil {
		return fmt.Errorf("prepare development update baseline: %w", err)
	}
	m.logger.Info("development initial commit checked out", "commit", hash.String())
	return nil
}

func (m *gameManager) checkForUpdate(ctx context.Context) error {
	m.mu.RLock()
	legacy := m.legacyInstall
	localCommit := m.state.Commit
	m.mu.RUnlock()
	if legacy {
		m.logger.Info("legacy installation requires Git conversion", "commit", localCommit)
		m.setUpdateState(localCommit, true, "既有安裝需轉換一次；更新時會建立 Git working tree")
		return nil
	}

	repository, err := git.PlainOpen(m.paths.Source)
	if err != nil {
		return fmt.Errorf("open game repository: %w", err)
	}
	m.logger.Info("fetching origin/main", "local_commit", localCommit)
	progress := newGitProgressReporter(m, "fetch", "正在向 origin/main 查詢更新…")
	defer progress.Close()
	err = repository.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{gameFetchRefSpec},
		Tags:       git.NoTags,
		Progress:   progress,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetch game updates: %w", err)
	}

	head, err := repository.Head()
	if err != nil {
		return fmt.Errorf("read local game revision: %w", err)
	}
	remote, err := repository.Reference(gameRemoteReference, true)
	if err != nil {
		return fmt.Errorf("read remote game revision: %w", err)
	}
	m.logger.Info("comparing Git revisions", "local", head.Hash().String(), "remote", remote.Hash().String())
	progress.Stage("比較版本", "正在比較 local HEAD 與 origin/main…")
	if head.Hash() == remote.Hash() {
		m.logger.Info("game is already up to date", "commit", head.Hash().String())
		m.setUpdateState(remote.Hash().String(), false, "目前已是最新版本")
		return nil
	}

	localObject, localErr := repository.CommitObject(head.Hash())
	if localErr != nil {
		return fmt.Errorf("read local commit: %w", localErr)
	}
	remoteObject, remoteErr := repository.CommitObject(remote.Hash())
	if remoteErr != nil {
		return fmt.Errorf("read remote commit: %w", remoteErr)
	}
	behind, ancestorErr := localObject.IsAncestor(remoteObject)
	if ancestorErr == nil && !behind {
		return errors.New("local main is not behind origin/main; refusing a non-fast-forward update")
	}
	if ancestorErr != nil {
		shallow, shallowErr := repository.Storer.Shallow()
		if shallowErr != nil || len(shallow) == 0 {
			return fmt.Errorf("compare local and remote revisions: %w", ancestorErr)
		}
	}
	m.setUpdateState(remote.Hash().String(), true, "發現新的官方版本")
	m.logger.Info("game update available", "local", head.Hash().String(), "remote", remote.Hash().String())
	return nil
}

func (m *gameManager) setUpdateState(remoteCommit string, available bool, message string) {
	m.mu.Lock()
	if available {
		m.state.Status = StatusUpdateAvailable
	} else {
		m.state.Status = StatusReady
	}
	m.state.RemoteCommit = remoteCommit
	m.state.UpdateAvailable = available
	m.state.Message = message
	m.state.Error = ""
	state := m.state
	m.mu.Unlock()
	m.publish(state)
}

func (m *gameManager) update(ctx context.Context) error {
	m.mu.RLock()
	legacy := m.legacyInstall
	m.mu.RUnlock()
	if legacy {
		m.logger.Info("updating legacy installation by replacing it with a clone")
		return m.cloneAndActivate(ctx)
	}

	m.logger.Info("opening game working tree for pull", "source", m.paths.Source)
	repository, err := git.PlainOpen(m.paths.Source)
	if err != nil {
		return fmt.Errorf("open game repository: %w", err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		return fmt.Errorf("open game working tree: %w", err)
	}
	status, err := worktree.Status()
	if err != nil {
		return fmt.Errorf("inspect game working tree: %w", err)
	}
	if !status.IsClean() {
		m.logger.Warn("pull refused because working tree is dirty", "status", status.String())
		return errors.New("game working tree has local changes; refusing to overwrite them")
	}
	progress := newGitProgressReporter(m, "pull", "正在下載並套用更新…")
	defer progress.Close()
	m.logger.Info("pulling origin/main")
	err = worktree.PullContext(ctx, &git.PullOptions{
		RemoteName:    "origin",
		ReferenceName: gameBranchReference,
		SingleBranch:  true,
		Progress:      progress,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("pull game update: %w", err)
	}
	progress.Stage("驗證檔案", "正在驗證更新後的遊戲檔案…")
	if err := validateGameRoot(m.paths.Source); err != nil {
		return fmt.Errorf("validate updated game: %w", err)
	}
	head, err := repository.Head()
	if err != nil {
		return fmt.Errorf("read updated revision: %w", err)
	}
	m.logger.Info("pull completed", "commit", head.Hash().String())
	return m.activate(head.Hash().String(), "更新完成，已載入最新版本")
}

func (m *gameManager) replaceSource(staging string) error {
	m.logger.Info("replacing active game source", "staging", staging, "destination", m.paths.Source)
	backup := filepath.Join(m.paths.Staging, ".previous-src")
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("clean previous game backup: %w", err)
	}

	hadSource := validateGameRoot(m.paths.Source) == nil
	if hadSource {
		m.logger.Info("backing up current game source", "backup", backup)
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
	m.logger.Info("game source replacement completed", "destination", m.paths.Source)
	return nil
}

func (m *gameManager) activate(sha, message string) error {
	m.logger.Info("activating game revision", "commit", sha)
	m.mu.Lock()
	m.activeRoot = m.paths.Source
	m.state = GameState{Status: StatusReady, Commit: sha, RemoteCommit: sha, Message: message}
	state := m.state
	m.mu.Unlock()
	m.publish(state)
	return nil
}

func (m *gameManager) updateGitProgress(phase, text string, percent int, seconds int64, force bool) {
	m.mu.Lock()
	m.state.ProgressPhase = phase
	m.state.ProgressText = text
	m.state.ProgressPercent = percent
	m.state.ProgressSeconds = seconds
	if !force && !m.lastProgress.IsZero() && time.Since(m.lastProgress) < 100*time.Millisecond {
		m.mu.Unlock()
		return
	}
	m.lastProgress = time.Now()
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
