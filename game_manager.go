package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	gitindex "github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const (
	gameRepository        = "shines871/idle-lineage-class"
	gameRepositoryPageURL = "https://github.com/" + gameRepository
	gameRepositoryURL     = gameRepositoryPageURL + ".git"
	gameBranch            = "main"
	finderMetadata        = ".DS_Store"
)

var (
	gameBranchReference  = plumbing.NewBranchReferenceName(gameBranch)
	gameRemoteReference  = plumbing.NewRemoteReferenceName("origin", gameBranch)
	gameFetchRefSpec     = config.RefSpec("+refs/heads/" + gameBranch + ":refs/remotes/origin/" + gameBranch)
	errActiveGameMissing = errors.New("active game installation is missing")
)

type repositoryStateError struct {
	err error
}

func (e *repositoryStateError) Error() string {
	return e.err.Error()
}

func (e *repositoryStateError) Unwrap() error {
	return e.err
}

func repositoryStateErrorf(format string, args ...any) error {
	return &repositoryStateError{err: fmt.Errorf(format, args...)}
}

func isRepositoryStateError(err error) bool {
	var target *repositoryStateError
	return errors.As(err, &target)
}

type gameTreePathKind uint8

const (
	gameTreeFile gameTreePathKind = iota
	gameTreeDirectory
)

type GameStatus string

const (
	StatusMissing            GameStatus = "missing"
	StatusResolving          GameStatus = "resolving"
	StatusInstalling         GameStatus = "installing"
	StatusReady              GameStatus = "ready"
	StatusChecking           GameStatus = "checking"
	StatusUpdateAvailable    GameStatus = "update_available"
	StatusUpdating           GameStatus = "updating"
	StatusMoving             GameStatus = "moving"
	StatusStorageUnavailable GameStatus = "storage_unavailable"
	StatusCancelled          GameStatus = "cancelled"
	StatusError              GameStatus = "error"
)

type GameState struct {
	Revision         uint64     `json:"revision"`
	Status           GameStatus `json:"status"`
	Commit           string     `json:"commit"`
	CommitTime       string     `json:"commitTime"`
	RemoteCommit     string     `json:"remoteCommit"`
	RemoteCommitTime string     `json:"remoteCommitTime"`
	UpdateAvailable  bool       `json:"updateAvailable"`
	ProgressPhase    string     `json:"progressPhase"`
	ProgressText     string     `json:"progressText"`
	ProgressPercent  int        `json:"progressPercent"`
	ProgressSeconds  int64      `json:"progressSeconds"`
	Message          string     `json:"message"`
	Error            string     `json:"error"`
}

type stateEmitter func(GameState)

type gameManager struct {
	mu sync.RWMutex

	paths             dataPaths
	repositoryURL     string
	initialCommit     string
	initialFetch      func(context.Context, *git.Repository, *git.FetchOptions) error
	logger            *slog.Logger
	emit              stateEmitter
	state             GameState
	activeRoot        string
	cancel            context.CancelFunc
	running           bool
	wg                sync.WaitGroup
	lastProgress      time.Time
	folderMoveStarted time.Time
	revision          uint64
	onInstalledChange func(bool)
}

func newGameManager(paths dataPaths, emit stateEmitter) (*gameManager, error) {
	logger := slog.Default().With("component", "game_manager")
	m := &gameManager{
		paths:         paths,
		repositoryURL: gameRepositoryURL,
		initialCommit: developmentInitialGameCommit,
		initialFetch: func(ctx context.Context, repository *git.Repository, options *git.FetchOptions) error {
			return repository.FetchContext(ctx, options)
		},
		logger: logger,
		emit:   emit,
		state: GameState{
			Status:  StatusMissing,
			Message: "尚未下載遊戲",
		},
	}
	logger.Info("initializing game manager", "root", paths.Root, "source", paths.Source, "development_commit", developmentInitialGameCommit)
	if err := m.initialise(); err != nil {
		logger.Error("game manager initialization failed", "error", err)
		if isStorageUnavailableError(err) {
			m.state = GameState{Status: StatusStorageUnavailable, Message: "遊戲儲存位置無法使用", Error: err.Error()}
		} else {
			m.state = GameState{Status: StatusError, Message: "無法載入既有安裝", Error: err.Error()}
		}
	} else {
		logger.Info("game manager initialized", "status", m.state.Status, "commit", m.state.Commit)
	}
	m.revision = 1
	m.state.Revision = m.revision
	return m, nil
}

func (m *gameManager) initialise() error {
	state, activeRoot, err := inspectGameInstallation(m.paths, m.logger)
	if err != nil {
		return err
	}
	m.state = state
	m.activeRoot = activeRoot
	return nil
}

func (m *gameManager) State() GameState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

func (m *gameManager) folderChangeSnapshot() (installed, running bool, activeRoot string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeRoot != "", m.running, m.activeRoot
}

func (m *gameManager) applyFolderPaths(paths dataPaths, state GameState, activeRoot string) {
	m.mu.Lock()
	m.paths = paths
	m.activeRoot = activeRoot
	m.state = state
	m.advanceRevisionLocked()
	state = m.state
	m.mu.Unlock()
	m.publish(state)
}

func (m *gameManager) commitFolderPaths(paths dataPaths, state GameState, activeRoot string, persist func() error) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return errors.New("請等待目前的遊戲作業完成後再變更資料夾")
	}
	if err := persist(); err != nil {
		m.mu.Unlock()
		return err
	}
	m.paths = paths
	m.activeRoot = activeRoot
	m.state = state
	m.advanceRevisionLocked()
	state = m.state
	m.mu.Unlock()
	m.publish(state)
	return nil
}

