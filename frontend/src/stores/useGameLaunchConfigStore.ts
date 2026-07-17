import { create } from 'zustand';
import { createJSONStorage, persist } from 'zustand/middleware';

export const GAME_LAUNCH_CONFIG_STORAGE_KEY = 'idle-lineage-launcher-game-launch-config';

type PersistedGameLaunchConfig = {
  browserID?: unknown;
};

export type GameLaunchConfigState = {
  browserID: string | null;
  setBrowserID: (browserID: string | null) => void;
};

function normalizeBrowserID(browserID: unknown): string | null {
  return typeof browserID === 'string' && browserID.length > 0 ? browserID : null;
}

export const useGameLaunchConfigStore = create<GameLaunchConfigState>()(
  persist(
    set => ({
      browserID: null,
      setBrowserID: browserID => set({ browserID: normalizeBrowserID(browserID) }),
    }),
    {
      name: GAME_LAUNCH_CONFIG_STORAGE_KEY,
      version: 1,
      storage: createJSONStorage(() => localStorage),
      partialize: state => ({ browserID: state.browserID }),
      migrate: persistedState => ({
        browserID: normalizeBrowserID(
          (persistedState as PersistedGameLaunchConfig | undefined)?.browserID,
        ),
      }),
      merge: (persistedState, currentState) => ({
        ...currentState,
        browserID: normalizeBrowserID(
          (persistedState as PersistedGameLaunchConfig | undefined)?.browserID,
        ),
      }),
    },
  ),
);
