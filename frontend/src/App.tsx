import { useEffect, useState, type ReactNode } from 'react';
import { Events } from '@wailsio/runtime';
import {
  GameState,
  GameStatus,
  LauncherService,
} from '../bindings/github.com/SDxBacon/idle-lineage-launcher';
import './App.css';

function App() {
  const [gameState, setGameState] = useState<GameState | null>(null);
  const [actionError, setActionError] = useState('');

  useEffect(() => {
    let mounted = true;
    let lastRevision = -1;
    const applyState = (state: GameState) => {
      if (!mounted || state.revision <= lastRevision) {
        return;
      }
      lastRevision = state.revision;
      setGameState(state);
    };
    const unsubscribeState = Events.On('launcher:game-state', event => {
      applyState(event.data);
    });

    LauncherService.GetGameState()
      .then(applyState)
      .catch(error => mounted && setActionError(readError(error)));

    return () => {
      mounted = false;
      unsubscribeState();
    };
  }, []);

  const runAction = (action: () => Promise<void>) => {
    setActionError('');
    void action().catch(error => setActionError(readError(error)));
  };

  if (!gameState) {
    return (
      <StatusShell>
        <LoadingMark />
        <p className="loading-copy">正在讀取遊戲狀態…</p>
        {actionError && <InlineError message={actionError} />}
      </StatusShell>
    );
  }

  const status = gameState.status;
  const installing = status === GameStatus.StatusResolving || status === GameStatus.StatusInstalling;
  const installed = isInstalledState(status);
  const canInstall = status === GameStatus.StatusMissing
    || status === GameStatus.StatusCancelled
    || status === GameStatus.StatusError;
  const canLaunch = status === GameStatus.StatusReady
    || status === GameStatus.StatusChecking
    || status === GameStatus.StatusUpdateAvailable;
  const showProgress = installing
    || status === GameStatus.StatusChecking
    || status === GameStatus.StatusUpdating;

  return (
    <StatusShell>
      <header className="launcher-header">
        <div className="brand-mark" aria-hidden="true">IL</div>
        <div>
          <p className="eyebrow">IDLE LINEAGE LAUNCHER</p>
          <h1>{statusTitle(status)}</h1>
        </div>
        <span className={`status-badge status-${status}`}>{statusLabel(gameState)}</span>
      </header>

      <p className="lead">{gameState.message || statusDescription(status)}</p>

      {installed ? (
        status !== GameStatus.StatusChecking ? <VersionSummary state={gameState} /> : null
      ) : !installing ? (
        <div className="requirements" aria-label="下載需求">
          <div><strong>約 500–800 MB</strong><span>網路下載</span></div>
          <div><strong>至少 1 GB</strong><span>可用磁碟空間</span></div>
        </div>
      ) : null}

      {showProgress && <OperationProgress state={gameState} />}

      {gameState.error && <InlineError message={gameState.error} />}
      {actionError && actionError !== gameState.error && <InlineError message={actionError} />}

      <div className="actions">
        {canInstall && (
          <button
            className="primary-button"
            type="button"
            onClick={() => runAction(() => LauncherService.StartInstall())}
          >
            {status === GameStatus.StatusMissing ? '下載遊戲' : '重試下載'}
          </button>
        )}

        {installing && (
          <button
            className="secondary-button"
            type="button"
            onClick={() => runAction(() => LauncherService.CancelInstall())}
          >
            取消下載
          </button>
        )}

        {installed && (
          <>
            <button
              className="primary-button"
              type="button"
              disabled={!canLaunch}
              onClick={() => runAction(() => LauncherService.LaunchGame())}
            >
              啟動遊戲
            </button>
            <UpdateAction state={gameState} runAction={runAction} />
            <button
              className="secondary-button folder-button"
              type="button"
              onClick={() => runAction(() => LauncherService.OpenGameFolder())}
            >
              遊戲資料夾
            </button>
          </>
        )}
      </div>

    </StatusShell>
  );
}

function StatusShell({ children }: { children: ReactNode }) {
  return <main className="status-shell"><section className="status-card">{children}</section></main>;
}

function VersionSummary({ state }: { state: GameState }) {
  return (
    <dl className="version-summary" aria-label="遊戲版本">
      <div>
        <dt>本機版本</dt>
        <dd title={state.commit || undefined}>
          {formatVersion(state.commit, state.commitTime, '尚未取得')}
        </dd>
      </div>
      <div>
        <dt>遠端版本</dt>
        <dd title={state.remoteCommit || undefined}>
          {formatVersion(state.remoteCommit, state.remoteCommitTime, '尚未檢查')}
        </dd>
      </div>
    </dl>
  );
}

function UpdateAction({
  state,
  runAction,
}: {
  state: GameState;
  runAction: (action: () => Promise<void>) => void;
}) {
  switch (state.status) {
    case GameStatus.StatusReady:
      return (
        <button
          className="secondary-button"
          type="button"
          onClick={() => runAction(() => LauncherService.CheckForUpdate())}
        >
          檢查更新
        </button>
      );
    case GameStatus.StatusChecking:
      return <button className="secondary-button" type="button" disabled>正在檢查更新</button>;
    case GameStatus.StatusUpdateAvailable:
      return (
        <button
          className="secondary-button update-button"
          type="button"
          onClick={() => runAction(() => LauncherService.StartUpdate())}
        >
          立即更新
        </button>
      );
    case GameStatus.StatusUpdating:
      return <button className="secondary-button" type="button" disabled>正在更新遊戲</button>;
    default:
      return null;
  }
}