func (m *gameManager) applyFolderInspectionError(paths dataPaths, err error) {
	m.mu.Lock()
	m.paths = paths
	if isStorageUnavailableError(err) {
		m.state.Status = StatusStorageUnavailable
		m.state.Message = "遊戲儲存位置無法使用"
	} else {
		m.state.Status = StatusError
		m.state.Message = "遊戲資料內容異常"
	}
	m.state.Error = err.Error()
	m.advanceRevisionLocked()
	state := m.state
	m.mu.Unlock()
	m.publish(state)
}

func (m *gameManager) beginFolderMove(expected dataPaths) (GameState, error) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return GameState{}, errors.New("請等待目前的遊戲作業完成後再搬移")
	}
	if m.activeRoot == "" || !sameCleanPath(m.activeRoot, expected.Source) {
		m.mu.Unlock()
		return GameState{}, errors.New("目前遊戲位置已變更，請重新選擇資料夾")
	}
	previous := m.state
	switch previous.Status {
	case StatusReady, StatusUpdateAvailable:
	default:
		m.mu.Unlock()
		return GameState{}, fmt.Errorf("目前狀態 %q 無法搬移遊戲", previous.Status)
	}
	m.running = true
	m.state.Status = StatusMoving
	m.state.Message = "正在搬移遊戲至新位置…"
	m.state.Error = ""
	m.state.ProgressPhase = "準備搬移"
	m.state.ProgressText = "正在準備遊戲檔案…"
	m.state.ProgressPercent = -1
	m.state.ProgressSeconds = 0
	m.folderMoveStarted = time.Now()
	m.advanceRevisionLocked()
	state := m.state
	m.wg.Add(1)
	m.mu.Unlock()
	m.publish(state)
	return previous, nil
}

func (m *gameManager) updateFolderMoveProgress(phase, text string, percent int) {
	m.mu.RLock()
	started := m.folderMoveStarted
	m.mu.RUnlock()
	seconds := int64(0)
	if !started.IsZero() {
		seconds = int64(time.Since(started).Seconds())
	}
	m.updateGitProgress(phase, text, percent, seconds, true)
}

func (m *gameManager) finishFolderMoveFailure(previous GameState) {
	m.mu.Lock()
	m.running = false
	m.folderMoveStarted = time.Time{}
	m.state = previous
	m.state.Error = ""
	m.advanceRevisionLocked()
	state := m.state
	m.mu.Unlock()
	m.wg.Done()
	m.publish(state)
}

func (m *gameManager) finishFolderMoveSuccess(paths dataPaths, previous GameState) {
	m.mu.Lock()
	m.paths = paths
	m.activeRoot = paths.Source
	m.running = false
	m.folderMoveStarted = time.Time{}
	m.state = previous
	m.state.Status = StatusReady
	m.state.UpdateAvailable = false
	m.state.Message = "遊戲已搬移至新位置"
	m.state.Error = ""
	m.state.ProgressPhase = ""
	m.state.ProgressText = ""
	m.state.ProgressPercent = 0
	m.state.ProgressSeconds = 0
	m.advanceRevisionLocked()
	state := m.state
	m.mu.Unlock()
	m.wg.Done()
	m.publish(state)
}

func (m *gameManager) ActiveVersion() (root, commit string, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.activeRoot == "" {
		return "", "", false
	}
	return m.activeRoot, m.state.Commit, true
}

func (m *gameManager) reconcileMissingActiveGame() (bool, error) {
	m.mu.Lock()
	if m.activeRoot == "" {
		m.mu.Unlock()
		return false, nil
	}
	if m.state.Status == StatusInstalling || m.state.Status == StatusUpdating {
		m.mu.Unlock()
		return false, nil
	}
	root := m.activeRoot
	// Every manager created by the application has GameRoot populated. Keep the
	// guard for small in-package managers used by callers that only provide an
	// active Source path; there is no independent storage root to probe in that
	// case.
	if m.paths.GameRoot != "" {
		if _, err := validateGameFolderRoot(m.paths.GameRoot); err != nil {
			m.state.Status = StatusStorageUnavailable
			m.state.Message = "遊戲儲存位置無法使用"
			m.state.Error = err.Error()
			m.advanceRevisionLocked()
			state := m.state
			m.mu.Unlock()
			m.publish(state)
			return true, nil
		}
	}
	missing, err := gameRootMissing(root)
	if err != nil {
		m.mu.Unlock()
		return false, fmt.Errorf("inspect active game directory: %w", err)
	}
	if !missing {
		m.mu.Unlock()
		return false, nil
	}
	cancel := m.cancel
	state := m.transitionToMissingLocked()
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	m.logger.Warn("active game installation disappeared", "root", root)
	m.publish(state)
	if m.onInstalledChange != nil {
		m.onInstalledChange(false)
	}
	return true, nil
}

func (m *gameManager) transitionToMissingLocked() GameState {
	m.activeRoot = ""
	m.state = GameState{Status: StatusMissing, Message: "尚未下載遊戲"}
	m.advanceRevisionLocked()
	return m.state
}

func gameRootMissing(root string) (bool, error) {
	_, err := os.Lstat(root)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, err
}

