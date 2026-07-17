import type { GameBrowser } from '../../bindings/github.com/SDxBacon/idle-lineage-launcher';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import type { GameBrowserLoadState } from '@/hooks/useGameBrowsers';
import { useGameLaunchConfigStore } from '@/stores/useGameLaunchConfigStore';

const SYSTEM_DEFAULT_VALUE = '__system_default__';
const BROWSER_VALUE_PREFIX = 'browser:';

function browserValue(browserID: string) {
  return `${BROWSER_VALUE_PREFIX}${browserID}`;
}

export type GameLaunchSelectProps = {
  browsers: GameBrowser[];
  loadState: GameBrowserLoadState;
};

export function GameLaunchSelect({ browsers, loadState }: GameLaunchSelectProps) {
  const browserID = useGameLaunchConfigStore(state => state.browserID);
  const setBrowserID = useGameLaunchConfigStore(state => state.setBrowserID);

  const selectedValue = browserID === null
    ? SYSTEM_DEFAULT_VALUE
    : browserValue(browserID);
  const disabled = loadState !== 'ready';

  const handleValueChange = (value: string) => {
    if (value === SYSTEM_DEFAULT_VALUE) {
      setBrowserID(null);
      return;
    }
    if (value.startsWith(BROWSER_VALUE_PREFIX)) {
      setBrowserID(value.slice(BROWSER_VALUE_PREFIX.length));
    }
  };

  return (
    <section className="game-launch-config" aria-labelledby="game-launch-browser-label">
      <div className="game-launch-config-copy">
        <label id="game-launch-browser-label" htmlFor="game-launch-browser">
          遊戲瀏覽器
        </label>
        <p id="game-launch-browser-description">遊戲存檔會依瀏覽器分開保存。</p>
      </div>
      <Select
        value={selectedValue}
        onValueChange={handleValueChange}
        disabled={disabled}
      >
        <SelectTrigger
          id="game-launch-browser"
          className="game-launch-select-trigger"
          aria-describedby="game-launch-browser-description"
        >
          {loadState === 'loading' && <span>正在讀取瀏覽器…</span>}
          {loadState === 'error' && <span>無法讀取瀏覽器</span>}
          {loadState === 'ready' && <SelectValue />}
        </SelectTrigger>
        <SelectContent position="popper" sideOffset={6}>
          <SelectItem value={SYSTEM_DEFAULT_VALUE}>系統預設</SelectItem>
          {browsers.map(browser => (
            <SelectItem key={browser.id} value={browserValue(browser.id)}>
              {browser.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </section>
  );
}