function OperationProgress({ state }: { state: GameState }) {
  const percentage = state.progressPercent >= 0 ? Math.min(100, state.progressPercent) : null;
  const phase = state.progressPhase || defaultProgressPhase(state.status);
  const detail = state.progressText || state.message || '等待 Git server 回應…';

  return (
    <div className="progress-block" aria-live="polite">
      <div className="progress-heading">
        <span>{phase}</span>
        <strong>{percentage === null ? '進行中' : `${percentage}%`}</strong>
      </div>
      <div
        className={`progress-track active ${percentage === null ? 'indeterminate' : ''}`}
        role="progressbar"
        aria-label={phase}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percentage ?? undefined}
      >
        {percentage !== null && <span style={{ width: `${percentage}%` }} />}
      </div>
      <div className="progress-details">
        <span title={detail}>{detail}</span>
        <span>{formatElapsed(state.progressSeconds)}</span>
      </div>
    </div>
  );
}

function InlineError({ message }: { message: string }) {
  return <p className="inline-error" role="alert">{message}</p>;
}

function LoadingMark() {
  return <span className="loading-mark" aria-hidden="true" />;
}

function statusTitle(status: GameStatus) {
  switch (status) {
    case GameStatus.StatusResolving:
    case GameStatus.StatusInstalling:
      return '正在下載遊戲';
    case GameStatus.StatusReady:
      return '遊戲已就緒';
    case GameStatus.StatusChecking:
      return '正在檢查更新';
    case GameStatus.StatusUpdateAvailable:
      return '有可用更新';
    case GameStatus.StatusUpdating:
      return '正在更新遊戲';
    case GameStatus.StatusCancelled:
    case GameStatus.StatusError:
      return '尚未下載遊戲';
    default:
      return '尚未下載遊戲';
  }
}

function statusLabel(state: GameState) {
  switch (state.status) {
    case GameStatus.StatusResolving:
      return '準備中';
    case GameStatus.StatusInstalling:
      return '下載中';
    case GameStatus.StatusReady:
      return state.remoteCommit
        && state.remoteCommit === state.commit
        && !state.error
        ? '已是最新版本'
        : '可啟動';
    case GameStatus.StatusChecking:
      return '檢查中';
    case GameStatus.StatusUpdateAvailable:
      return '可更新';
    case GameStatus.StatusUpdating:
      return '更新中';
    case GameStatus.StatusCancelled:
      return '已取消';
    case GameStatus.StatusError:
      return '需要處理';
    default:
      return '尚未下載';
  }
}

function statusDescription(status: GameStatus) {
  switch (status) {
    case GameStatus.StatusResolving:
      return '正在確認官方版本與下載位置。';
    case GameStatus.StatusInstalling:
      return '正在從官方 GitHub main 分支下載遊戲。';
    case GameStatus.StatusReady:
      return '可以啟動遊戲，或手動檢查是否有新版本。';
    case GameStatus.StatusChecking:
      return '正在向官方 repository 查詢新版本；目前版本仍可啟動。';
    case GameStatus.StatusUpdateAvailable:
      return '新版本已可下載；更新前仍可啟動目前版本。';
    case GameStatus.StatusUpdating:
      return '正在套用新版本，完成前暫時無法啟動。';
    case GameStatus.StatusCancelled:
      return '上次下載已取消，你可以隨時重新開始。';
    case GameStatus.StatusError:
      return '遊戲內容尚未可用，請查看錯誤後重試下載。';
    default:
      return '尚未下載遊戲。按下下載後，launcher 才會連線取得遊戲內容。';
  }
}

function defaultProgressPhase(status: GameStatus) {
  switch (status) {
    case GameStatus.StatusChecking:
      return 'Fetch repository';
    case GameStatus.StatusUpdating:
      return 'Pull repository';
    case GameStatus.StatusResolving:
      return 'Resolve repository';
    default:
      return 'Clone repository';
  }
}

function shortCommit(commit: string, fallback: string) {
  return commit ? commit.slice(0, 8) : fallback;
}

function formatVersion(commit: string, commitTime: string, fallback: string) {
  if (!commit) return fallback;
  const formattedTime = formatCommitTime(commitTime);
  if (!formattedTime) return shortCommit(commit, fallback);
  return (
    <>
      {shortCommit(commit, fallback)}
      <span className="version-time"> ({formattedTime})</span>
    </>
  );
}

function formatCommitTime(commitTime: string) {
  if (!commitTime) return '';
  const date = new Date(commitTime);
  if (Number.isNaN(date.getTime())) return '';
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

function formatElapsed(seconds: number) {
  if (!Number.isFinite(seconds) || seconds < 1) return '剛剛開始';
  if (seconds < 60) return `已執行 ${Math.floor(seconds)} 秒`;
  const minutes = Math.floor(seconds / 60);
  return `已執行 ${minutes} 分 ${Math.floor(seconds % 60)} 秒`;
}

function readError(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

function isInstalledState(status: GameStatus) {
  return status === GameStatus.StatusReady
    || status === GameStatus.StatusChecking
    || status === GameStatus.StatusUpdateAvailable
    || status === GameStatus.StatusUpdating;
}

export default App;
