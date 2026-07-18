package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"

	git "github.com/go-git/go-git/v5"
)

const movingGameDirectory = ".moving-game"

type storageUnavailableError struct{ err error }

func (e *storageUnavailableError) Error() string { return e.err.Error() }
func (e *storageUnavailableError) Unwrap() error { return e.err }

func isStorageUnavailableError(err error) bool {
	var target *storageUnavailableError
	return errors.As(err, &target)
}

func storageUnavailableErrorf(format string, args ...any) error {
	return &storageUnavailableError{err: fmt.Errorf(format, args...)}
}

type GameFolderInfo struct {
	Root        string `json:"root"`
	GamePath    string `json:"gamePath"`
	DefaultRoot string `json:"defaultRoot"`
	IsDefault   bool   `json:"isDefault"`
}

type GameFolderChangeResult struct {
	Cancelled       bool   `json:"cancelled"`
	Applied         bool   `json:"applied"`
	RequiresMove    bool   `json:"requiresMove"`
	Root            string `json:"root"`
	GamePath        string `json:"gamePath"`
	CurrentGamePath string `json:"currentGamePath"`
}

type gameFolderCoordinator struct {
	mu           sync.Mutex
	lifecycleMu  sync.Mutex
	moveWG       sync.WaitGroup
	shuttingDown bool
	manager      *gameManager
	store        *launcherSettingsStore
	settings     launcherSettings
	defaultRoot  string
	appRoot      string
	logger       *slog.Logger
	stageSource  func(string, string) error
}

func newGameFolderCoordinator(manager *gameManager, store *launcherSettingsStore, settings launcherSettings, appRoot string) *gameFolderCoordinator {
	coordinator := &gameFolderCoordinator{
		manager:     manager,
		store:       store,
		settings:    settings,
		defaultRoot: store.defaultRoot,
		appRoot:     appRoot,
		logger:      slog.Default().With("component", "game_folder"),
		stageSource: os.Rename,
	}
	manager.onInstalledChange = coordinator.recordInstalled
	switch manager.State().Status {
	case StatusReady:
		coordinator.recordInstalled(true)
	case StatusMissing:
		coordinator.recordInstalled(false)
	}
	return coordinator
}

func recoverPendingGameMove(store *launcherSettingsStore, settings launcherSettings, appRoot string) (launcherSettings, error) {
	pending := settings.PendingMove
	if pending == nil {
		return settings, nil
	}
	oldPaths := makeDataPathsForGameRoot(appRoot, pending.FromRoot)
	newPaths := makeDataPathsForGameRoot(appRoot, pending.ToRoot)
	temporary := filepath.Join(newPaths.Staging, movingGameDirectory)
	if _, err := validateGameFolderRoot(oldPaths.GameRoot); err != nil {
		return settings, fmt.Errorf("原遊戲位置目前無法用於搬移復原：%w", err)
	}
	if _, err := validateGameFolderRoot(newPaths.GameRoot); err != nil {
		return settings, fmt.Errorf("新遊戲位置目前無法用於搬移復原：%w", err)
	}
	if pending.Phase == "committed" || sameCleanPath(settings.GameRoot, pending.ToRoot) {
		if exists, _ := pathExists(newPaths.Source); !exists {
			return settings, errors.New("已提交的遊戲搬移缺少新位置資料")
		}
		if err := validateRecoveredGameSource(newPaths.Source, pending.Commit); err != nil {
			return settings, fmt.Errorf("已提交的搬移目的地驗證失敗：%w", err)
		}
		if err := os.RemoveAll(oldPaths.Source); err != nil {
			return settings, fmt.Errorf("清理已完成搬移的舊遊戲失敗：%w", err)
		}
		_ = os.RemoveAll(temporary)
		settings.GameRoot = pending.ToRoot
		settings.LastKnownInstalled = true
		settings.PendingMove = nil
		if err := store.Save(settings); err != nil {
			return settings, err
		}
		return settings, nil
	}

	oldExists, err := pathExists(oldPaths.Source)
	if err != nil {
		return settings, fmt.Errorf("檢查原遊戲位置以進行搬移復原：%w", err)
	}
	if !oldExists {
		candidate := temporary
		if exists, existsErr := pathExists(newPaths.Source); existsErr != nil {
			return settings, fmt.Errorf("檢查新遊戲位置以進行搬移復原：%w", existsErr)
		} else if exists {
			candidate = newPaths.Source
		}
		if exists, existsErr := pathExists(candidate); existsErr != nil {
			return settings, fmt.Errorf("檢查搬移暫存資料以進行復原：%w", existsErr)
		} else if !exists {
			return settings, errors.New("搬移復原找不到原遊戲或目的地暫存資料；將保留復原紀錄後重試")
		}
		if err := validateRecoveredGameSource(candidate, pending.Commit); err != nil {
			return settings, fmt.Errorf("待復原的遊戲資料驗證失敗：%w", err)
		}
		if err := os.MkdirAll(oldPaths.Game, 0o755); err != nil {
			return settings, fmt.Errorf("prepare interrupted move rollback: %w", err)
		}
		if err := os.Rename(candidate, oldPaths.Source); err != nil {
			return settings, fmt.Errorf("rollback interrupted game move: %w", err)
		}
	} else {
		if err := validateRecoveredGameSource(oldPaths.Source, pending.Commit); err != nil {
			return settings, fmt.Errorf("原遊戲位置已有無法辨識的內容；將保留搬移復原紀錄：%w", err)
		}
		_ = os.RemoveAll(newPaths.Source)
		_ = os.RemoveAll(temporary)
	}
	settings.GameRoot = pending.FromRoot
	settings.PendingMove = nil
	if err := store.Save(settings); err != nil {
		return settings, err
	}
	return settings, nil
}