// withLaunchableRoot keeps update state transitions out of the launch critical
// section. If an update wins the lock first, launch is rejected as updating;
// if launch wins first, the update cannot begin changing files until the system
// opener has accepted or rejected the request.
func (m *gameManager) withLaunchableRoot(open func(string) error) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	switch m.state.Status {
	case StatusReady, StatusChecking, StatusUpdateAvailable:
	default:
		return fmt.Errorf("game cannot be launched while status is %q", m.state.Status)
	}
	if m.activeRoot == "" {
		return errors.New("game is not installed")
	}
	return open(m.activeRoot)
}

func (m *gameManager) withOpenableRoot(open func(string) error) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch m.state.Status {
	case StatusReady, StatusChecking, StatusUpdateAvailable, StatusUpdating:
	default:
		return fmt.Errorf("game folder cannot be opened while status is %q", m.state.Status)
	}
	if m.activeRoot == "" {
		return errors.New("game is not installed")
	}
	return open(m.activeRoot)
}

func (m *gameManager) StartInstall() error {
	m.logger.Info("install requested")
	m.mu.Lock()
	if _, err := validateGameFolderRoot(m.paths.GameRoot); err != nil {
		if isStorageUnavailableError(err) {
			m.state.Status = StatusStorageUnavailable
			m.state.Message = "遊戲儲存位置無法使用"
		} else {
			m.state.Status = StatusError
			m.state.Message = "遊戲資料內容異常"
		}
		m.state.Error = err.Error()
		m.advanceRevisionLocked()
		state := m.state
		m.mu.Unlock()
		m.publish(state)
		return err
	}
	if m.running || m.activeRoot != "" {
		m.logger.Info("install request ignored", "running", m.running, "installed", m.activeRoot != "")
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.running = true
	message := "正在下載官方遊戲版本…"
	if m.initialCommit != "" {
		message = "正在下載 development 固定版本…"
	}
	m.state = GameState{Status: StatusInstalling, ProgressPhase: "準備", ProgressText: "正在連線更新伺服器…", ProgressPercent: -1, Message: message}
	m.advanceRevisionLocked()
	state := m.state
	m.wg.Add(1)
	m.mu.Unlock()
	m.publish(state)

	go func() {
		defer m.wg.Done()
		fallback := GameState{Status: StatusMissing, Message: "尚未下載遊戲"}
		m.finishJob("install", m.cloneAndActivate(ctx), fallback, "安裝失敗", "已取消安裝；可隨時重新開始")
	}()
	return nil
}

func (m *gameManager) StartCheckForUpdate() error {
	m.logger.Info("update check requested")
	if missing, err := m.reconcileMissingActiveGame(); err != nil {
		return err
	} else if missing {
		return nil
	}
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
	m.state.Message = "正在檢查官方最新版本…"
	m.state.Error = ""
	m.state.ProgressPhase = "準備"
	m.state.ProgressText = "正在連線更新伺服器…"
	m.state.ProgressPercent = -1
	m.state.ProgressSeconds = 0
	m.advanceRevisionLocked()
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
	if missing, err := m.reconcileMissingActiveGame(); err != nil {
		return err
	} else if missing {
		return nil
	}
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
	m.state.Message = "正在同步官方最新遊戲內容…"
	m.state.Error = ""
	m.state.ProgressPhase = "準備"
	m.state.ProgressText = "正在連線更新伺服器…"
	m.state.ProgressPercent = -1
	m.state.ProgressSeconds = 0
	m.advanceRevisionLocked()
	state := m.state
	m.wg.Add(1)
	m.mu.Unlock()
	m.publish(state)

	go func() {
		defer m.wg.Done()
		m.finishJob("sync", m.update(ctx), fallback, "更新失敗；目前版本仍可使用", "已取消更新；目前版本仍可使用")
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
	reconciledMissing := m.state.Status == StatusMissing && m.activeRoot == ""
	missingRoot := false
	if m.activeRoot != "" {
		missingRoot, _ = gameRootMissing(m.activeRoot)
	}
	if errors.Is(err, errActiveGameMissing) || missingRoot {
		if !reconciledMissing {
			m.transitionToMissingLocked()
		}
		reconciledMissing = true
	} else if err != nil && !reconciledMissing {
		if m.activeRoot != "" {
			m.state = fallback
			if errors.Is(err, context.Canceled) {
				m.state.Message = cancelledMessage
				m.state.Error = ""
			} else {
				m.state.Message = failureMessage
				m.state.Error = userFacingOperationError(operation)
			}
		} else if errors.Is(err, context.Canceled) {
			m.state = GameState{Status: StatusCancelled, Message: cancelledMessage}
		} else {
			m.state = GameState{Status: StatusError, Message: failureMessage, Error: userFacingOperationError(operation)}
		}
		m.advanceRevisionLocked()
	}
	state := m.state
	m.mu.Unlock()
	if reconciledMissing {
		m.logger.Warn("Git job ended after active game installation disappeared", "operation", operation)
	} else if err == nil {
		m.logger.Info("Git job completed", "operation", operation, "status", state.Status, "commit", state.Commit)
	} else if errors.Is(err, context.Canceled) {
		m.logger.Warn("Git job cancelled", "operation", operation)
	} else {
		m.logger.Error("Git job failed", "operation", operation, "error", err)
	}
	m.publish(state)
	if reconciledMissing && m.onInstalledChange != nil {
		m.onInstalledChange(false)
	}
}

func userFacingOperationError(operation string) string {
	switch operation {
	case "install":
		return "無法下載遊戲。請確認網路連線與可用磁碟空間後重試。"
	case "fetch":
		return "無法連線檢查更新。請確認網路連線後再試一次。"
	case "sync":
		return "無法完成更新。目前版本仍可使用，請稍後再試。"
	default:
		return "操作失敗，請稍後再試。"
	}
}

func (m *gameManager) cloneAndActivate(ctx context.Context) error {
	return m.cloneVersionAndActivate(ctx, false, "安裝完成，可離線啟動")
}

func (m *gameManager) replaceWithLatestAndActivate(ctx context.Context, cause error) error {
	missing, err := gameRootMissing(m.paths.Source)
	if err != nil {
		return fmt.Errorf("inspect game source before rebuild: %w", err)
	}
	if missing {
		return fmt.Errorf("%w: %v", errActiveGameMissing, cause)
	}
	m.logger.Warn("rebuilding managed game repository", "cause", cause)
	m.beginVersionReplacement()
	if err := m.cloneVersionAndActivate(ctx, true, "更新完成；已重新下載官方最新版本"); err != nil {
		return fmt.Errorf("replace game version after %v: %w", cause, err)
	}
	return nil
}

func (m *gameManager) beginVersionReplacement() {
	m.mu.Lock()
	m.state.Status = StatusUpdating
	m.state.Message = "正在重新下載官方最新版本…"
	m.state.Error = ""
	m.state.ProgressPhase = "重新下載"
	m.state.ProgressText = "正在準備下載官方版本…"
	m.state.ProgressPercent = -1
	m.state.ProgressSeconds = 0
	m.advanceRevisionLocked()
	state := m.state
	m.mu.Unlock()
	m.publish(state)
}

func (m *gameManager) cloneVersionAndActivate(ctx context.Context, forceLatest bool, successMessage string) error {
	if err := os.MkdirAll(m.paths.Staging, 0o755); err != nil {
		return fmt.Errorf("prepare clone staging directory: %w", err)
	}
	staging, err := os.MkdirTemp(m.paths.Staging, "clone-")
	if err != nil {
		return fmt.Errorf("create clone directory: %w", err)
	}
	defer os.RemoveAll(staging)
	operation := "clone"
	if forceLatest {
		operation = "rebuild"
	}
	progress := newGitProgressReporter(m, operation, "正在連線並下載官方遊戲檔案…")
	progress.WatchPackDir(filepath.Join(staging, ".git", "objects", "pack"))
	defer progress.Close()

	var repository *git.Repository
	if forceLatest || m.initialCommit == "" {
		m.logger.Info("starting repository clone", "url", m.repositoryURL, "branch", gameBranch, "depth", 1)
		repository, err = git.PlainCloneContext(ctx, staging, false, &git.CloneOptions{
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
	} else {
		repository, err = m.fetchDevelopmentRepository(ctx, staging, progress)
		if err != nil {
			return err
		}
	}
	m.ensureManagedGameExclude(staging)
	progress.Stage("驗證檔案", "正在驗證遊戲檔案…")
	if err := validateGameRoot(staging); err != nil {
		return fmt.Errorf("validate cloned game: %w", err)
	}
	head, err := repository.Head()
	if err != nil {
		return fmt.Errorf("read cloned revision: %w", err)
	}
	commitTime, err := repositoryCommitTime(repository, head.Hash())
	if err != nil {
		return fmt.Errorf("read cloned commit time: %w", err)
	}
	m.logger.Info("cloned revision validated", "commit", head.Hash().String())
	progress.Stage("啟用版本", "正在啟用遊戲版本…")
	if err := m.replaceSource(staging); err != nil {
		return err
	}
	return m.activate(head.Hash().String(), commitTime, successMessage)
}

func (m *gameManager) fetchDevelopmentRepository(ctx context.Context, staging string, progress *gitProgressReporter) (*git.Repository, error) {
	if !validCommit(m.initialCommit) {
		return nil, errors.New("development initial commit is invalid")
	}
	m.logger.Info("starting development fixed-commit fetch", "url", m.repositoryURL, "commit", m.initialCommit, "depth", 1)
	repository, err := git.PlainInit(staging, false)
	if err != nil {
		return nil, fmt.Errorf("initialize development repository: %w", err)
	}
	if _, err := repository.CreateRemote(&config.RemoteConfig{
		Name:  "origin",
		URLs:  []string{m.repositoryURL},
		Fetch: []config.RefSpec{gameFetchRefSpec},
	}); err != nil {
		return nil, fmt.Errorf("create development origin remote: %w", err)
	}

	hash := plumbing.NewHash(m.initialCommit)
	progress.Stage("取得固定版本", "正在下載 development 固定 commit…")
	fetchErr := m.initialFetch(ctx, repository, &git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec(m.initialCommit + ":" + gameRemoteReference.String())},
		Depth:      1,
		Tags:       git.NoTags,
		Progress:   progress,
	})
	if errors.Is(fetchErr, git.ErrExactSHA1NotSupported) {
		m.logger.Warn("Git server does not support exact-SHA fetch; falling back to full main history", "commit", m.initialCommit)
		progress.Stage("相容模式", "Git server 不支援固定 SHA；正在下載完整 main 歷史…")
		fetchErr = m.initialFetch(ctx, repository, &git.FetchOptions{
			RemoteName: "origin",
			RefSpecs:   []config.RefSpec{gameFetchRefSpec},
			Depth:      0,
			Tags:       git.NoTags,
			Progress:   progress,
		})
	}
	if fetchErr != nil && !errors.Is(fetchErr, git.NoErrAlreadyUpToDate) {
		return nil, fmt.Errorf("fetch development initial commit: %w", fetchErr)
	}
	if _, err := repository.CommitObject(hash); err != nil {
		return nil, fmt.Errorf("read development initial commit: %w", err)
	}

	worktree, err := repository.Worktree()
	if err != nil {
		return nil, fmt.Errorf("open development working tree: %w", err)
	}
	progress.Stage("Checkout", "正在 checkout development 固定 commit…")
	if err := worktree.Checkout(&git.CheckoutOptions{
		Branch: gameBranchReference,
		Hash:   hash,
		Create: true,
		Force:  true,
	}); err != nil {
		return nil, fmt.Errorf("checkout development initial commit: %w", err)
	}

	if err := repository.Storer.SetReference(plumbing.NewHashReference(gameRemoteReference, hash)); err != nil {
		return nil, fmt.Errorf("prepare development update baseline: %w", err)
	}
	if err := repository.Storer.SetShallow([]plumbing.Hash{hash}); err != nil {
		return nil, fmt.Errorf("set development shallow baseline: %w", err)
	}
	repositoryConfig, err := repository.Config()
	if err != nil {
		return nil, fmt.Errorf("read development repository config: %w", err)
	}
	repositoryConfig.Branches[gameBranch] = &config.Branch{
		Name:   gameBranch,
		Remote: "origin",
		Merge:  gameBranchReference,
	}
	if err := repository.Storer.SetConfig(repositoryConfig); err != nil {
		return nil, fmt.Errorf("configure development main tracking: %w", err)
	}
	m.logger.Info("development fixed commit fetched and checked out", "commit", hash.String())
	return repository, nil
}

func (m *gameManager) checkForUpdate(ctx context.Context) error {
	repository, err := git.PlainOpen(m.paths.Source)
	if err != nil {
		return fmt.Errorf("open game repository: %w", err)
	}
	head, err := repository.Head()
	if err != nil {
		return fmt.Errorf("read local game revision: %w", err)
	}

	m.logger.Info("fetching official main", "local_commit", head.Hash().String())
	remote, remoteObject, err := m.fetchOfficialMain(ctx, repository, "fetch", "正在查詢官方最新版本…")
	if err != nil {
		return err
	}
	remoteCommitTime := remoteObject.Committer.When.Format(time.RFC3339)
	m.logger.Info("comparing Git revisions", "local", head.Hash().String(), "remote", remote.Hash().String())
	m.updateGitProgress("比較版本", "正在比較本機版本與官方版本…", -1, m.State().ProgressSeconds, true)
	if head.Hash() == remote.Hash() {
		m.logger.Info("game is already up to date", "commit", head.Hash().String())
		m.setUpdateState(remote.Hash().String(), remoteCommitTime, false, "目前已是最新版本")
		return nil
	}

	m.setUpdateState(remote.Hash().String(), remoteCommitTime, true, "發現新的官方版本")
	m.logger.Info("game update available", "local", head.Hash().String(), "remote", remote.Hash().String())
	return nil
}

func (m *gameManager) fetchOfficialMain(ctx context.Context, repository *git.Repository, operation, initialText string) (*plumbing.Reference, *object.Commit, error) {
	progress := newGitProgressReporter(m, operation, initialText)
	progress.WatchPackDir(filepath.Join(m.paths.Source, ".git", "objects", "pack"))
	defer progress.Close()
	officialRemote := git.NewRemote(repository.Storer, &config.RemoteConfig{
		Name:  "origin",
		URLs:  []string{m.repositoryURL},
		Fetch: []config.RefSpec{gameFetchRefSpec},
	})
	err := officialRemote.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{gameFetchRefSpec},
		Tags:       git.NoTags,
		Force:      true,
		Progress:   progress,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil, nil, fmt.Errorf("fetch official game version: %w", err)
	}
	remote, err := repository.Reference(gameRemoteReference, true)
	if err != nil {
		return nil, nil, repositoryStateErrorf("read fetched official revision: %w", err)
	}
	remoteObject, err := repository.CommitObject(remote.Hash())
	if err != nil {
		return nil, nil, repositoryStateErrorf("read fetched official commit: %w", err)
	}
	return remote, remoteObject, nil
}

func (m *gameManager) setUpdateState(remoteCommit, remoteCommitTime string, available bool, message string) {
	m.mu.Lock()
	if available {
		m.state.Status = StatusUpdateAvailable
	} else {
		m.state.Status = StatusReady
	}
	m.state.RemoteCommit = remoteCommit
	m.state.RemoteCommitTime = remoteCommitTime
	m.state.UpdateAvailable = available
	m.state.Message = message
	m.state.Error = ""
	m.advanceRevisionLocked()
	state := m.state
	m.mu.Unlock()
	m.publish(state)
}

func (m *gameManager) update(ctx context.Context) error {
	m.logger.Info("opening managed game working tree for forced synchronization", "source", m.paths.Source)
	repository, err := git.PlainOpen(m.paths.Source)
	if err != nil {
		return m.replaceWithLatestAndActivate(ctx, fmt.Errorf("open game repository: %w", err))
	}
	m.ensureManagedGameExclude(m.paths.Source)
	remote, remoteObject, err := m.fetchOfficialMain(ctx, repository, "sync", "正在下載官方最新版本…")
	if err != nil {
		if isRepositoryStateError(err) {
			return m.replaceWithLatestAndActivate(ctx, err)
		}
		return err
	}
	manifest, err := gameCommitManifest(remoteObject)
	if err != nil {
		return m.replaceWithLatestAndActivate(ctx, repositoryStateErrorf("read official game tree: %w", err))
	}
	worktree, err := repository.Worktree()
	if err != nil {
		return m.replaceWithLatestAndActivate(ctx, fmt.Errorf("open game working tree: %w", err))
	}
	configureManagedGameWorktree(worktree)
	if _, err := repository.Storer.Index(); err != nil {
		return m.replaceWithLatestAndActivate(ctx, fmt.Errorf("read game index: %w", err))
	}
	if _, err := worktree.Status(); err != nil {
		return m.replaceWithLatestAndActivate(ctx, fmt.Errorf("inspect game working tree: %w", err))
	}
	m.updateGitProgress("套用版本", "正在以官方版本取代本機檔案…", -1, m.State().ProgressSeconds, true)
	if err := forceSynchronizeGameTree(repository, worktree, m.paths.Source, remote.Hash(), manifest); err != nil {
		return m.replaceWithLatestAndActivate(ctx, fmt.Errorf("force synchronize game working tree: %w", err))
	}
	m.updateGitProgress("驗證檔案", "正在驗證更新後的遊戲檔案…", -1, m.State().ProgressSeconds, true)
	if err := validateGameRoot(m.paths.Source); err != nil {
		return m.replaceWithLatestAndActivate(ctx, fmt.Errorf("validate updated game: %w", err))
	}
	head, err := repository.Head()
	if err != nil {
		return m.replaceWithLatestAndActivate(ctx, fmt.Errorf("read synchronized revision: %w", err))
	}
	if head.Hash() != remote.Hash() {
		return m.replaceWithLatestAndActivate(ctx, fmt.Errorf("synchronized HEAD %s does not match official revision %s", head.Hash(), remote.Hash()))
	}
	commitTime := remoteObject.Committer.When.Format(time.RFC3339)
	m.logger.Info("forced synchronization completed", "commit", head.Hash().String())
	return m.activate(head.Hash().String(), commitTime, "更新完成；請重新整理或重新開啟瀏覽器頁面")
}

func gameCommitManifest(commit *object.Commit) (map[string]gameTreePathKind, error) {
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	manifest := make(map[string]gameTreePathKind)
	var walk func(*object.Tree, string) error
	walk = func(current *object.Tree, parent string) error {
		for _, entry := range current.Entries {
			name := path.Join(parent, entry.Name)
			switch entry.Mode {
			case filemode.Dir:
				manifest[name] = gameTreeDirectory
				subtree, err := current.Tree(entry.Name)
				if err != nil {
					return fmt.Errorf("read tree %q: %w", name, err)
				}
				if err := walk(subtree, name); err != nil {
					return err
				}
			case filemode.Submodule:
				return fmt.Errorf("unsupported game submodule %q", name)
			default:
				if !entry.Mode.IsFile() {
					return fmt.Errorf("unsupported game file mode %s for %q", entry.Mode, name)
				}
				manifest[name] = gameTreeFile
			}
		}
		return nil
	}
	if err := walk(tree, ""); err != nil {
		return nil, err
	}
	if kind, exists := manifest["index.html"]; !exists || kind != gameTreeFile {
		return nil, errors.New("official commit is missing index.html")
	}
	for _, name := range []string{"assets", "css", "js"} {
		if manifest[name] != gameTreeDirectory {
			return nil, fmt.Errorf("official commit is missing required directory %q", name)
		}
	}
	return manifest, nil
}

func forceSynchronizeGameTree(repository *git.Repository, worktree *git.Worktree, root string, target plumbing.Hash, manifest map[string]gameTreePathKind) error {
	return forceSynchronizeGameTreeWithRemover(repository, worktree, root, target, manifest, os.RemoveAll)
}

func forceSynchronizeGameTreeWithRemover(repository *git.Repository, worktree *git.Worktree, root string, target plumbing.Hash, manifest map[string]gameTreePathKind, removePath func(string) error) error {
	if err := cleanGameTreeToManifestWithRemover(root, manifest, removePath); err != nil {
		return fmt.Errorf("remove non-official files: %w", err)
	}
	if err := repository.Storer.SetReference(plumbing.NewHashReference(gameBranchReference, target)); err != nil {
		return fmt.Errorf("set local main revision: %w", err)
	}
	if err := repository.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, gameBranchReference)); err != nil {
		return fmt.Errorf("attach HEAD to main: %w", err)
	}
	if err := replaceGameIndexWithCommit(repository, target); err != nil {
		return fmt.Errorf("reset index to official revision: %w", err)
	}
	// go-git's unrestricted hard reset also removes untracked files without
	// applying excludes. Restrict the worktree reset to official files so a
	// Finder-created .DS_Store that cleanup could not remove cannot fail the
	// synchronization. Rebuilding the index above discards every staged change
	// and clears flags such as skip-worktree before the scoped hard reset.
	if err := worktree.Reset(&git.ResetOptions{
		Mode:   git.HardReset,
		Commit: target,
		Files:  gameManifestFiles(manifest),
	}); err != nil {
		return fmt.Errorf("hard reset official files: %w", err)
	}
	if err := cleanGameTreeToManifestWithRemover(root, manifest, removePath); err != nil {
		return fmt.Errorf("remove files created during synchronization: %w", err)
	}
	return validateSynchronizedGameTree(repository, worktree, root, manifest)
}

