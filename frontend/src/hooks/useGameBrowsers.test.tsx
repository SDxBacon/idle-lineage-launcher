import { StrictMode } from 'react';
import { act, cleanup, renderHook, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { useGameLaunchConfigStore } from '@/stores/useGameLaunchConfigStore';
import { useGameBrowsers } from './useGameBrowsers';

const mocks = vi.hoisted(() => ({
  getGameBrowsers: vi.fn(),
}));

vi.mock('../../bindings/github.com/SDxBacon/idle-lineage-launcher', () => ({
  LauncherService: {
    GetGameBrowsers: mocks.getGameBrowsers,
  },
}));

describe('useGameBrowsers', () => {
  beforeEach(() => {
    localStorage.clear();
    useGameLaunchConfigStore.setState({ browserID: null });
    vi.clearAllMocks();
  });

  afterEach(cleanup);

  it('loads usable browsers and validates the current persisted ID', async () => {
    useGameLaunchConfigStore.getState().setBrowserID('browser:removed');
    mocks.getGameBrowsers.mockResolvedValue([
      { id: '', name: 'Invalid Browser' },
      { id: 'browser:available', name: 'Available Browser' },
    ]);

    const { result } = renderHook(() => useGameBrowsers());

    expect(result.current).toEqual({ browsers: [], loadState: 'loading' });
    await waitFor(() => expect(result.current.loadState).toBe('ready'));
    expect(result.current.browsers).toEqual([
      { id: 'browser:available', name: 'Available Browser' },
    ]);
    expect(useGameLaunchConfigStore.getState().browserID).toBeNull();
  });

  it('preserves a persisted browser that is still installed', async () => {
    useGameLaunchConfigStore.getState().setBrowserID('browser:available');
    mocks.getGameBrowsers.mockResolvedValue([
      { id: 'browser:available', name: 'Available Browser' },
    ]);

    const { result } = renderHook(() => useGameBrowsers());

    await waitFor(() => expect(result.current.loadState).toBe('ready'));
    expect(useGameLaunchConfigStore.getState().browserID).toBe('browser:available');
  });

  it('uses the store value at response time instead of a mount-time snapshot', async () => {
    let resolveBrowsers!: (browsers: { id: string; name: string }[]) => void;
    mocks.getGameBrowsers.mockReturnValue(new Promise(resolve => {
      resolveBrowsers = resolve;
    }));
    useGameLaunchConfigStore.getState().setBrowserID('browser:old');

    const { result } = renderHook(() => useGameBrowsers());
    act(() => {
      useGameLaunchConfigStore.getState().setBrowserID('browser:new');
      resolveBrowsers([{ id: 'browser:new', name: 'New Browser' }]);
    });

    await waitFor(() => expect(result.current.loadState).toBe('ready'));
    expect(useGameLaunchConfigStore.getState().browserID).toBe('browser:new');
  });

  it('keeps the persisted browser when lookup fails', async () => {
    useGameLaunchConfigStore.getState().setBrowserID('browser:temporarily-unavailable');
    mocks.getGameBrowsers.mockRejectedValue(new Error('platform lookup failed'));

    const { result } = renderHook(() => useGameBrowsers());

    await waitFor(() => expect(result.current.loadState).toBe('error'));
    expect(result.current.browsers).toEqual([]);
    expect(useGameLaunchConfigStore.getState().browserID).toBe(
      'browser:temporarily-unavailable',
    );
  });

  it('deduplicates the StrictMode mount request while it is in flight', async () => {
    let resolveBrowsers!: (browsers: []) => void;
    mocks.getGameBrowsers.mockReturnValue(new Promise(resolve => {
      resolveBrowsers = resolve;
    }));

    const { result } = renderHook(() => useGameBrowsers(), {
      wrapper: StrictMode,
    });

    expect(mocks.getGameBrowsers).toHaveBeenCalledTimes(1);
    act(() => resolveBrowsers([]));
    await waitFor(() => expect(result.current.loadState).toBe('ready'));
  });
});