func validateRecoveredGameSource(source, expectedCommit string) error {
	state, _, err := inspectGameSource(source, slog.Default())
	if err != nil {
		return err
	}
	if expectedCommit != "" && state.Commit != expectedCommit {
		return fmt.Errorf("遊戲版本 %s 與搬移紀錄 %s 不一致", state.Commit, expectedCommit)
	}
	return nil
}

func (coordinator *gameFolderCoordinator) Info() GameFolderInfo {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.infoLocked()
}

func (coordinator *gameFolderCoordinator) infoLocked() GameFolderInfo {
	paths := makeDataPathsForGameRoot(coordinator.appRoot, coordinator.settings.GameRoot)
	return GameFolderInfo{
		Root:        coordinator.settings.GameRoot,
		GamePath:    paths.Source,
		DefaultRoot: coordinator.defaultRoot,
		IsDefault:   sameCleanPath(coordinator.settings.GameRoot, coordinator.defaultRoot),
	}
}

func (coordinator *gameFolderCoordinator) RequestChange(candidate string) (GameFolderChangeResult, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()

	root, err := validateGameFolderRoot(candidate)
	if err != nil {
		return GameFolderChangeResult{}, err
	}
	currentPaths := makeDataPathsForGameRoot(coordinator.appRoot, coordinator.settings.GameRoot)
	newPaths := makeDataPathsForGameRoot(coordinator.appRoot, root)
	result := GameFolderChangeResult{
		Root:            root,
		GamePath:        newPaths.Source,
		CurrentGamePath: currentPaths.Source,
	}
	if sameCleanPath(root, coordinator.settings.GameRoot) {
		return GameFolderChangeResult{}, errors.New("所選位置與目前遊戲資料夾相同")
	}
	if err := validateGameFolderRelationship(currentPaths.Source, newPaths.Source); err != nil {
		return GameFolderChangeResult{}, err
	}

	installed, running, activeRoot := coordinator.manager.folderChangeSnapshot()
	if running {
		return GameFolderChangeResult{}, errors.New("請等待目前的遊戲作業完成後再變更資料夾")
	}
	installed = installed || coordinator.settings.LastKnownInstalled
	targetExists, err := pathExists(newPaths.Source)
	if err != nil {
		return GameFolderChangeResult{}, fmt.Errorf("檢查新遊戲位置失敗：%w", err)
	}
	if installed {
		if activeRoot == "" {
			return GameFolderChangeResult{}, errors.New("目前無法存取原遊戲資料夾，請恢復原位置後再變更")
		}
		if _, err := validateGameFolderRoot(currentPaths.GameRoot); err != nil {
			return GameFolderChangeResult{}, errors.New("目前無法存取原遊戲資料夾，請恢復原位置後再變更")
		}
		if _, _, err := inspectGameSource(currentPaths.Source, coordinator.logger); err != nil {
			return GameFolderChangeResult{}, fmt.Errorf("目前的遊戲資料夾無法搬移：%w", err)
		}
		if targetExists {
			if sameCleanPath(root, coordinator.defaultRoot) {
				return GameFolderChangeResult{}, errors.New("預設位置已有遊戲，請先自行清理後再重試")
			}
			return GameFolderChangeResult{}, errors.New("新位置已存在遊戲，無法搬移至此")
		}
		result.RequiresMove = true
		return result, nil
	}

	state := GameState{Status: StatusMissing, Message: "尚未下載遊戲"}
	active := ""
	if targetExists {
		state, active, err = inspectGameSource(newPaths.Source, coordinator.logger)
		if err != nil {
			return GameFolderChangeResult{}, fmt.Errorf("新位置已有無法辨識的遊戲內容：%w", err)
		}
	}
	updated := coordinator.settings
	updated.GameRoot = root
	updated.LastKnownInstalled = active != ""
	updated.PendingMove = nil
	if err := coordinator.manager.commitFolderPaths(newPaths, state, active, func() error {
		return coordinator.store.Save(updated)
	}); err != nil {
		return GameFolderChangeResult{}, fmt.Errorf("儲存遊戲資料夾設定失敗：%w", err)
	}
	coordinator.settings = updated
	result.Applied = true
	return result, nil
}