func replaceGameIndexWithCommit(repository *git.Repository, target plumbing.Hash) error {
	current, err := repository.Storer.Index()
	if err != nil {
		return err
	}
	commit, err := repository.CommitObject(target)
	if err != nil {
		return err
	}
	tree, err := commit.Tree()
	if err != nil {
		return err
	}
	targetIndex := &gitindex.Index{Version: current.Version}
	if err := tree.Files().ForEach(func(file *object.File) error {
		targetIndex.Entries = append(targetIndex.Entries, &gitindex.Entry{
			Hash: file.Hash,
			Name: file.Name,
			Mode: file.Mode,
		})
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(targetIndex.Entries, func(left, right int) bool {
		return targetIndex.Entries[left].Name < targetIndex.Entries[right].Name
	})
	return repository.Storer.SetIndex(targetIndex)
}

func cleanGameTreeToManifest(root string, manifest map[string]gameTreePathKind) error {
	return cleanGameTreeToManifestWithRemover(root, manifest, os.RemoveAll)
}

func cleanGameTreeToManifestWithRemover(root string, manifest map[string]gameTreePathKind, removePath func(string) error) error {
	return walkManagedGameTree(root, "", func(relative, absolute string, info os.FileInfo) (bool, error) {
		expected, exists := manifest[relative]
		if !exists || !gameTreePathKindMatches(expected, info) {
			if err := removePath(absolute); err != nil {
				if !exists && isAllowedFinderMetadata(relative, info) {
					return false, nil
				}
				return false, err
			}
			return false, nil
		}
		return expected == gameTreeDirectory, nil
	})
}

func gameManifestFiles(manifest map[string]gameTreePathKind) []string {
	files := make([]string, 0, len(manifest))
	for relative, kind := range manifest {
		if kind == gameTreeFile {
			files = append(files, relative)
		}
	}
	sort.Strings(files)
	return files
}

func validateGameTreeManifest(root string, manifest map[string]gameTreePathKind, tracked map[string]struct{}) error {
	seen := make(map[string]struct{}, len(manifest))
	err := walkManagedGameTree(root, "", func(relative, _ string, info os.FileInfo) (bool, error) {
		expected, exists := manifest[relative]
		if !exists {
			_, isTracked := tracked[relative]
			if !isTracked && isAllowedFinderMetadata(relative, info) {
				return false, nil
			}
			return false, fmt.Errorf("non-official path remains after synchronization: %q", relative)
		}
		if !gameTreePathKindMatches(expected, info) {
			return false, fmt.Errorf("path type differs from official version: %q", relative)
		}
		seen[relative] = struct{}{}
		return expected == gameTreeDirectory, nil
	})
	if err != nil {
		return err
	}
	for relative := range manifest {
		if _, exists := seen[relative]; !exists {
			return fmt.Errorf("official path is missing after synchronization: %q", relative)
		}
	}
	return nil
}

func validateSynchronizedGameTree(repository *git.Repository, worktree *git.Worktree, root string, manifest map[string]gameTreePathKind) error {
	index, err := repository.Storer.Index()
	if err != nil {
		return fmt.Errorf("read synchronized index: %w", err)
	}
	tracked := make(map[string]struct{}, len(index.Entries))
	for _, entry := range index.Entries {
		tracked[filepath.ToSlash(entry.Name)] = struct{}{}
	}
	status, err := worktree.Status()
	if err != nil {
		return fmt.Errorf("inspect synchronized working tree: %w", err)
	}
	for relative, fileStatus := range status {
		if fileStatus.Staging == git.Unmodified && fileStatus.Worktree == git.Unmodified {
			continue
		}
		normalized := filepath.ToSlash(relative)
		_, official := manifest[normalized]
		_, isTracked := tracked[normalized]
		if !official && !isTracked && fileStatus.Staging == git.Untracked && fileStatus.Worktree == git.Untracked && path.Base(normalized) == finderMetadata {
			info, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(normalized)))
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr == nil && isAllowedFinderMetadata(normalized, info) {
				continue
			}
		}
		return fmt.Errorf("synchronized working tree is not clean: %s", status.String())
	}
	return validateGameTreeManifest(root, manifest, tracked)
}

