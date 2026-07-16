import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
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
    createGameWindow: vi.fn(),
    toggleFullscreen: vi.fn(),
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
  Window: { ToggleFullscreen: mocks.toggleFullscreen },
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
    CreateGameWindow: mocks.createGameWindow,
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
  message: '尚未安裝遊戲內容',
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
    mocks.createGameWindow.mockResolvedValue(undefined);
    mocks.toggleFullscreen.mockResolvedValue(undefined);
  });

  afterEach(() => {
    document.body.innerHTML = '';
  });

  it('shows the first-install requirements and starts only after confirmation', async () => {
    mocks.getGameState.mockResolvedValue(state());
    render(<App />);

    expect(await screen.findByRole('heading', { name: '準備安裝遊戲' })).toBeInTheDocument();
    expect(screen.getByText('約 500–800 MB')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '下載並安裝' }));
    expect(mocks.startInstall).toHaveBeenCalledTimes(1);
  });

  it('renders shared install progress and cancellation', async () => {
    mocks.getGameState.mockResolvedValue(state({
      status: 'installing',
      commit: 'abc',
      progressPhase: '接收 Git objects',
      progressText: 'Receiving objects: 50% (50/100)',
      progressPercent: 50,
      progressSeconds: 8,
      message: '正在下載',
    }));
    render(<App />);

    expect(await screen.findByText('50%')).toBeInTheDocument();
    expect(screen.getByText('Receiving objects: 50% (50/100)')).toBeInTheDocument();
    expect(screen.getByText('已執行 8 秒')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '取消' }));
    expect(mocks.cancelInstall).toHaveBeenCalledTimes(1);
  });

  it('shows backend errors and retries', async () => {
    mocks.getGameState.mockResolvedValue(state({ status: 'error', error: 'archive 已損毀' }));
    render(<App />);

    expect(await screen.findByRole('alert')).toHaveTextContent('archive 已損毀');
    fireEvent.click(screen.getByRole('button', { name: '重試安裝' }));
    expect(mocks.startInstall).toHaveBeenCalledTimes(1);
  });

  it('switches from missing to ready when the global event arrives', async () => {
    mocks.getGameState.mockResolvedValue(state());
    render(<App />);
    await screen.findByRole('heading', { name: '準備安裝遊戲' });

    emit('launcher:game-state', state({ status: 'ready', commit: 'commit-sha' }));
    const frame = await screen.findByTitle('Idle Lineage Class');
    expect(frame).toHaveAttribute('src', '/game/index.html?version=commit-sha');
    expect(frame).not.toHaveAttribute('sandbox');
  });

  it('runs all Dock actions and reloads only the iframe', async () => {
    mocks.getGameState.mockResolvedValue(state({ status: 'ready', commit: 'commit-sha' }));
    render(<App />);
    const firstFrame = await screen.findByTitle('Idle Lineage Class');

    fireEvent.click(screen.getByRole('button', { name: '新增視窗' }));
    fireEvent.click(screen.getByRole('button', { name: '切換全螢幕' }));
    fireEvent.click(screen.getByRole('button', { name: '重新載入遊戲' }));

    expect(mocks.createGameWindow).toHaveBeenCalledTimes(1);
    expect(mocks.toggleFullscreen).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.getByTitle('Idle Lineage Class')).not.toBe(firstFrame));
  });

  it('checks for updates and pulls an available revision', async () => {
    mocks.getGameState.mockResolvedValue(state({ status: 'ready', commit: 'local' }));
    render(<App />);
    await screen.findByTitle('Idle Lineage Class');

    fireEvent.click(screen.getByRole('button', { name: '檢查更新' }));
    expect(mocks.checkForUpdate).toHaveBeenCalledTimes(1);

    emit('launcher:game-state', state({
      status: 'update_available',
      commit: 'local',
      remoteCommit: 'remote',
      updateAvailable: true,
      message: '發現新的官方版本',
    }));
    fireEvent.click(await screen.findByRole('button', { name: '發現新的官方版本，按一下即可更新' }));
    expect(mocks.startUpdate).toHaveBeenCalledTimes(1);
    expect(screen.getByTitle('Idle Lineage Class')).toHaveAttribute('src', '/game/index.html?version=local');
  });

  it('keeps the installed game visible while fetch and pull run', async () => {
    mocks.getGameState.mockResolvedValue(state({
      status: 'checking',
      commit: 'local',
      message: '正在 fetch',
      progressPhase: '計算 Git objects',
      progressText: 'Counting objects: 30% (3/10)',
      progressPercent: 30,
      progressSeconds: 2,
    }));
    render(<App />);

    expect(await screen.findByTitle('Idle Lineage Class')).toHaveAttribute('src', '/game/index.html?version=local');
    expect(screen.getByText('Counting objects: 30% (3/10)')).toBeInTheDocument();
    expect(screen.getByText('30%')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '正在檢查更新' })).toBeDisabled();
  });

  it('removes the loading cover when the same-origin game DOM becomes interactive', async () => {
    mocks.getGameState.mockResolvedValue(state({ status: 'ready', commit: 'commit-sha' }));
    render(<App />);
    const frame = await screen.findByTitle('Idle Lineage Class');
    expect(screen.getByText('正在載入遊戲…')).toBeInTheDocument();

    Object.defineProperty(frame, 'contentWindow', {
      configurable: true,
      value: { location: { pathname: '/game/index.html' } },
    });
    Object.defineProperty(frame, 'contentDocument', {
      configurable: true,
      value: { readyState: 'interactive' },
    });

    await waitFor(() => expect(screen.queryByText('正在載入遊戲…')).not.toBeInTheDocument());
  });

  it('handles the native reload event and unregisters listeners on unmount', async () => {
    mocks.getGameState.mockResolvedValue(state({ status: 'ready', commit: 'commit-sha' }));
    const { unmount } = render(<App />);
    const firstFrame = await screen.findByTitle('Idle Lineage Class');

    emit('launcher:reload-game');
    await waitFor(() => expect(screen.getByTitle('Idle Lineage Class')).not.toBe(firstFrame));
    unmount();

    expect(mocks.unsubscribe).toHaveBeenCalledWith('launcher:game-state');
    expect(mocks.unsubscribe).toHaveBeenCalledWith('launcher:reload-game');
    expect(mocks.listeners.get('launcher:game-state')).toHaveLength(0);
    expect(mocks.listeners.get('launcher:reload-game')).toHaveLength(0);
  });
});
