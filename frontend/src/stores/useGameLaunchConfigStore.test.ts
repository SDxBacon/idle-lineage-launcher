import { beforeEach, describe, expect, it } from 'vitest';

import {
  GAME_LAUNCH_CONFIG_STORAGE_KEY,
  useGameLaunchConfigStore,
} from './useGameLaunchConfigStore';

describe('useGameLaunchConfigStore', () => {
  beforeEach(() => {
    localStorage.clear();
    useGameLaunchConfigStore.setState({ browserID: null });
  });

  it('persists only the selected browser ID', () => {
    useGameLaunchConfigStore.getState().setBrowserID('macos:com.google.Chrome');

    expect(useGameLaunchConfigStore.getState().browserID).toBe('macos:com.google.Chrome');
    expect(JSON.parse(localStorage.getItem(GAME_LAUNCH_CONFIG_STORAGE_KEY) || '{}')).toEqual({
      state: { browserID: 'macos:com.google.Chrome' },
      version: 1,
    });
  });

  it('normalizes empty, malformed, and corrupt persisted values to system default', async () => {
    useGameLaunchConfigStore.getState().setBrowserID('');
    expect(useGameLaunchConfigStore.getState().browserID).toBeNull();

    localStorage.setItem(
      GAME_LAUNCH_CONFIG_STORAGE_KEY,
      JSON.stringify({
        state: { browserID: 42 },
        version: 1,
      }),
    );
    await useGameLaunchConfigStore.persist.rehydrate();

    expect(useGameLaunchConfigStore.getState().browserID).toBeNull();
  });

  it('rehydrates a valid browser ID', async () => {
    localStorage.setItem(
      GAME_LAUNCH_CONFIG_STORAGE_KEY,
      JSON.stringify({
        state: { browserID: 'windows:ChromeHTML' },
        version: 1,
      }),
    );
    await useGameLaunchConfigStore.persist.rehydrate();

    expect(useGameLaunchConfigStore.getState().browserID).toBe('windows:ChromeHTML');
  });
});