func isAllowedFinderMetadata(relative string, info os.FileInfo) bool {
	return path.Base(filepath.ToSlash(relative)) == finderMetadata && info.Mode().IsRegular()
}

func walkManagedGameTree(root, relative string, visit func(string, string, os.FileInfo) (bool, error)) error {
	directory := root
	if relative != "" {
		directory = filepath.Join(root, filepath.FromSlash(relative))
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		childRelative := path.Join(relative, entry.Name())
		if childRelative == ".git" {
			continue
		}
		absolute := filepath.Join(root, filepath.FromSlash(childRelative))
		info, err := os.Lstat(absolute)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		recurse, err := visit(childRelative, absolute, info)
		if err != nil {
			return err
		}
		if recurse {
			if err := walkManagedGameTree(root, childRelative, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func gameTreePathKindMatches(expected gameTreePathKind, info os.FileInfo) bool {
	if expected == gameTreeDirectory {
		return info.IsDir()
	}
	return info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0
}

func (m *gameManager) ensureManagedGameExclude(root string) {
	if err := ensureGitInfoExclude(root); err != nil {
		m.logger.Warn("could not configure local Finder metadata exclusion; launcher validation remains tolerant", "root", root, "error", err)
	}
}

// go-git's protected worktree filesystem does not expose root .git paths to
// status scans, so mirror the persisted rule into each worktree instance.
func configureManagedGameWorktree(worktree *git.Worktree) {
	worktree.Excludes = append(worktree.Excludes, gitignore.ParsePattern(finderMetadata, nil))
}

func ensureGitInfoExclude(root string) error {
	gameRoot, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open game root: %w", err)
	}
	defer gameRoot.Close()
	gitDirectory, err := openVerifiedSubdirectory(gameRoot, ".git", "Git directory")
	if err != nil {
		return err
	}
	defer gitDirectory.Close()
	info, err := gitDirectory.Lstat("info")
	if errors.Is(err, os.ErrNotExist) {
		if err := gitDirectory.Mkdir("info", 0o755); err != nil {
			return fmt.Errorf("create Git info directory: %w", err)
		}
		info, err = gitDirectory.Lstat("info")
		if err != nil {
			return fmt.Errorf("inspect created Git info directory: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect Git info directory: %w", err)
	}
	infoDirectory, err := openVerifiedSubdirectoryWithInfo(gitDirectory, "info", "Git info path", info)
	if err != nil {
		return err
	}
	defer infoDirectory.Close()
	excludeInfo, err := infoDirectory.Lstat("exclude")
	if err == nil && !excludeInfo.Mode().IsRegular() {
		return errors.New("Git info exclude is not a regular file")
	}
	excludeMissing := errors.Is(err, os.ErrNotExist)
	if err != nil && !excludeMissing {
		return fmt.Errorf("inspect Git info exclude: %w", err)
	}
	flags := os.O_RDWR | os.O_APPEND
	if excludeMissing {
		flags |= os.O_CREATE | os.O_EXCL
	}
	file, err := infoDirectory.OpenFile("exclude", flags, 0o644)
	if err != nil {
		return fmt.Errorf("open Git info exclude: %w", err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("inspect opened Git info exclude: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || (!excludeMissing && !os.SameFile(excludeInfo, openedInfo)) {
		_ = file.Close()
		return errors.New("Git info exclude changed while it was being opened")
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("read Git info exclude: %w", err)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.TrimSuffix(line, "\r") == finderMetadata {
			if err := file.Close(); err != nil {
				return fmt.Errorf("close Git info exclude: %w", err)
			}
			return nil
		}
	}
	addition := finderMetadata + "\n"
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		addition = "\n" + addition
	}
	written, err := file.WriteString(addition)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("append Git info exclude: %w", err)
	}
	if written != len(addition) {
		_ = file.Close()
		return fmt.Errorf("append Git info exclude: wrote %d of %d bytes", written, len(addition))
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close Git info exclude: %w", err)
	}
	return nil
}

func openVerifiedSubdirectory(parent *os.Root, name, description string) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", description, err)
	}
	return openVerifiedSubdirectoryWithInfo(parent, name, description, info)
}

func openVerifiedSubdirectoryWithInfo(parent *os.Root, name, description string, expected os.FileInfo) (*os.Root, error) {
	if !expected.IsDir() {
		return nil, fmt.Errorf("%s is not a real directory", description)
	}
	directory, err := parent.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", description, err)
	}
	opened, err := directory.Stat(".")
	if err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("inspect opened %s: %w", description, err)
	}
	if !os.SameFile(expected, opened) {
		_ = directory.Close()
		return nil, fmt.Errorf("%s changed while it was being opened", description)
	}
	return directory, nil
}