func (coordinator *gameFolderCoordinator) ConfirmMove(candidate string) error {
	coordinator.lifecycleMu.Lock()
	if coordinator.shuttingDown {
		coordinator.lifecycleMu.Unlock()
		return errors.New("啟動器正在關閉，無法開始搬移遊戲")
	}
	coordinator.moveWG.Add(1)
	coordinator.lifecycleMu.Unlock()
	defer coordinator.moveWG.Done()

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()

	root, err := validateGameFolderRoot(candidate)
	if err != nil {
		return err
	}
	if sameCleanPath(root, coordinator.settings.GameRoot) {
		return nil
	}
	oldPaths := makeDataPathsForGameRoot(coordinator.appRoot, coordinator.settings.GameRoot)
	newPaths := makeDataPathsForGameRoot(coordinator.appRoot, root)
	if err := validateGameFolderRelationship(oldPaths.Source, newPaths.Source); err != nil {
		return err
	}
	if exists, err := pathExists(newPaths.Source); err != nil {
		return fmt.Errorf("檢查新遊戲位置失敗：%w", err)
	} else if exists {
		if sameCleanPath(root, coordinator.defaultRoot) {
			return errors.New("預設位置已有遊戲，請先自行清理後再重試")
		}
		return errors.New("新位置已存在遊戲，無法搬移至此")
	}
	if _, err := validateGameFolderRoot(oldPaths.GameRoot); err != nil {
		return errors.New("目前無法存取原遊戲資料夾，請恢復原位置後再變更")
	}
	sourceState, _, err := inspectGameSource(oldPaths.Source, coordinator.logger)
	if err != nil {
		return fmt.Errorf("目前的遊戲資料夾無法搬移：%w", err)
	}
	if current := coordinator.manager.State(); current.Commit != "" && sourceState.Commit != current.Commit {
		return errors.New("目前遊戲版本已變更，請重新選擇資料夾")
	}

	previousState, err := coordinator.manager.beginFolderMove(oldPaths)
	if err != nil {
		return err
	}
	moveErr := coordinator.moveInstallation(oldPaths, newPaths, previousState)
	if moveErr != nil {
		coordinator.manager.finishFolderMoveFailure(previousState)
		return moveErr
	}
	coordinator.manager.finishFolderMoveSuccess(newPaths, previousState)
	return nil
}

func (coordinator *gameFolderCoordinator) Shutdown() {
	coordinator.lifecycleMu.Lock()
	coordinator.shuttingDown = true
	coordinator.lifecycleMu.Unlock()
	coordinator.moveWG.Wait()
}

func (coordinator *gameFolderCoordinator) Recheck() error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if _, running, _ := coordinator.manager.folderChangeSnapshot(); running {
		return errors.New("請等待目前的遊戲作業完成後再重新檢查")
	}
	paths := makeDataPathsForGameRoot(coordinator.appRoot, coordinator.settings.GameRoot)
	state, active, err := inspectGameInstallation(paths, coordinator.logger)
	if err != nil {
		coordinator.manager.applyFolderInspectionError(paths, err)
		return err
	}
	updated := coordinator.settings
	updated.LastKnownInstalled = active != ""
	if err := coordinator.manager.commitFolderPaths(paths, state, active, func() error {
		return coordinator.store.Save(updated)
	}); err != nil {
		return fmt.Errorf("儲存遊戲資料夾狀態失敗：%w", err)
	}
	coordinator.settings = updated
	return nil
}

