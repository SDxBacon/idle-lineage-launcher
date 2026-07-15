import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from './App';

const mocks = vi.hoisted(() => {
  const listeners = new Map<string, Set<(event: { data: unknown }) => void>>();
  return {
    listeners,
    getGameState: vi.fn(),
    startInstall: vi.fn(),
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
    StatusCancelled: 'cancelled',
    StatusError: 'error',
  },
  LauncherService: {
    GetGameState: mocks.getGameState,
    StartInstall: mocks.startInstall,
    CancelInstall: mocks.cancelInstall,
    CreateGameWindow: mocks.createGameWindow,
  },
}));

type State = {
  status: string;
  commit: string;
  receivedBytes: number;
  totalBytes: number;
  filesExtracted: number;
  message: string;
  error: string;
};

const state = (overrides: Partial<State> = {}): State => ({
  status: 'missing',
  commit: '',
  receivedBytes: 0,
  totalBytes: 0,
  filesExtracted: 0,
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
      receivedBytes: 5 * 1024 * 1024,
      totalBytes: 10 * 1024 * 1024,
      filesExtracted: 42,
      message: '正在下載',
    }));
    render(<App />);

    expect(await screen.findByText('50%')).toBeInTheDocument();
    expect(screen.getByText('42 個檔案已解壓')).toBeInTheDocument();
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