func (m *gameManager) replaceSource(staging string) error {
	m.logger.Info("replacing active game source", "staging", staging, "destination", m.paths.Source)
	backup := filepath.Join(m.paths.Staging, ".previous-game")
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("clean previous game backup: %w", err)
	}

	_, sourceErr := os.Lstat(m.paths.Source)
	hadSource := sourceErr == nil
	if sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist) {
		return fmt.Errorf("inspect current game source: %w", sourceErr)
	}
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
		m.logger.Warn("new game version is active but previous backup cleanup is deferred", "backup", backup, "error", err)
	}
	m.logger.Info("game source replacement completed", "destination", m.paths.Source)
	return nil
}

func (m *gameManager) activate(sha, commitTime, message string) error {
	m.logger.Info("activating game revision", "commit", sha)
	m.mu.Lock()
	m.activeRoot = m.paths.Source
	m.state = GameState{
		Status:           StatusReady,
		Commit:           sha,
		CommitTime:       commitTime,
		RemoteCommit:     sha,
		RemoteCommitTime: commitTime,
		Message:          message,
	}
	m.advanceRevisionLocked()
	state := m.state
	m.mu.Unlock()
	m.publish(state)
	if m.onInstalledChange != nil {
		m.onInstalledChange(true)
	}
	return nil
}

func (m *gameManager) updateGitProgress(phase, text string, percent int, seconds int64, force bool) {
	m.mu.Lock()
	m.state.ProgressPhase = phase
	m.state.ProgressText = text
	m.state.ProgressPercent = percent
	m.state.ProgressSeconds = seconds
	m.advanceRevisionLocked()
	if !force && !m.lastProgress.IsZero() && time.Since(m.lastProgress) < 100*time.Millisecond {
		m.mu.Unlock()
		return
	}
	m.lastProgress = time.Now()
	state := m.state
	m.mu.Unlock()
	m.publish(state)
}

func (m *gameManager) advanceRevisionLocked() {
	m.revision++
	m.state.Revision = m.revision
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

func repositoryCommitTime(repository *git.Repository, hash plumbing.Hash) (string, error) {
	commit, err := repository.CommitObject(hash)
	if err != nil {
		return "", err
	}
	return commit.Committer.When.Format(time.RFC3339), nil
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
