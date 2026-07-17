import InlineError from '@/components/InlineError';
import { GameLaunchSelect } from '@/components/GameLaunchSelect';
import { type GameBrowserOptions } from '@/hooks/useGameBrowsers';

function SettingsView({
  browsers,
  loadState,
  actionError,
  onBack,
}: GameBrowserOptions & {
  actionError: string;
  onBack: () => void;
}) {
  return (
    <div className="settings-view">
      <GameLaunchSelect browsers={browsers} loadState={loadState} />
      <section className="settings-panel" aria-labelledby="game-folder-settings-title">
        <h2 id="game-folder-settings-title">遊戲資料夾</h2>
        <p>Lorem ipsum dolor sit amet, consectetur adipiscing elit.</p>
      </section>
      {actionError && <InlineError message={actionError} />}
      <div className="settings-actions">
        <button className="secondary-button" type="button" onClick={onBack}>
          返回
        </button>
      </div>
    </div>
  );
}

export default SettingsView;
