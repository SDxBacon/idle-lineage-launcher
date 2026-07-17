import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
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
    getLauncherInfo: vi.fn(),
    startInstall: vi.fn(),
    checkForUpdate: vi.fn(),
    startUpdate: vi.fn(),
    cancelInstall: vi.fn(),
    launchGame: vi.fn(),
    openGameFolder: vi.fn(),
    openGameRepository: vi.fn(),
    openLauncherRepository: vi.fn(),
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
    StatusCancelled: 'cancelled',
    StatusError: 'error',
  },
  LauncherService: {
    GetGameBrowsers: mocks.getGameBrowsers,
    GetGameState: mocks.getGameState,
    GetLauncherInfo: mocks.getLauncherInfo,
    StartInstall: mocks.startInstall,
    CheckForUpdate: mocks.checkForUpdate,
    StartUpdate: mocks.startUpdate,
    CancelInstall: mocks.cancelInstall,
    LaunchGame: mocks.launchGame,
    OpenGameFolder: mocks.openGameFolder,
    OpenGameRepository: mocks.openGameRepository,
    OpenLauncherRepository: mocks.openLauncherRepository,
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
    mocks.startInstall.mockResolvedValue(undefined);
    mocks.checkForUpdate.mockResolvedValue(undefined);
    mocks.startUpdate.mockResolvedValue(undefined);
    mocks.cancelInstall.mockResolvedValue(undefined);
    mocks.launchGame.mockResolvedValue({ fallbackToDefault: false });
    mocks.openGameFolder.mockResolvedValue(undefined);
    mocks.openGameRepository.mockResolvedValue(undefined);
    mocks.openLauncherRepository.mockResolvedValue(undefined);
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
    expect(screen.queryByRole('button', { name: '開啟啟動器設置頁' })).not.toBeInTheDocument();
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
      screen.getByText('Lorem ipsum dolor sit amet, consectetur adipiscing elit.'),
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
        progressPhase: '同步官方版本',
        progressText: '正在套用官方檔案',
        progressPercent: 70,
        progressSeconds: 65,
        message: '正在更新',
      }),
    );
    render(<App />);

    const launchButton = await screen.findByRole('button', { name: '啟動遊戲' });
    const updateButton = screen.getByRole('button', { name: '正在更新遊戲' });
    expect(launchButton).toBeDisabled();
    expect(updateButton).toBeDisabled();
    expect(screen.getByRole('button', { name: '遊戲資料夾' })).toBeEnabled();
    expect(screen.getByText('正在套用官方檔案')).toBeInTheDocument();
    expect(screen.getByText('已執行 1 分 5 秒')).toBeInTheDocument();

    fireEvent.click(launchButton);
    fireEvent.click(updateButton);

    expect(mocks.launchGame).not.toHaveBeenCalled();
    expect(mocks.startUpdate).not.toHaveBeenCalled();
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

  it('reacts to backend state events and unregisters only the state listener', async () => {
    mocks.getGameState.mockResolvedValue(state());
    const { unmount } = render(<App />);
    await screen.findByRole('heading', { name: '尚未下載遊戲' });

    emit('launcher:game-state', state({ revision: 2, status: 'ready', commit: 'commit-sha' }));
    expect(await screen.findByRole('heading', { name: '遊戲已就緒' })).toBeInTheDocument();
    expect(document.querySelector('iframe')).not.toBeInTheDocument();

    unmount();

    expect(mocks.unsubscribe).toHaveBeenCalledTimes(1);
    expect(mocks.unsubscribe).toHaveBeenCalledWith('launcher:game-state');
    expect(mocks.listeners.get('launcher:game-state')?.size).toBe(0);
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
    expect(screen.queryByRole('button', { name: '開啟啟動器設置頁' })).not.toBeInTheDocument();
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
});
