import { useEffect, useState } from 'react';

import {
  LauncherService,
  type GameBrowser,
} from '../../bindings/github.com/SDxBacon/idle-lineage-launcher';
import { useGameLaunchConfigStore } from '@/stores/useGameLaunchConfigStore';

export type GameBrowserLoadState = 'loading' | 'ready' | 'error';

export type GameBrowserOptions = {
  browsers: GameBrowser[];
  loadState: GameBrowserLoadState;
};

let gameBrowsersInFlight: Promise<GameBrowser[]> | null = null;

function getGameBrowsersOnce() {
  if (gameBrowsersInFlight) {
    return gameBrowsersInFlight;
  }

  let request: Promise<GameBrowser[]>;
  try {
    request = Promise.resolve(LauncherService.GetGameBrowsers());
  } catch (error) {
    request = Promise.reject(error);
  }

  gameBrowsersInFlight = request;
  void request
    .finally(() => {
      if (gameBrowsersInFlight === request) {
        gameBrowsersInFlight = null;
      }
    })
    .catch(() => undefined);

  return request;
}

export function useGameBrowsers(): GameBrowserOptions {
  const [options, setOptions] = useState<GameBrowserOptions>({
    browsers: [],
    loadState: 'loading',
  });

  useEffect(() => {
    let mounted = true;

    void getGameBrowsersOnce()
      .then(installedBrowsers => {
        if (!mounted) return;

        const browsers = installedBrowsers.filter(browser => browser.id.length > 0);
        setOptions({ browsers, loadState: 'ready' });

        const { browserID, setBrowserID } = useGameLaunchConfigStore.getState();
        if (browserID !== null && !browsers.some(browser => browser.id === browserID)) {
          setBrowserID(null);
        }
      })
      .catch(() => {
        if (mounted) {
          setOptions({ browsers: [], loadState: 'error' });
        }
      });

    return () => {
      mounted = false;
    };
  }, []);

  return options;
}