func (coordinator *gameFolderCoordinator) recordInstalled(installed bool) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.settings.LastKnownInstalled == installed {
		return
	}
	updated := coordinator.settings
	updated.LastKnownInstalled = installed
	if err := coordinator.store.Save(updated); err != nil {
		coordinator.logger.Error("persist last known installation state", "installed", installed, "error", err)
		return
	}
	coordinator.settings = updated
}

func (coordinator *gameFolderCoordinator) moveInstallation(oldPaths, newPaths dataPaths, previous GameState) error {
	updated := coordinator.settings
	updated.PendingMove = &pendingGameMove{FromRoot: oldPaths.GameRoot, ToRoot: newPaths.GameRoot, Phase: "prepared", Commit: previous.Commit}
	if err := coordinator.store.Save(updated); err != nil {
		return fmt.Errorf("準備搬移紀錄失敗：%w", err)
	}
	coordinator.settings = updated
	if err := os.MkdirAll(newPaths.Staging, 0o755); err != nil {
		coordinator.clearPendingMove(oldPaths.GameRoot)
		return fmt.Errorf("建立新位置暫存資料夾失敗：%w", err)
	}
	temporary := filepath.Join(newPaths.Staging, movingGameDirectory)
	if err := os.RemoveAll(temporary); err != nil {
		coordinator.clearPendingMove(oldPaths.GameRoot)
		return fmt.Errorf("清理搬移暫存資料失敗：%w", err)
	}

	renamed := false
	if err := coordinator.stageSource(oldPaths.Source, temporary); err == nil {
		renamed = true
		coordinator.manager.updateFolderMoveProgress("搬移遊戲", "正在移動遊戲檔案…", -1)
	} else if errors.Is(err, syscall.EXDEV) {
		if err := copyGameTree(oldPaths.Source, temporary, coordinator.manager.updateFolderMoveProgress); err != nil {
			_ = os.RemoveAll(temporary)
			coordinator.clearPendingMove(oldPaths.GameRoot)
			return fmt.Errorf("跨磁碟複製遊戲失敗：%w", err)
		}
	} else {
		coordinator.clearPendingMove(oldPaths.GameRoot)
		return fmt.Errorf("移動遊戲資料夾失敗：%w", err)
	}

	rollback := func() error {
		if renamed {
			candidate := temporary
			if exists, err := pathExists(newPaths.Source); err != nil {
				return fmt.Errorf("檢查已啟用的搬移目的地：%w", err)
			} else if exists {
				candidate = newPaths.Source
			}
			if exists, err := pathExists(candidate); err != nil {
				return fmt.Errorf("檢查待復原的遊戲資料：%w", err)
			} else if !exists {
				return errors.New("找不到可搬回原位置的遊戲資料")
			}
			if err := os.Rename(candidate, oldPaths.Source); err != nil {
				return fmt.Errorf("將遊戲資料搬回原位置：%w", err)
			}
		} else {
			if err := os.RemoveAll(newPaths.Source); err != nil {
				return fmt.Errorf("清理失敗的搬移目的地：%w", err)
			}
			if err := os.RemoveAll(temporary); err != nil {
				return fmt.Errorf("清理失敗的搬移暫存資料：%w", err)
			}
		}
		coordinator.clearPendingMove(oldPaths.GameRoot)
		return nil
	}

	coordinator.manager.updateFolderMoveProgress("驗證遊戲", "正在驗證搬移後的遊戲…", -1)
	state, _, err := inspectGameSource(temporary, coordinator.logger)
	if err != nil || state.Commit != previous.Commit {
		rollbackErr := rollback()
		if rollbackErr != nil {
			return fmt.Errorf("驗證搬移後的遊戲失敗，且無法立即復原（下次啟動將重試）：%w", rollbackErr)
		}
		if err != nil {
			return fmt.Errorf("驗證搬移後的遊戲失敗：%w", err)
		}
		return errors.New("搬移後的遊戲版本與原版本不一致")
	}
	if err := os.Rename(temporary, newPaths.Source); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return fmt.Errorf("啟用新遊戲位置失敗，且無法立即復原（下次啟動將重試）：%w", rollbackErr)
		}
		return fmt.Errorf("啟用新遊戲位置失敗：%w", err)
	}

	updated.GameRoot = newPaths.GameRoot
	updated.LastKnownInstalled = true
	updated.PendingMove.Phase = "committed"
	if err := coordinator.store.Save(updated); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return fmt.Errorf("儲存新遊戲位置失敗，且無法立即復原（下次啟動將重試）：%w", rollbackErr)
		}
		return fmt.Errorf("儲存新遊戲位置失敗：%w", err)
	}
	coordinator.settings = updated
	if !renamed {
		if err := os.RemoveAll(oldPaths.Source); err != nil {
			coordinator.logger.Warn("new game location committed but old source cleanup is deferred", "source", oldPaths.Source, "error", err)
			return nil
		}
	}
	updated.PendingMove = nil
	if err := coordinator.store.Save(updated); err != nil {
		coordinator.logger.Warn("could not clear completed game move record", "error", err)
		return nil
	}
	coordinator.settings = updated
	return nil
}

