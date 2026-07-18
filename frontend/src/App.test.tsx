import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { toast } from 'sonner';
import App from './App';
import { useGameLaunchConfigStore } from './stores/useGameLaunchConfigStore';

const mocks = vi.hoisted(() => {
  const listeners = new Map<string, Set<(event: { data: unknown }) => void>>();
  return {
    listeners,
    getGameBrowsers: vi.fn(),
    getGameState: vi.fn(),
    getGameFolderInfo: vi.fn(),
    getLauncherInfo: vi.fn(),
    startInstall: vi.fn(),
    checkForUpdate: vi.fn(),
    startUpdate: vi.fn(),
    cancelUpdate: vi.fn(),
    cancelUpdateAndClose: vi.fn(),
    retryUpdateRecovery: vi.fn(),
    cancelInstall: vi.fn(),
    launchGame: vi.fn(),
    openGameFolder: vi.fn(),
    openGameRepository: vi.fn(),
    openLauncherRepository: vi.fn(),
    selectGameFolder: vi.fn(),
    restoreDefaultGameFolder: vi.fn(),
    confirmGameFolderMove: vi.fn(),
    recheckGameFolder: vi.fn(),
    fetchLatestRelease: vi.fn(),
    unsubscribe: vi.fn(),
  };
});

vi.mock('@wailsio/runtime', () => ({
  Events: {
    On: (name: string, callback: (event: { data: unknown }) => void) => {
      const callbacks = mocks.listeners.get(name) ?? new Set();
      callbacks.add(callback);
      mocks.listeners.set(name, callbacks);
      return () => {
        callbacks.delete(callback);
        mocks.unsubscribe(name);
      };
    },
  },
}));

vi.mock('../bindings/github.com/SDxBacon/idle-lineage-launcher', () => ({
  GameState: class GameState {
    constructor(source: Record<string, unknown>) {
      Object.assign(this, source);
    }
  },
  GameStatus: {
    StatusMissing: 'missing',
    StatusResolving: 'resolving',
    StatusInstalling: 'installing',
    StatusReady: 'ready',
    StatusChecking: 'checking',
    StatusUpdateAvailable: 'update_available',
    StatusUpdating: 'updating',
    StatusRecovering: 'recovering',
    StatusRecoveryFailed: 'recovery_failed',
    StatusMoving: 'moving',
    StatusStorageUnavailable: 'storage_unavailable',
    StatusCancelled: 'cancelled',
    StatusError: 'error',
  },
  LauncherService: {
    GetGameBrowsers: mocks.getGameBrowsers,
    GetGameState: mocks.getGameState,
    GetGameFolderInfo: mocks.getGameFolderInfo,
    GetLauncherInfo: mocks.getLauncherInfo,
    StartInstall: mocks.startInstall,
    CheckForUpdate: mocks.checkForUpdate,
    StartUpdate: mocks.startUpdate,
    CancelUpdate: mocks.cancelUpdate,
    CancelUpdateAndClose: mocks.cancelUpdateAndClose,
    RetryUpdateRecovery: mocks.retryUpdateRecovery,
    CancelInstall: mocks.cancelInstall,
    LaunchGame: mocks.launchGame,
    OpenGameFolder: mocks.openGameFolder,
    OpenGameRepository: mocks.openGameRepository,
    OpenLauncherRepository: mocks.openLauncherRepository,
    SelectGameFolder: mocks.selectGameFolder,
    RestoreDefaultGameFolder: mocks.restoreDefaultGameFolder,
    ConfirmGameFolderMove: mocks.confirmGameFolderMove,
    RecheckGameFolder: mocks.recheckGameFolder,
  },
}));

type State = {
  revision: number;
  status: string;
  commit: string;
  commitTime: string;
  remoteCommit: string;
  remoteCommitTime: string;
  updateAvailable: boolean;
  progressPhase: string;
  progressText: string;
  progressPercent: number;
  progressSeconds: number;
  progressStep: number;
  progressStepTotal: number;
  progressCancellable: boolean;
  message: string;
  error: string;
};

const state = (overrides: Partial<State> = {}): State => ({
  revision: 1,
  status: 'missing',
  commit: '',
  commitTime: '',
  remoteCommit: '',
  remoteCommitTime: '',
  updateAvailable: false,
  progressPhase: '',
  progressText: '',
  progressPercent: -1,
  progressSeconds: 0,
  progressStep: 0,
  progressStepTotal: 0,
  progressCancellable: false,
  message: '尚未下載遊戲',
  error: '',
  ...overrides,
});

function emit(name: string, data: unknown = {}) {
  act(() => {
    mocks.listeners.get(name)?.forEach(callback => callback({ data }));
  });
}

function releaseResponse(tagName: unknown, ok = true) {
  return {
    ok,
    json: vi.fn().mockResolvedValue({ tag_name: tagName }),
  } as unknown as Response;
}

