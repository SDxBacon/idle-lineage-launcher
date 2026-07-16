import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from './App';

const mocks = vi.hoisted(() => {
  const listeners = new Map<string, Set<(event: { data: unknown }) => void>>();
  return {
    listeners,
    getGameState: vi.fn(),
    startInstall: vi.fn(),
    checkForUpdate: vi.fn(),
    startUpdate: vi.fn(),
    cancelInstall: vi.fn(),
    launchGame: vi.fn(),
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
    constructor(source: Record<string, unknown>) { Object.assign(this, source); }
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
    GetGameState: mocks.getGameState,
    StartInstall: mocks.startInstall,
    CheckForUpdate: mocks.checkForUpdate,
    StartUpdate: mocks.startUpdate,
    CancelInstall: mocks.cancelInstall,
    LaunchGame: mocks.launchGame,
  },
}));

type State = {
  status: string;
  commit: string;
  remoteCommit: string;
  updateAvailable: boolean;
  progressPhase: string;
  progressText: string;
  progressPercent: number;
  progressSeconds: number;
  message: string;
  error: string;
};

const state = (overrides: Partial<State> = {}): State => ({
  status: 'missing',
  commit: '',
  remoteCommit: '',
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

describe('App', () => {
  beforeEach(() => {
    mocks.listeners.clear();
    vi.clearAllMocks();
    mocks.startInstall.mockResolvedValue(undefined);
    mocks.checkForUpdate.mockResolvedValue(undefined);
    mocks.startUpdate.mockResolvedValue(undefined);
    mocks.cancelInstall.mockResolvedValue(undefined);
    mocks.launchGame.mockResolvedValue(undefined);
  });

  afterEach(() => {
    cleanup();
  });

  it('performs only a local state read on first render and downloads after confirmation', async () => {
    mocks.getGameState.mockResolvedValue(state());
    render(<App />);

    expect(await screen.findByRole('heading', { name: '尚未下載遊戲' })).toBeInTheDocument();
    expect(screen.getByText('約 500–800 MB')).toBeInTheDocument();
    expect(mocks.startInstall).not.toHaveBeenCalled();
    expect(mocks.checkForUpdate).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: '下載遊戲' }));

    expect(mocks.startInstall).toHaveBeenCalledTimes(1);
  });

  it('renders install progress and lets the user cancel the clone', async () => {
    mocks.getGameState.mockResolvedValue(state({
      status: 'installing',
      progressPhase: '接收 Git objects',
      progressText: 'Receiving objects: 50% (50/100)',
      progressPercent: 50,
      progressSeconds: 8,
      message: '正在下載',
    }));
    render(<App />);

    expect(await screen.findByRole('heading', { name: '正在下載遊戲' })).toBeInTheDocument();
    expect(screen.getByText('50%')).toBeInTheDocument();
    expect(screen.getByText('Receiving objects: 50% (50/100)')).toBeInTheDocument();
    expect(screen.getByText('已執行 8 秒')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '取消下載' }));

    expect(mocks.cancelInstall).toHaveBeenCalledTimes(1);
  });

  it('renders repository resolution as cancellable download progress', async () => {
    mocks.getGameState.mockResolvedValue(state({
      status: 'resolving',
      progressPercent: -1,
      message: '正在確認官方 repository',
    }));
    render(<App />);

    expect(await screen.findByRole('heading', { name: '正在下載遊戲' })).toBeInTheDocument();
    expect(screen.getByRole('progressbar', { name: 'Resolve repository' })).not.toHaveAttribute('aria-valuenow');
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
    fireEvent.click(screen.getByRole('button', { name: '重試下載' }));

    expect(mocks.startInstall).toHaveBeenCalledTimes(1);
  });

  it('launches and manually checks updates from the ready dashboard', async () => {
    mocks.getGameState.mockResolvedValue(state({
      status: 'ready',
      commit: '0123456789abcdef',
      remoteCommit: 'fedcba9876543210',
      message: '遊戲已就緒',
    }));
    render(<App />);

    expect(await screen.findByRole('heading', { name: '遊戲已就緒' })).toBeInTheDocument();
    expect(screen.getByText('01234567')).toBeInTheDocument();
    expect(screen.getByText('fedcba98')).toBeInTheDocument();
    expect(document.querySelector('iframe')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '啟動遊戲' }));
    fireEvent.click(screen.getByRole('button', { name: '檢查更新' }));

    expect(mocks.launchGame).toHaveBeenCalledTimes(1);
    expect(mocks.checkForUpdate).toHaveBeenCalledTimes(1);
  });

  it('does not claim the game is current before a successful fetch', async () => {
    mocks.getGameState.mockResolvedValue(state({
      status: 'ready',
      commit: 'local-commit',
      remoteCommit: '',
      message: '檢查更新失敗；目前版本仍可使用',
      error: 'fetch origin/main failed',
    }));
    render(<App />);

    expect(await screen.findByText('可啟動')).toBeInTheDocument();
    expect(screen.queryByText('已是最新版本')).not.toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent('fetch origin/main failed');
  });

  it('keeps launch enabled while fetch is checking for updates', async () => {
    mocks.getGameState.mockResolvedValue(state({
      status: 'checking',
      commit: 'local-commit',
      message: '正在 fetch',
      progressPhase: '計算 Git objects',
      progressText: 'Counting objects: 30% (3/10)',
      progressPercent: 30,
      progressSeconds: 2,
    }));
    render(<App />);

    const launchButton = await screen.findByRole('button', { name: '啟動遊戲' });
    expect(launchButton).toBeEnabled();
    expect(screen.getByRole('button', { name: '正在檢查更新' })).toBeDisabled();
    expect(screen.getByText('Counting objects: 30% (3/10)')).toBeInTheDocument();

    fireEvent.click(launchButton);

    expect(mocks.launchGame).toHaveBeenCalledTimes(1);
  });

  it('offers both the current version and an explicit pull when an update is available', async () => {
    mocks.getGameState.mockResolvedValue(state({
      status: 'update_available',
      commit: '11111111aaaa',
      remoteCommit: '22222222bbbb',
      updateAvailable: true,
      message: '發現新的官方版本',
    }));
    render(<App />);

    expect(await screen.findByRole('heading', { name: '有可用更新' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '啟動遊戲' })).toBeEnabled();

    fireEvent.click(screen.getByRole('button', { name: '啟動遊戲' }));
    fireEvent.click(screen.getByRole('button', { name: '立即更新' }));

    expect(mocks.launchGame).toHaveBeenCalledTimes(1);
    expect(mocks.startUpdate).toHaveBeenCalledTimes(1);
  });

  it('disables launch and update controls while pull is in progress', async () => {
    mocks.getGameState.mockResolvedValue(state({
      status: 'updating',
      commit: 'local-commit',
      remoteCommit: 'remote-commit',
      progressPhase: 'Pull repository',
      progressText: 'Fast-forwarding files',
      progressPercent: 70,
      progressSeconds: 65,
      message: '正在更新',
    }));
    render(<App />);

    const launchButton = await screen.findByRole('button', { name: '啟動遊戲' });
    const updateButton = screen.getByRole('button', { name: '正在更新遊戲' });
    expect(launchButton).toBeDisabled();
    expect(updateButton).toBeDisabled();
    expect(screen.getByText('Fast-forwarding files')).toBeInTheDocument();
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

    emit('launcher:game-state', state({
      status: 'checking',
      commit: 'local-commit',
      progressText: 'Fetching origin/main',
      progressPercent: 10,
    }));
    expect(screen.getByRole('alert')).toHaveTextContent('預設瀏覽器拒絕開啟檔案');

    fireEvent.click(screen.getByRole('button', { name: '啟動遊戲' }));
    await waitFor(() => expect(screen.queryByText('預設瀏覽器拒絕開啟檔案')).not.toBeInTheDocument());
  });

  it('reacts to backend state events and unregisters only the state listener', async () => {
    mocks.getGameState.mockResolvedValue(state());
    const { unmount } = render(<App />);
    await screen.findByRole('heading', { name: '尚未下載遊戲' });

    emit('launcher:game-state', state({ status: 'ready', commit: 'commit-sha' }));
    expect(await screen.findByRole('heading', { name: '遊戲已就緒' })).toBeInTheDocument();
    expect(document.querySelector('iframe')).not.toBeInTheDocument();

    unmount();

    expect(mocks.unsubscribe).toHaveBeenCalledTimes(1);
    expect(mocks.unsubscribe).toHaveBeenCalledWith('launcher:game-state');
    expect(mocks.listeners.get('launcher:game-state')?.size).toBe(0);
    expect(mocks.listeners.has('launcher:reload-game')).toBe(false);
  });

  it('does not let a late initial snapshot overwrite a newer state event', async () => {
    let resolveSnapshot!: (value: State) => void;
    mocks.getGameState.mockReturnValue(new Promise(resolve => {
      resolveSnapshot = resolve;
    }));
    render(<App />);

    emit('launcher:game-state', state({ status: 'ready', commit: 'newer-commit' }));
    expect(await screen.findByRole('heading', { name: '遊戲已就緒' })).toBeInTheDocument();

    await act(async () => {
      resolveSnapshot(state());
    });

    expect(screen.getByRole('heading', { name: '遊戲已就緒' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '尚未下載遊戲' })).not.toBeInTheDocument();
  });
});