func (coordinator *gameFolderCoordinator) clearPendingMove(root string) {
	updated := coordinator.settings
	updated.GameRoot = root
	updated.PendingMove = nil
	if err := coordinator.store.Save(updated); err != nil {
		coordinator.logger.Error("clear failed game move record", "error", err)
		return
	}
	coordinator.settings = updated
}

func validateGameFolderRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("遊戲資料夾路徑不可為空")
	}
	absolute, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("解析遊戲資料夾路徑失敗：%w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", storageUnavailableErrorf("找不到所選的遊戲資料夾")
		}
		return "", storageUnavailableErrorf("無法解析所選的遊戲資料夾：%v", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", storageUnavailableErrorf("無法讀取所選的遊戲資料夾：%v", err)
	}
	if !info.IsDir() {
		return "", errors.New("所選路徑不是資料夾")
	}
	if err := probeWritableDirectory(resolved); err != nil {
		return "", storageUnavailableErrorf("所選資料夾沒有完整的讀寫權限：%v", err)
	}
	game := filepath.Join(resolved, "game")
	gameInfo, err := os.Stat(game)
	if err == nil {
		if !gameInfo.IsDir() {
			return "", errors.New("所選位置中的 game 不是資料夾")
		}
		if err := probeWritableDirectory(game); err != nil {
			return "", storageUnavailableErrorf("所選位置中的 game 沒有完整的讀寫權限：%v", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", storageUnavailableErrorf("無法檢查所選位置中的 game：%v", err)
	}
	return resolved, nil
}

func probeWritableDirectory(directory string) error {
	if _, err := os.ReadDir(directory); err != nil {
		return fmt.Errorf("read directory: %w", err)
	}
	probe, err := os.CreateTemp(directory, ".idle-lineage-write-test-*")
	if err != nil {
		return fmt.Errorf("create test file: %w", err)
	}
	name := probe.Name()
	defer os.Remove(name)
	if _, err := probe.WriteString("idle-lineage-launcher"); err != nil {
		_ = probe.Close()
		return fmt.Errorf("write test file: %w", err)
	}
	if err := probe.Sync(); err != nil {
		_ = probe.Close()
		return fmt.Errorf("flush test file: %w", err)
	}
	if err := probe.Close(); err != nil {
		return fmt.Errorf("close test file: %w", err)
	}
	if _, err := os.ReadFile(name); err != nil {
		return fmt.Errorf("read test file: %w", err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("remove test file: %w", err)
	}
	return nil
}

func inspectGameInstallation(paths dataPaths, logger *slog.Logger) (GameState, string, error) {
	if _, err := validateGameFolderRoot(paths.GameRoot); err != nil {
		return GameState{}, "", err
	}
	if entries, err := os.ReadDir(paths.Staging); err == nil {
		for _, entry := range entries {
			if err := os.RemoveAll(filepath.Join(paths.Staging, entry.Name())); err != nil {
				return GameState{}, "", fmt.Errorf("remove stale staging data: %w", err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return GameState{}, "", fmt.Errorf("read staging directory: %w", err)
	}
	exists, err := pathExists(paths.Source)
	if err != nil {
		return GameState{}, "", fmt.Errorf("inspect installed game directory: %w", err)
	}
	if !exists {
		return GameState{Status: StatusMissing, Message: "尚未下載遊戲"}, "", nil
	}
	return inspectGameSource(paths.Source, logger)
}

func inspectGameSource(source string, logger *slog.Logger) (GameState, string, error) {
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return GameState{}, "", fmt.Errorf("inspect game directory: %w", err)
	}
	if !sourceInfo.IsDir() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return GameState{}, "", errors.New("game directory is not a real directory")
	}
	gitInfo, err := os.Lstat(filepath.Join(source, ".git"))
	if err != nil {
		return GameState{}, "", fmt.Errorf("inspect game Git directory: %w", err)
	}
	if !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return GameState{}, "", errors.New("game Git metadata is not a real directory")
	}
	repository, err := git.PlainOpen(source)
	if err != nil {
		return GameState{}, "", fmt.Errorf("game directory exists but is not a valid Git working tree: %w", err)
	}
	if err := ensureGitInfoExclude(source); err != nil {
		logger.Warn("could not configure local Finder metadata exclusion", "root", source, "error", err)
	}
	if err := validateGameRoot(source); err != nil {
		return GameState{}, "", fmt.Errorf("validate installed game: %w", err)
	}
	head, err := repository.Head()
	if err != nil {
		return GameState{}, "", fmt.Errorf("read installed Git revision: %w", err)
	}
	commitTime, err := repositoryCommitTime(repository, head.Hash())
	if err != nil {
		return GameState{}, "", fmt.Errorf("read installed Git commit time: %w", err)
	}
	state := GameState{
		Status:           StatusReady,
		Commit:           head.Hash().String(),
		CommitTime:       commitTime,
		RemoteCommit:     head.Hash().String(),
		RemoteCommitTime: commitTime,
		Message:          "遊戲已可離線使用",
	}
	return state, source, nil
}

func validateGameFolderRelationship(oldSource, newSource string) error {
	oldAbsolute := comparablePath(oldSource)
	newAbsolute := comparablePath(newSource)
	if sameCleanPath(oldAbsolute, newAbsolute) {
		return errors.New("新舊遊戲位置相同")
	}
	if pathContains(oldAbsolute, newAbsolute) || pathContains(newAbsolute, oldAbsolute) {
		return errors.New("新舊遊戲位置不可互相包含")
	}
	return nil
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func sameCleanPath(left, right string) bool {
	left = comparablePath(left)
	right = comparablePath(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// comparablePath resolves symlinks and junctions even when the final path does
// not exist yet by resolving the deepest existing parent and appending the
// missing suffix. It is intentionally read-only; permission probing belongs to
// validateGameFolderRoot.
func comparablePath(path string) string {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return filepath.Clean(path)
	}
	current := absolute
	var suffix []string
	for {
		resolved, resolveErr := filepath.EvalSymlinks(current)
		if resolveErr == nil {
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved)
		}
		if !errors.Is(resolveErr, os.ErrNotExist) {
			return absolute
		}
		parent := filepath.Dir(current)
		if parent == current {
			return absolute
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

type moveProgress func(phase, text string, percent int)

func copyGameTree(source, destination string, progress moveProgress) error {
	progress("掃描遊戲", "正在計算需要搬移的遊戲檔案…", -1)
	var total int64
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	}); err != nil {
		return err
	}
	var copied int64
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode().IsRegular():
			if err := copyRegularFile(path, target, info.Mode().Perm(), func(delta int64) {
				copied += delta
				percent := -1
				if total > 0 {
					percent = int(copied * 100 / total)
				}
				progress("複製遊戲", "正在跨磁碟複製遊戲檔案…", percent)
			}); err != nil {
				return err
			}
			return nil
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			return fmt.Errorf("unsupported game path type: %s", path)
		}
	})
	if err != nil {
		return err
	}
	progress("複製遊戲", "已完成跨磁碟遊戲檔案複製", 100)
	return nil
}

func copyRegularFile(source, destination string, mode os.FileMode, progress func(int64)) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, &progressReader{reader: input, progress: progress})
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written < 0 {
		return errors.New("invalid copied byte count")
	}
	return nil
}

type progressReader struct {
	reader   io.Reader
	progress func(int64)
}

func (reader *progressReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	if count > 0 {
		reader.progress(int64(count))
	}
	return count, err
}