describe('App', () => {
  beforeEach(() => {
    localStorage.clear();
    useGameLaunchConfigStore.setState({ browserID: null });
    toast.dismiss();
    mocks.listeners.clear();
    vi.clearAllMocks();
    mocks.getGameBrowsers.mockResolvedValue([]);
    mocks.getLauncherInfo.mockResolvedValue({
      version: '0.1.0',
      gameRepository: 'shines871/idle-lineage-class',
    });
    mocks.getGameFolderInfo.mockResolvedValue({
      root: '/Users/test/Library/Application Support/IdleLineageLauncher',
      gamePath: '/Users/test/Library/Application Support/IdleLineageLauncher/game/shines871',
      defaultRoot: '/Users/test/Library/Application Support/IdleLineageLauncher',
      isDefault: true,
    });
    mocks.startInstall.mockResolvedValue(undefined);
    mocks.checkForUpdate.mockResolvedValue(undefined);
    mocks.startUpdate.mockResolvedValue(undefined);
    mocks.cancelUpdate.mockResolvedValue(undefined);
    mocks.cancelUpdateAndClose.mockResolvedValue(undefined);
    mocks.retryUpdateRecovery.mockResolvedValue(undefined);
    mocks.cancelInstall.mockResolvedValue(undefined);
    mocks.launchGame.mockResolvedValue({ fallbackToDefault: false });
    mocks.openGameFolder.mockResolvedValue(undefined);
    mocks.openGameRepository.mockResolvedValue(undefined);
    mocks.openLauncherRepository.mockResolvedValue(undefined);
    mocks.selectGameFolder.mockResolvedValue({ cancelled: true });
    mocks.restoreDefaultGameFolder.mockResolvedValue({ cancelled: true });
    mocks.confirmGameFolderMove.mockResolvedValue(undefined);
    mocks.recheckGameFolder.mockResolvedValue(undefined);
    mocks.fetchLatestRelease.mockResolvedValue(releaseResponse('v0.1.0'));
    vi.stubGlobal('fetch', mocks.fetchLatestRelease);
  });

  afterEach(() => {
    vi.useRealTimers();
    cleanup();
    vi.unstubAllGlobals();
  });

  it('reads local state and launcher info, renders repository links, and downloads after confirmation', async () => {
    mocks.getGameState.mockResolvedValue(state());
    render(<App />);

    expect(await screen.findByRole('heading', { name: '尚未下載遊戲' })).toBeInTheDocument();
    const repositoryBadge = await screen.findByRole('button', {
      name: '在 GitHub 開啟 shines871/idle-lineage-class',
    });
    expect(repositoryBadge).toHaveTextContent('shines871/idle-lineage-class');
    expect(repositoryBadge).not.toHaveTextContent('遊戲來源');
    expect(screen.getByText('遊戲來源')).toHaveClass('status-badge-label');
    expect(repositoryBadge).toHaveClass('status-badge');
    expect(repositoryBadge.className).not.toContain('status-missing');
    expect(screen.getByText('v0.1.0')).toBeInTheDocument();
    expect(screen.getByText('SDxBacon').closest('footer')).toHaveClass('launcher-footer');
    expect(screen.getByText('約 500–800 MB')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '遊戲資料夾' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '開啟啟動器設置頁' })).toBeEnabled();
    expect(mocks.getGameState).toHaveBeenCalledTimes(1);
    expect(mocks.getLauncherInfo).toHaveBeenCalledTimes(1);
    expect(mocks.getGameBrowsers).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(mocks.fetchLatestRelease).toHaveBeenCalledTimes(1));
    expect(screen.queryByTestId('launcher-update-indicator')).not.toBeInTheDocument();
    expect(mocks.startInstall).not.toHaveBeenCalled();
    expect(mocks.checkForUpdate).not.toHaveBeenCalled();

    fireEvent.click(repositoryBadge);
    fireEvent.click(screen.getByRole('button', { name: '在 GitHub 開啟 Idle Lineage Launcher' }));
    fireEvent.click(screen.getByRole('button', { name: '下載遊戲' }));

    expect(mocks.openGameRepository).toHaveBeenCalledTimes(1);
    expect(mocks.openLauncherRepository).toHaveBeenCalledTimes(1);
    expect(mocks.startInstall).toHaveBeenCalledTimes(1);
  });

  it('shows an animated update indicator and tooltip for a newer launcher release', async () => {
    mocks.fetchLatestRelease.mockResolvedValueOnce(releaseResponse('v0.2.0'));
    mocks.getGameState.mockResolvedValue(state());
    render(<App />);

    const githubButton = await screen.findByRole('button', {
      name: '在 GitHub 開啟 Idle Lineage Launcher；有更新版本 v0.2.0 可供下載',
    });
    const indicator = screen.getByTestId('launcher-update-indicator');
    expect(indicator).toHaveAttribute('aria-hidden', 'true');
    expect(indicator).toHaveClass('absolute', '-top-1', '-right-1');
    expect(indicator.parentElement).toHaveClass('relative', 'h-4', 'w-4');
    expect(indicator.querySelector('.animate-ping')).toBeInTheDocument();
    expect(indicator.querySelector('.animate-pulse')).toBeInTheDocument();
    expect(githubButton).toHaveAttribute('data-tooltip-content', '有更新版本 v0.2.0 可供下載');

    fireEvent.mouseEnter(githubButton);
    expect(await screen.findByRole('tooltip')).toHaveTextContent('有更新版本 v0.2.0 可供下載');

    fireEvent.mouseLeave(githubButton);
    await waitFor(() => expect(screen.queryByRole('tooltip')).not.toBeInTheDocument());
    fireEvent.focus(githubButton);
    expect(await screen.findByRole('tooltip')).toHaveTextContent('有更新版本 v0.2.0 可供下載');

    fireEvent.click(githubButton);
    expect(mocks.openLauncherRepository).toHaveBeenCalledTimes(1);
  });

  it('silently ignores launcher release lookup failures', async () => {
    mocks.fetchLatestRelease.mockRejectedValueOnce(new Error('GitHub is unavailable'));
    mocks.getGameState.mockResolvedValue(state());
    render(<App />);

    expect(await screen.findByRole('heading', { name: '尚未下載遊戲' })).toBeInTheDocument();
    await waitFor(() => expect(mocks.fetchLatestRelease).toHaveBeenCalledTimes(1));
    expect(screen.queryByTestId('launcher-update-indicator')).not.toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('keeps game controls usable when launcher metadata cannot be read', async () => {
    mocks.getLauncherInfo.mockRejectedValueOnce(new Error('無法取得 launcher metadata'));
    mocks.getGameState.mockResolvedValue(state());
    render(<App />);

    expect(await screen.findByRole('heading', { name: '尚未下載遊戲' })).toBeInTheDocument();
    expect(screen.getByText('v—')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '正在讀取遊戲 repository' })).toBeDisabled();
    expect(screen.getByRole('alert')).toHaveTextContent('無法取得 launcher metadata');
    expect(mocks.fetchLatestRelease).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: '下載遊戲' }));
    expect(mocks.startInstall).toHaveBeenCalledTimes(1);
  });

  it('reports repository opener failures through the existing inline error', async () => {
    mocks.openGameRepository.mockRejectedValueOnce(new Error('無法開啟 GitHub'));
    mocks.getGameState.mockResolvedValue(state());
    render(<App />);

    fireEvent.click(
      await screen.findByRole('button', {
        name: '在 GitHub 開啟 shines871/idle-lineage-class',
      }),
    );

    expect(await screen.findByRole('alert')).toHaveTextContent('無法開啟 GitHub');
    expect(screen.getByRole('button', { name: '下載遊戲' })).toBeEnabled();
  });

  it('renders install progress and lets the user cancel the download', async () => {
    mocks.getGameState.mockResolvedValue(
      state({
        status: 'installing',
        progressPhase: '接收遊戲檔案',
        progressText: '接收遊戲檔案：50%',
        progressPercent: 50,
        progressSeconds: 8,
        message: '正在下載',
      }),
    );
    render(<App />);

    expect(await screen.findByRole('heading', { name: '正在下載遊戲' })).toBeInTheDocument();
    expect(screen.getByText('50%')).toBeInTheDocument();
    expect(screen.getByText('接收遊戲檔案：50%')).toBeInTheDocument();
    expect(screen.getByText('已執行 8 秒')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '取消下載' }));

    expect(mocks.cancelInstall).toHaveBeenCalledTimes(1);
  });

  it('renders version resolution as cancellable download progress', async () => {
    mocks.getGameState.mockResolvedValue(
      state({
        status: 'resolving',
        progressPercent: -1,
        message: '正在確認官方版本',
      }),
    );
    render(<App />);

    expect(await screen.findByRole('heading', { name: '正在下載遊戲' })).toBeInTheDocument();
    expect(screen.getByRole('progressbar', { name: '確認官方版本' })).not.toHaveAttribute(
      'aria-valuenow',
    );
    expect(screen.getByRole('button', { name: '取消下載' })).toBeEnabled();
  });

  it('keeps a cancelled download uninstalled until the user retries', async () => {
    mocks.getGameState.mockResolvedValue(state({ status: 'cancelled', message: '下載已取消' }));
    render(<App />);

    expect(await screen.findByRole('heading', { name: '尚未下載遊戲' })).toBeInTheDocument();
    expect(screen.getByText('下載已取消')).toBeInTheDocument();
    expect(mocks.startInstall).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: '重試下載' }));

    expect(mocks.startInstall).toHaveBeenCalledTimes(1);
  });

  it('shows a damaged-install error and offers an explicit retry', async () => {
    mocks.getGameState.mockResolvedValue(state({ status: 'error', error: 'repository 已損毀' }));
    render(<App />);

    expect(await screen.findByRole('alert')).toHaveTextContent('repository 已損毀');
    expect(screen.queryByRole('button', { name: '遊戲資料夾' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '重試下載' }));

    expect(mocks.startInstall).toHaveBeenCalledTimes(1);
  });

  it('launches and manually checks updates from the ready dashboard', async () => {
    const localCommitTime = new Date(2026, 0, 2, 3, 4, 5).toISOString();
    const remoteCommitTime = new Date(2026, 5, 7, 8, 9, 10).toISOString();
    mocks.getGameState.mockResolvedValue(
      state({
        status: 'ready',
        commit: '0123456789abcdef',
        commitTime: localCommitTime,
        remoteCommit: 'fedcba9876543210',
        remoteCommitTime,
        message: '遊戲已就緒',
      }),
    );
    render(<App />);

    expect(await screen.findByRole('heading', { name: '遊戲已就緒' })).toBeInTheDocument();
    expect(screen.getByText('2026-01-02 03:04:05')).toHaveClass('version-time');
    expect(screen.getByText('2026-06-07 08:09:10')).toHaveClass('version-time');
    expect(screen.getAllByText('·', { selector: '.version-separator' })).toHaveLength(2);
    expect(document.querySelector('iframe')).not.toBeInTheDocument();
    expect(screen.queryByRole('combobox', { name: '遊戲瀏覽器' })).not.toBeInTheDocument();
    const folderButton = screen.getByRole('button', { name: '遊戲資料夾' });
    const settingsButton = screen.getByRole('button', { name: '開啟啟動器設置頁' });
    expect(settingsButton).toHaveAttribute('title', '開啟啟動器設置頁');
    expect(folderButton.parentElement).toHaveClass('folder-settings-actions');
    expect(folderButton.nextElementSibling).toBe(settingsButton);

    fireEvent.click(screen.getByRole('button', { name: '啟動遊戲' }));
    fireEvent.click(screen.getByRole('button', { name: '檢查更新' }));
    fireEvent.click(folderButton);

    expect(mocks.launchGame).toHaveBeenCalledTimes(1);
    expect(mocks.launchGame).toHaveBeenCalledWith(null);
    expect(screen.queryByText('所選瀏覽器無法開啟遊戲')).not.toBeInTheDocument();
    expect(mocks.checkForUpdate).toHaveBeenCalledTimes(1);
    expect(mocks.startUpdate).not.toHaveBeenCalled();
    expect(mocks.openGameFolder).toHaveBeenCalledTimes(1);
  });

  it('opens the settings view with the shared chrome and returns to the latest dashboard', async () => {
    mocks.getGameBrowsers.mockResolvedValue([{ id: 'browser:chrome', name: 'Google Chrome' }]);
    mocks.getGameState.mockResolvedValue(
      state({
        status: 'ready',
        commit: 'local-commit',
        message: '遊戲已就緒',
      }),
    );
    render(<App />);

    expect(await screen.findByRole('heading', { name: '遊戲已就緒' })).toBeInTheDocument();
    expect(screen.queryByRole('combobox', { name: '遊戲瀏覽器' })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '開啟啟動器設置頁' }));

    expect(screen.getByRole('heading', { name: '啟動器設置頁' })).toBeInTheDocument();
    expect(screen.getByText('IDLE LINEAGE LAUNCHER')).toBeInTheDocument();
    expect(screen.getByText('遊戲來源')).toHaveClass('status-badge-label');
    expect(
      screen.getByRole('button', {
        name: '在 GitHub 開啟 shines871/idle-lineage-class',
      }),
    ).toBeInTheDocument();
    const browserSelect = await screen.findByRole('combobox', { name: '遊戲瀏覽器' });
    expect(browserSelect).toHaveTextContent('系統預設');
    fireEvent.click(browserSelect);
    fireEvent.click(await screen.findByRole('option', { name: 'Google Chrome' }));
    expect(useGameLaunchConfigStore.getState().browserID).toBe('browser:chrome');
    expect(screen.getByRole('heading', { name: '遊戲資料夾', level: 2 })).toBeInTheDocument();
    expect(
      screen.getByDisplayValue('/Users/test/Library/Application Support/IdleLineageLauncher'),
    ).toHaveAttribute('readonly');
    expect(
      screen.getByText(
        '/Users/test/Library/Application Support/IdleLineageLauncher/game/shines871',
      ),
    ).toBeInTheDocument();
    expect(screen.getByText('v0.1.0')).toBeInTheDocument();

    emit(
      'launcher:game-state',
      state({
        revision: 2,
        status: 'update_available',
        commit: 'local-commit',
        remoteCommit: 'remote-commit',
        updateAvailable: true,
        message: '發現新的官方版本',
      }),
    );
    expect(screen.getByRole('heading', { name: '啟動器設置頁' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '有可用更新' })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '返回' }));

    expect(screen.getByRole('heading', { name: '有可用更新' })).toBeInTheDocument();
    expect(screen.queryByRole('combobox', { name: '遊戲瀏覽器' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '立即更新' })).toBeEnabled();
    fireEvent.click(screen.getByRole('button', { name: '啟動遊戲' }));
    expect(mocks.launchGame).toHaveBeenCalledWith('browser:chrome');
  });

  it('keeps repository action errors visible in settings', async () => {
    mocks.openGameRepository.mockRejectedValueOnce(new Error('無法開啟遊戲來源'));
    mocks.getGameState.mockResolvedValue(state({ status: 'ready', commit: 'local-commit' }));
    render(<App />);

    fireEvent.click(await screen.findByRole('button', { name: '開啟啟動器設置頁' }));
    fireEvent.click(
      screen.getByRole('button', {
        name: '在 GitHub 開啟 shines871/idle-lineage-class',
      }),
    );

    expect(await screen.findByRole('alert')).toHaveTextContent('無法開啟遊戲來源');
    expect(screen.getByRole('heading', { name: '啟動器設置頁' })).toBeInTheDocument();
  });

  it('does not claim the game is current before a successful fetch', async () => {
    mocks.getGameState.mockResolvedValue(
      state({
        status: 'ready',
        commit: 'local-commit',
        remoteCommit: '',
        message: '檢查更新失敗；目前版本仍可使用',
        error: 'fetch origin/main failed',
      }),
    );
    render(<App />);

    expect(await screen.findByRole('heading', { name: '遊戲已就緒' })).toBeInTheDocument();
    expect(
      screen.getByRole('button', {
        name: '在 GitHub 開啟 shines871/idle-lineage-class',
      }),
    ).toHaveTextContent('shines871/idle-lineage-class');
    expect(screen.queryByText('可啟動')).not.toBeInTheDocument();
    expect(screen.queryByText('已是最新版本')).not.toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent('fetch origin/main failed');
  });

  it('keeps launch enabled while checking for updates', async () => {
    mocks.getGameState.mockResolvedValue(
      state({
        status: 'checking',
        commit: 'local-commit',
        message: '正在檢查官方版本',
        progressPhase: '計算遊戲檔案',
        progressText: '計算遊戲檔案：30%',
        progressPercent: 30,
        progressSeconds: 2,
      }),
    );
    render(<App />);

    const launchButton = await screen.findByRole('button', { name: '啟動遊戲' });
    expect(launchButton).toBeEnabled();
    expect(screen.getByRole('button', { name: '正在檢查更新' })).toBeDisabled();
    expect(screen.getByText('計算遊戲檔案：30%')).toBeInTheDocument();
    expect(screen.queryByLabelText('遊戲版本')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '遊戲資料夾' })).toBeEnabled();

    fireEvent.click(launchButton);

    expect(mocks.launchGame).toHaveBeenCalledTimes(1);
    expect(mocks.launchGame).toHaveBeenCalledWith(null);
  });

  it('offers both the current version and an explicit update when one is available', async () => {
    mocks.getGameState.mockResolvedValue(
      state({
        status: 'update_available',
        commit: '11111111aaaa',
        remoteCommit: '22222222bbbb',
        updateAvailable: true,
        message: '發現新的官方版本',
      }),
    );
    render(<App />);

    expect(await screen.findByRole('heading', { name: '有可用更新' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '啟動遊戲' })).toBeEnabled();
    expect(screen.getByRole('button', { name: '遊戲資料夾' })).toBeEnabled();

    fireEvent.click(screen.getByRole('button', { name: '啟動遊戲' }));
    fireEvent.click(screen.getByRole('button', { name: '立即更新' }));

    expect(mocks.launchGame).toHaveBeenCalledTimes(1);
    expect(mocks.launchGame).toHaveBeenCalledWith(null);
    expect(mocks.startUpdate).toHaveBeenCalledTimes(1);
  });

  it('resets a failed custom browser and keeps a single fallback toast until dismissed', async () => {
    useGameLaunchConfigStore.getState().setBrowserID('browser:chrome');
    mocks.getGameBrowsers.mockResolvedValue([{ id: 'browser:chrome', name: 'Google Chrome' }]);
    mocks.launchGame.mockResolvedValue({ fallbackToDefault: true });
    mocks.getGameState.mockResolvedValue(
      state({
        status: 'ready',
        commit: 'local-commit',
        message: '遊戲已就緒',
      }),
    );
    render(<App />);

    fireEvent.click(await screen.findByRole('button', { name: '開啟啟動器設置頁' }));
    expect(await screen.findByRole('combobox', { name: '遊戲瀏覽器' })).toHaveTextContent(
      'Google Chrome',
    );
    fireEvent.click(screen.getByRole('button', { name: '返回' }));

    fireEvent.click(screen.getByRole('button', { name: '啟動遊戲' }));

    expect(await screen.findByText('所選瀏覽器無法開啟遊戲')).toBeInTheDocument();
    expect(screen.getByText('已改用系統預設瀏覽器開啟，並重設瀏覽器選擇。')).toBeInTheDocument();
    expect(mocks.launchGame).toHaveBeenCalledWith('browser:chrome');
    expect(useGameLaunchConfigStore.getState().browserID).toBeNull();
    expect(toast.getToasts()).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          id: 'game-launch-browser-fallback',
          duration: Infinity,
          closeButton: true,
        }),
      ]),
    );

    fireEvent.click(screen.getByRole('button', { name: '啟動遊戲' }));
    await waitFor(() => expect(mocks.launchGame).toHaveBeenCalledTimes(2));
    expect(screen.getAllByText('所選瀏覽器無法開啟遊戲')).toHaveLength(1);

    fireEvent.click(screen.getByRole('button', { name: '關閉通知' }));
    await waitFor(() => {
      expect(screen.queryByText('所選瀏覽器無法開啟遊戲')).not.toBeInTheDocument();
    });
  });

  it('does not auto-dismiss the fallback toast after the normal toast lifetime', async () => {
    useGameLaunchConfigStore.getState().setBrowserID('browser:chrome');
    mocks.getGameBrowsers.mockResolvedValue([{ id: 'browser:chrome', name: 'Google Chrome' }]);
    mocks.launchGame.mockResolvedValue({ fallbackToDefault: true });
    mocks.getGameState.mockResolvedValue(
      state({
        status: 'ready',
        commit: 'local-commit',
        message: '遊戲已就緒',
      }),
    );
    render(<App />);

    fireEvent.click(await screen.findByRole('button', { name: '開啟啟動器設置頁' }));
    await screen.findByRole('combobox', { name: '遊戲瀏覽器' });
    fireEvent.click(screen.getByRole('button', { name: '返回' }));
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole('button', { name: '啟動遊戲' }));
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      vi.advanceTimersByTime(0);
      await Promise.resolve();
    });

    expect(screen.getByText('所選瀏覽器無法開啟遊戲')).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(60_000));
    expect(screen.getByText('所選瀏覽器無法開啟遊戲')).toBeInTheDocument();
  });

  it('disables launch and update controls while synchronization is in progress', async () => {
    mocks.getGameState.mockResolvedValue(
      state({
        status: 'updating',
        commit: 'local-commit',
        remoteCommit: 'remote-commit',
        progressPhase: '套用遊戲版本',
        progressText: '正在套用官方檔案',
        progressPercent: -1,
        progressSeconds: 65,
        progressStep: 3,
        progressStepTotal: 4,
        progressCancellable: false,
        message: '正在更新',
      }),
    );
    render(<App />);

    const launchButton = await screen.findByRole('button', { name: '啟動遊戲' });
    const updateButton = screen.getByRole('button', { name: '正在更新遊戲' });
    expect(launchButton).toBeDisabled();
    expect(updateButton).toBeDisabled();
    expect(screen.getByRole('button', { name: '遊戲資料夾' })).toBeEnabled();
    expect(screen.getByRole('button', { name: '開啟啟動器設置頁' })).toBeDisabled();
    expect(screen.getByText('正在套用官方檔案')).toBeInTheDocument();
    expect(screen.getByText('已執行 1 分 5 秒')).toBeInTheDocument();
    expect(screen.getByText('步驟 3/4 · 套用遊戲版本')).toBeInTheDocument();
    expect(screen.getByRole('progressbar', { name: '套用遊戲版本' })).not.toHaveAttribute(
      'aria-valuenow',
    );
    expect(screen.getByText('正在保護遊戲檔案，請保持 Launcher 開啟')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '取消更新' })).not.toBeInTheDocument();

    fireEvent.click(launchButton);
    fireEvent.click(updateButton);

    expect(mocks.launchGame).not.toHaveBeenCalled();
    expect(mocks.startUpdate).not.toHaveBeenCalled();
  });

  it('shows all update steps, phase-local progress, elapsed time, and cancellation only while safe', async () => {
    mocks.getGameState.mockResolvedValue(
      state({
        status: 'updating',
        commit: 'local-commit',
        remoteCommit: 'remote-commit',
        progressPhase: '接收 Git 物件',
        progressText: '已接收 70%',
        progressPercent: 70,
        progressSeconds: 600,
        progressStep: 2,
        progressStepTotal: 4,
        progressCancellable: true,
      }),
    );
    render(<App />);

    const stepper = await screen.findByRole('list', { name: '更新進度' });
    expect(within(stepper).getAllByRole('listitem')).toHaveLength(4);
    expect(within(stepper).getByText('連線 GitHub')).toBeInTheDocument();
    expect(within(stepper).getByText('下載更新檔案').closest('li')).toHaveAttribute(
      'aria-current',
      'step',
    );
    expect(within(stepper).getByText('套用遊戲版本')).toBeInTheDocument();
    expect(within(stepper).getByText('驗證遊戲檔案')).toBeInTheDocument();
    expect(screen.getByText('步驟 2/4 · 接收 Git 物件')).toBeInTheDocument();
    expect(screen.getByRole('progressbar', { name: '接收 Git 物件' })).toHaveAttribute(
      'aria-valuenow',
      '70',
    );
    const elapsed = screen.getByText('已執行 10 分 0 秒');
    expect(elapsed).toHaveAttribute('aria-live', 'off');
    expect(elapsed.closest('.progress-block')).not.toHaveAttribute('aria-live');
    expect(screen.queryByText('正在保護遊戲檔案，請保持 Launcher 開啟')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '取消更新' }));
    expect(mocks.cancelUpdate).toHaveBeenCalledTimes(1);

    emit(
      'launcher:game-state',
      state({
        revision: 2,
        status: 'updating',
        commit: 'local-commit',
        remoteCommit: 'remote-commit',
        progressPhase: '解析下載內容',
        progressText: '正在處理 pack',
        progressPercent: 10,
        progressSeconds: 601,
        progressStep: 2,
        progressStepTotal: 4,
        progressCancellable: true,
      }),
    );

    expect(screen.getByText('步驟 2/4 · 解析下載內容')).toBeInTheDocument();
    expect(screen.getByRole('progressbar', { name: '解析下載內容' })).toHaveAttribute(
      'aria-valuenow',
      '10',
    );
    expect(screen.getByText('已執行 10 分 1 秒')).toBeInTheDocument();
  });

  it('renders recovery progress and a retry-only failed recovery state', async () => {
    mocks.getGameState.mockResolvedValue(
      state({
        status: 'recovering',
        commit: 'local-commit',
        progressPhase: '驗證遊戲檔案',
        progressText: '正在確認復原結果…',
        progressPercent: -1,
        progressSeconds: 12,
        progressStep: 2,
        progressStepTotal: 2,
        progressCancellable: false,
      }),
    );
    render(<App />);

    expect(await screen.findByRole('heading', { name: '正在復原遊戲' })).toBeInTheDocument();
    const stepper = screen.getByRole('list', { name: '復原進度' });
    expect(within(stepper).getAllByRole('listitem')).toHaveLength(2);
    expect(within(stepper).getByText('驗證遊戲檔案').closest('li')).toHaveAttribute(
      'aria-current',
      'step',
    );
    expect(screen.getByText('步驟 2/2 · 驗證遊戲檔案')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '啟動遊戲' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '正在復原遊戲' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '開啟啟動器設置頁' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: '取消更新' })).not.toBeInTheDocument();

    emit('launcher:close-guard', {
      mode: 'blocked',
      progressPhase: '驗證遊戲檔案',
    });
    const recoveryDialog = await screen.findByRole('alertdialog', { name: '正在復原遊戲' });
    expect(
      within(recoveryDialog).getByText('為避免遊戲檔案不完整，復原完成前無法關閉 Launcher。'),
    ).toBeInTheDocument();
    fireEvent.click(within(recoveryDialog).getByRole('button', { name: '繼續等待' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument());

    emit(
      'launcher:game-state',
      state({
        revision: 2,
        status: 'recovery_failed',
        commit: 'local-commit',
        message: '無法完成自動復原',
        error: '儲存裝置無法使用',
      }),
    );

    expect(await screen.findByRole('heading', { name: '無法復原遊戲' })).toBeInTheDocument();
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent('儲存裝置無法使用');
    expect(screen.getByRole('button', { name: '開啟啟動器設置頁' })).toBeEnabled();
    fireEvent.click(screen.getByRole('button', { name: '重試復原' }));
    expect(mocks.retryUpdateRecovery).toHaveBeenCalledTimes(1);
  });

  it('keeps action errors across progress events and clears them on the next action', async () => {
    mocks.launchGame.mockRejectedValueOnce(new Error('預設瀏覽器拒絕開啟檔案'));
    mocks.getGameState.mockResolvedValue(state({ status: 'ready', commit: 'local-commit' }));
    render(<App />);

    fireEvent.click(await screen.findByRole('button', { name: '啟動遊戲' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('預設瀏覽器拒絕開啟檔案');
    expect(screen.queryByText('所選瀏覽器無法開啟遊戲')).not.toBeInTheDocument();

    emit(
      'launcher:game-state',
      state({
        revision: 2,
        status: 'checking',
        commit: 'local-commit',
        progressText: 'Fetching origin/main',
        progressPercent: 10,
      }),
    );
    expect(screen.getByRole('alert')).toHaveTextContent('預設瀏覽器拒絕開啟檔案');

    fireEvent.click(screen.getByRole('button', { name: '啟動遊戲' }));
    await waitFor(() =>
      expect(screen.queryByText('預設瀏覽器拒絕開啟檔案')).not.toBeInTheDocument(),
    );
  });

  it('shows the safe-cancel close guard and requests cancellation before closing', async () => {
    mocks.getGameState.mockResolvedValue(
      state({
        status: 'updating',
        commit: 'local-commit',
        progressStep: 2,
        progressStepTotal: 4,
        progressCancellable: true,
      }),
    );
    render(<App />);
    await screen.findByRole('heading', { name: '正在更新遊戲' });

    emit('launcher:close-guard', {
      mode: 'confirm_cancel',
      progressPhase: '下載更新檔案',
    });

    let dialog = await screen.findByRole('alertdialog', { name: '更新仍在下載' });
    expect(within(dialog).getByText('取消更新不會影響目前可用的遊戲版本。')).toBeInTheDocument();
    expect(within(dialog).getByText('目前進度：下載更新檔案')).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole('button', { name: '繼續更新' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument());
    expect(mocks.cancelUpdateAndClose).not.toHaveBeenCalled();

    emit('launcher:close-guard', {
      mode: 'confirm_cancel',
      progressPhase: '下載更新檔案',
    });
    dialog = await screen.findByRole('alertdialog', { name: '更新仍在下載' });
    fireEvent.click(within(dialog).getByRole('button', { name: '取消更新並關閉' }));

    expect(mocks.cancelUpdateAndClose).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument());
  });

  it('blocks closing during a critical phase and offers only continued waiting', async () => {
    mocks.getGameState.mockResolvedValue(
      state({
        status: 'updating',
        commit: 'local-commit',
        progressStep: 3,
        progressStepTotal: 4,
        progressCancellable: false,
      }),
    );
    render(<App />);
    await screen.findByRole('heading', { name: '正在更新遊戲' });

    emit('launcher:close-guard', {
      mode: 'blocked',
      progressPhase: '套用遊戲版本',
    });

    const dialog = await screen.findByRole('alertdialog', { name: '正在套用更新' });
    expect(
      within(dialog).getByText('為避免遊戲檔案不完整，套用與驗證完成前無法關閉 Launcher。'),
    ).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: '繼續等待' })).toBeEnabled();
    expect(
      within(dialog).queryByRole('button', { name: '取消更新並關閉' }),
    ).not.toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole('button', { name: '繼續等待' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument());
    expect(mocks.cancelUpdateAndClose).not.toHaveBeenCalled();
  });

  it('reacts to backend state events and unregisters its runtime listeners', async () => {
    mocks.getGameState.mockResolvedValue(state());
    const { unmount } = render(<App />);
    await screen.findByRole('heading', { name: '尚未下載遊戲' });

    emit('launcher:game-state', state({ revision: 2, status: 'ready', commit: 'commit-sha' }));
    expect(await screen.findByRole('heading', { name: '遊戲已就緒' })).toBeInTheDocument();
    expect(document.querySelector('iframe')).not.toBeInTheDocument();

    unmount();

    expect(mocks.unsubscribe).toHaveBeenCalledTimes(2);
    expect(mocks.unsubscribe).toHaveBeenCalledWith('launcher:game-state');
    expect(mocks.unsubscribe).toHaveBeenCalledWith('launcher:close-guard');
    expect(mocks.listeners.get('launcher:game-state')?.size).toBe(0);
    expect(mocks.listeners.get('launcher:close-guard')?.size).toBe(0);
    expect(mocks.listeners.has('launcher:reload-game')).toBe(false);
  });

  it('returns to the install UI when the backend reports a deleted installation', async () => {
    mocks.getGameState.mockResolvedValue(
      state({
        revision: 1,
        status: 'ready',
        commit: 'local-commit',
        remoteCommit: 'local-commit',
        error: 'stale repository error',
      }),
    );
    render(<App />);

    expect(await screen.findByRole('heading', { name: '遊戲已就緒' })).toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent('stale repository error');

    emit(
      'launcher:game-state',
      state({
        revision: 2,
        status: 'missing',
        commit: '',
        remoteCommit: '',
        message: '尚未下載遊戲',
        error: '',
      }),
    );

    expect(await screen.findByRole('heading', { name: '尚未下載遊戲' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '下載遊戲' })).toBeEnabled();
    expect(screen.queryByRole('button', { name: '啟動遊戲' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '開啟啟動器設置頁' })).toBeEnabled();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('does not let a late initial snapshot overwrite a newer state event', async () => {
    let resolveSnapshot!: (value: State) => void;
    mocks.getGameState.mockReturnValue(
      new Promise(resolve => {
        resolveSnapshot = resolve;
      }),
    );
    render(<App />);

    emit('launcher:game-state', state({ revision: 2, status: 'ready', commit: 'newer-commit' }));
    expect(await screen.findByRole('heading', { name: '遊戲已就緒' })).toBeInTheDocument();

    await act(async () => {
      resolveSnapshot(state());
    });

    expect(screen.getByRole('heading', { name: '遊戲已就緒' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '尚未下載遊戲' })).not.toBeInTheDocument();
  });

  it('drops an older progress event that arrives after the ready state', async () => {
    mocks.getGameState.mockResolvedValue(state({ revision: 1 }));
    render(<App />);
    await screen.findByRole('heading', { name: '尚未下載遊戲' });

    emit(
      'launcher:game-state',
      state({
        revision: 3,
        status: 'ready',
        commit: 'current-commit',
        remoteCommit: 'current-commit',
        message: '目前已是最新版本',
      }),
    );
    expect(await screen.findByText('目前已是最新版本')).toBeInTheDocument();

    emit(
      'launcher:game-state',
      state({
        revision: 2,
        status: 'checking',
        commit: 'current-commit',
        progressPhase: '比較版本',
        progressText: '正在比較 local HEAD 與 origin/main…',
        message: '正在 fetch 官方 main 分支…',
      }),
    );

    expect(screen.getByText('目前已是最新版本')).toBeInTheDocument();
    expect(screen.queryByText('正在比較 local HEAD 與 origin/main…')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '檢查更新' })).toBeEnabled();
  });

  it('shows the full game path tooltip when the path is truncated', async () => {
    const scrollWidth = vi.spyOn(HTMLElement.prototype, 'scrollWidth', 'get').mockReturnValue(320);
    const clientWidth = vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(160);

    try {
      mocks.getGameState.mockResolvedValue(state());
      render(<App />);

      fireEvent.click(await screen.findByRole('button', { name: '開啟啟動器設置頁' }));
      const gamePath = await screen.findByText(
        '/Users/test/Library/Application Support/IdleLineageLauncher/game/shines871',
      );
      expect(gamePath).toHaveAttribute('data-tooltip-id', 'game-folder-path-tooltip');
      expect(gamePath).toHaveAttribute('data-tooltip-content', gamePath.textContent);

      fireEvent.mouseEnter(gamePath);
      const tooltip = await screen.findByRole('tooltip');
      expect(tooltip).toHaveTextContent(gamePath.textContent || '');
      expect(tooltip).toHaveStyle({
        backgroundColor: '#59616c',
        color: '#f8fafc',
        zIndex: '100',
      });
    } finally {
      scrollWidth.mockRestore();
      clientWidth.mockRestore();
    }
  });

  it('does not show a game path tooltip when the full path fits', async () => {
    const scrollWidth = vi.spyOn(HTMLElement.prototype, 'scrollWidth', 'get').mockReturnValue(160);
    const clientWidth = vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(160);

    try {
      mocks.getGameState.mockResolvedValue(state());
      render(<App />);

      fireEvent.click(await screen.findByRole('button', { name: '開啟啟動器設置頁' }));
      const gamePath = await screen.findByText(
        '/Users/test/Library/Application Support/IdleLineageLauncher/game/shines871',
      );
      expect(gamePath).not.toHaveAttribute('data-tooltip-id');
      expect(gamePath).not.toHaveAttribute('data-tooltip-content');

      fireEvent.mouseEnter(gamePath);
      expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();
    } finally {
      scrollWidth.mockRestore();
      clientWidth.mockRestore();
    }
  });

  it('applies an uninstalled folder selection without showing a move dialog', async () => {
    mocks.getGameState.mockResolvedValue(state());
    mocks.selectGameFolder.mockResolvedValue({
      cancelled: false,
      applied: true,
      requiresMove: false,
      root: '/Volumes/GameSSD',
      gamePath: '/Volumes/GameSSD/game/shines871',
      currentGamePath: '/Users/test/Library/Application Support/IdleLineageLauncher/game/shines871',
    });
    render(<App />);

    fireEvent.click(await screen.findByRole('button', { name: '開啟啟動器設置頁' }));
    await screen.findByDisplayValue('/Users/test/Library/Application Support/IdleLineageLauncher');
    mocks.getGameFolderInfo.mockResolvedValue({
      root: '/Volumes/GameSSD',
      gamePath: '/Volumes/GameSSD/game/shines871',
      defaultRoot: '/Users/test/Library/Application Support/IdleLineageLauncher',
      isDefault: false,
    });
    fireEvent.click(screen.getByRole('button', { name: '選擇資料夾' }));

    expect(await screen.findByDisplayValue('/Volumes/GameSSD')).toHaveAttribute('readonly');
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
    expect(mocks.confirmGameFolderMove).not.toHaveBeenCalled();
  });

  it('leaves the current folder unchanged when the native picker is cancelled', async () => {
    mocks.getGameState.mockResolvedValue(state());
    mocks.selectGameFolder.mockResolvedValue({ cancelled: true });
    render(<App />);

    fireEvent.click(await screen.findByRole('button', { name: '開啟啟動器設置頁' }));
    const current = await screen.findByDisplayValue(
      '/Users/test/Library/Application Support/IdleLineageLauncher',
    );
    fireEvent.click(screen.getByRole('button', { name: '選擇資料夾' }));

    await waitFor(() => expect(mocks.selectGameFolder).toHaveBeenCalledOnce());
    expect(current).toHaveValue('/Users/test/Library/Application Support/IdleLineageLauncher');
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  });

  it('restores an uninstalled custom folder to the default root', async () => {
    mocks.getGameState.mockResolvedValue(state());
    mocks.getGameFolderInfo.mockResolvedValue({
      root: '/Volumes/GameSSD',
      gamePath: '/Volumes/GameSSD/game/shines871',
      defaultRoot: '/Users/test/Library/Application Support/IdleLineageLauncher',
      isDefault: false,
    });
    mocks.restoreDefaultGameFolder.mockResolvedValue({
      cancelled: false,
      applied: true,
      requiresMove: false,
      root: '/Users/test/Library/Application Support/IdleLineageLauncher',
      gamePath: '/Users/test/Library/Application Support/IdleLineageLauncher/game/shines871',
      currentGamePath: '/Volumes/GameSSD/game/shines871',
    });
    render(<App />);

    fireEvent.click(await screen.findByRole('button', { name: '開啟啟動器設置頁' }));
    await screen.findByDisplayValue('/Volumes/GameSSD');
    mocks.getGameFolderInfo.mockResolvedValue({
      root: '/Users/test/Library/Application Support/IdleLineageLauncher',
      gamePath: '/Users/test/Library/Application Support/IdleLineageLauncher/game/shines871',
      defaultRoot: '/Users/test/Library/Application Support/IdleLineageLauncher',
      isDefault: true,
    });
    fireEvent.click(screen.getByRole('button', { name: '恢復預設位置' }));

    expect(
      await screen.findByDisplayValue(
        '/Users/test/Library/Application Support/IdleLineageLauncher',
      ),
    ).toBeInTheDocument();
    const restoreDefaultButton = screen.getByRole('button', { name: '恢復預設位置' });
    expect(restoreDefaultButton).toBeDisabled();
    expect(restoreDefaultButton).toHaveClass(
      'disabled:pointer-events-auto',
      'disabled:cursor-not-allowed',
      'disabled:border-slate-700',
      'disabled:bg-slate-800',
      'disabled:text-slate-500',
      'disabled:opacity-100',
    );
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  });

  it('requires explicit confirmation before moving an installed game', async () => {
    mocks.getGameState.mockResolvedValue(
      state({ status: 'ready', commit: 'local-commit', message: '遊戲已就緒' }),
    );
    mocks.selectGameFolder.mockResolvedValue({
      cancelled: false,
      applied: false,
      requiresMove: true,
      root: '/Volumes/GameSSD',
      gamePath: '/Volumes/GameSSD/game/shines871',
      currentGamePath: '/Users/test/Library/Application Support/IdleLineageLauncher/game/shines871',
    });
    render(<App />);

    fireEvent.click(await screen.findByRole('button', { name: '開啟啟動器設置頁' }));
    fireEvent.click(await screen.findByRole('button', { name: '選擇資料夾' }));

    const dialog = await screen.findByRole('alertdialog');
    expect(within(dialog).getByText('/Volumes/GameSSD/game/shines871')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: '返回' })).toBeEnabled();
    expect(within(dialog).getByRole('button', { name: '確認' })).toBeEnabled();
    expect(mocks.confirmGameFolderMove).not.toHaveBeenCalled();

    fireEvent.click(within(dialog).getByRole('button', { name: '返回' }));
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
    expect(mocks.confirmGameFolderMove).not.toHaveBeenCalled();
  });

  it('confirms the installed game move and refreshes the effective path', async () => {
    mocks.getGameState.mockResolvedValue(
      state({ status: 'ready', commit: 'local-commit', message: '遊戲已就緒' }),
    );
    mocks.selectGameFolder.mockResolvedValue({
      cancelled: false,
      applied: false,
      requiresMove: true,
      root: '/Volumes/GameSSD',
      gamePath: '/Volumes/GameSSD/game/shines871',
      currentGamePath: '/Users/test/Library/Application Support/IdleLineageLauncher/game/shines871',
    });
    render(<App />);

    fireEvent.click(await screen.findByRole('button', { name: '開啟啟動器設置頁' }));
    await screen.findByDisplayValue('/Users/test/Library/Application Support/IdleLineageLauncher');
    fireEvent.click(screen.getByRole('button', { name: '選擇資料夾' }));
    const dialog = await screen.findByRole('alertdialog');
    mocks.getGameFolderInfo.mockResolvedValue({
      root: '/Volumes/GameSSD',
      gamePath: '/Volumes/GameSSD/game/shines871',
      defaultRoot: '/Users/test/Library/Application Support/IdleLineageLauncher',
      isDefault: false,
    });
    fireEvent.click(within(dialog).getByRole('button', { name: '確認' }));

    await waitFor(() =>
      expect(mocks.confirmGameFolderMove).toHaveBeenCalledWith('/Volumes/GameSSD'),
    );
    expect(await screen.findByDisplayValue('/Volumes/GameSSD')).toBeInTheDocument();
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  });

  it('keeps the move dialog locked during progress and allows retry after failure', async () => {
    mocks.getGameState.mockResolvedValue(
      state({ status: 'ready', commit: 'local-commit', message: '遊戲已就緒' }),
    );
    mocks.selectGameFolder.mockResolvedValue({
      cancelled: false,
      applied: false,
      requiresMove: true,
      root: '/Volumes/GameSSD',
      gamePath: '/Volumes/GameSSD/game/shines871',
      currentGamePath: '/Users/test/Library/Application Support/IdleLineageLauncher/game/shines871',
    });
    let rejectMove!: (reason: Error) => void;
    mocks.confirmGameFolderMove.mockReturnValueOnce(
      new Promise((_resolve, reject) => {
        rejectMove = reject;
      }),
    );
    render(<App />);

    fireEvent.click(await screen.findByRole('button', { name: '開啟啟動器設置頁' }));
    fireEvent.click(await screen.findByRole('button', { name: '選擇資料夾' }));
    const dialog = await screen.findByRole('alertdialog');
    fireEvent.click(within(dialog).getByRole('button', { name: '確認' }));
    emit(
      'launcher:game-state',
      state({
        revision: 2,
        status: 'moving',
        commit: 'local-commit',
        progressPhase: '複製遊戲',
        progressText: '正在跨磁碟複製遊戲檔案…',
        progressPercent: 42,
      }),
    );

    expect(within(dialog).getByRole('button', { name: '返回' })).toBeDisabled();
    expect(within(dialog).getByRole('button', { name: '搬移中…' })).toBeDisabled();
    expect(within(dialog).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '42');
    expect(within(dialog).getByText('正在跨磁碟複製遊戲檔案…')).toBeInTheDocument();

    await act(async () => rejectMove(new Error('搬移驗證失敗')));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('搬移驗證失敗');
    expect(within(dialog).getByRole('button', { name: '返回' })).toBeEnabled();
    expect(within(dialog).getByRole('button', { name: '確認' })).toBeEnabled();

    mocks.confirmGameFolderMove.mockResolvedValueOnce(undefined);
    mocks.getGameFolderInfo.mockResolvedValue({
      root: '/Volumes/GameSSD',
      gamePath: '/Volumes/GameSSD/game/shines871',
      defaultRoot: '/Users/test/Library/Application Support/IdleLineageLauncher',
      isDefault: false,
    });
    fireEvent.click(within(dialog).getByRole('button', { name: '確認' }));
    await waitFor(() => expect(mocks.confirmGameFolderMove).toHaveBeenCalledTimes(2));
    expect(await screen.findByDisplayValue('/Volumes/GameSSD')).toBeInTheDocument();
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  });

  it('shows a default destination conflict without opening the move dialog', async () => {
    mocks.getGameState.mockResolvedValue(
      state({ status: 'ready', commit: 'local-commit', message: '遊戲已就緒' }),
    );
    mocks.getGameFolderInfo.mockResolvedValue({
      root: '/Volumes/GameSSD',
      gamePath: '/Volumes/GameSSD/game/shines871',
      defaultRoot: '/Users/test/Library/Application Support/IdleLineageLauncher',
      isDefault: false,
    });
    mocks.restoreDefaultGameFolder.mockRejectedValue(
      new Error('預設位置已有遊戲，請先自行清理後再重試'),
    );
    render(<App />);

    fireEvent.click(await screen.findByRole('button', { name: '開啟啟動器設置頁' }));
    fireEvent.click(await screen.findByRole('button', { name: '恢復預設位置' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '預設位置已有遊戲，請先自行清理後再重試',
    );
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  });

  it('renders the unavailable storage recovery actions and configured root', async () => {
    mocks.getGameState.mockResolvedValue(
      state({
        status: 'storage_unavailable',
        message: '遊戲儲存位置無法使用',
        error: '找不到目前設定的遊戲資料夾',
      }),
    );
    mocks.getGameFolderInfo.mockResolvedValue({
      root: '/Volumes/MissingSSD',
      gamePath: '/Volumes/MissingSSD/game/shines871',
      defaultRoot: '/Users/test/Library/Application Support/IdleLineageLauncher',
      isDefault: false,
    });
    render(<App />);

    expect(
      await screen.findByRole('heading', { name: '遊戲儲存位置無法使用' }),
    ).toBeInTheDocument();
    expect(screen.getByText('/Volumes/MissingSSD')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '重新檢查' })).toBeEnabled();
    expect(screen.getByRole('button', { name: '前往設定' })).toBeEnabled();
    expect(screen.getByRole('button', { name: '開啟啟動器設置頁' })).toBeEnabled();
  });
});
