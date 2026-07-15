import { useEffect, useMemo, useRef, useState } from 'react';
import { Events, Window } from '@wailsio/runtime';
import {
  GameState,
  GameStatus,
  LauncherService,
} from '../bindings/github.com/SDxBacon/idle-lineage-launcher';
import { Dock, type DockItemData } from './components/Dock/Dock';
import './App.css';

function App() {
  const [gameState, setGameState] = useState<GameState | null>(null);
  const [reloadKey, setReloadKey] = useState(0);
  const [frameLoaded, setFrameLoaded] = useState(false);
  const [actionError, setActionError] = useState('');
  const frameRef = useRef<HTMLIFrameElement>(null);

  useEffect(() => {
    let mounted = true;
    const unsubscribeState = Events.On('launcher:game-state', event => {
      if (mounted) {
        setGameState(event.data);
        setActionError('');
      }
    });
    const unsubscribeReload = Events.On('launcher:reload-game', () => {
      if (mounted) {
        setFrameLoaded(false);
        setReloadKey(value => value + 1);
      }
    });

    LauncherService.GetGameState()
      .then(state => mounted && setGameState(state))
      .catch(error => mounted && setActionError(readError(error)));

    return () => {
      mounted = false;
      unsubscribeState();
      unsubscribeReload();
    };
  }, []);

  useEffect(() => {
    if (gameState?.status !== GameStatus.StatusReady) return;
    const timer = window.setInterval(() => {
      try {
        const frame = frameRef.current;
        const location = frame?.contentWindow?.location;
        const readyState = frame?.contentDocument?.readyState;
        if (location?.pathname.startsWith('/game/') && readyState && readyState !== 'loading') {
          setFrameLoaded(true);
          window.clearInterval(timer);
        }
      } catch {
        // The game is intentionally same-origin. If a platform briefly reports
        // otherwise during navigation, the iframe's load event remains the fallback.
      }
    }, 100);
    return () => window.clearInterval(timer);
  }, [gameState?.status, gameState?.commit, reloadKey]);

  const dockItems = useMemo<DockItemData[]>(() => [
    {
      label: '新增視窗',
      icon: <NewWindowIcon />,
      onClick: () => void LauncherService.CreateGameWindow().catch(error => setActionError(readError(error))),
    },
    {
      label: '重新載入遊戲',
      icon: <ReloadIcon />,
      onClick: () => {
        setFrameLoaded(false);
        setReloadKey(value => value + 1);
      },
    },
    {
      label: '切換全螢幕',
      icon: <FullscreenIcon />,
      onClick: () => void Window.ToggleFullscreen().catch(error => setActionError(readError(error))),
    },
  ], []);

  if (!gameState) {
    return <StatusShell><LoadingMark /><p>正在讀取遊戲狀態…</p>{actionError && <InlineError message={actionError} />}</StatusShell>;
  }

  if (gameState.status === GameStatus.StatusReady) {
    const source = `/game/index.html?version=${encodeURIComponent(gameState.commit)}`;
    return (
      <main className="game-shell">
        {!frameLoaded && <div className="frame-loading"><LoadingMark /><span>正在載入遊戲…</span></div>}
        <iframe
          ref={frameRef}
          key={`${gameState.commit}-${reloadKey}`}
          className="game-frame"
          src={source}
          title="Idle Lineage Class"
          onLoad={() => setFrameLoaded(true)}
          allow="autoplay; fullscreen"
        />
        <Dock items={dockItems} />
        {actionError && <div className="toast" role="alert">{actionError}</div>}
      </main>
    );
  }

  const busy = gameState.status === GameStatus.StatusResolving || gameState.status === GameStatus.StatusInstalling;
  const canInstall = gameState.status === GameStatus.StatusMissing
    || gameState.status === GameStatus.StatusCancelled
    || gameState.status === GameStatus.StatusError;

  return (
    <StatusShell>
      <div className="brand-mark" aria-hidden="true">IL</div>
      <p className="eyebrow">IDLE LINEAGE LAUNCHER</p>
      <h1>{busy ? '正在安裝遊戲內容' : gameState.status === GameStatus.StatusError ? '安裝未完成' : '準備安裝遊戲'}</h1>
      <p className="lead">
        {busy
          ? gameState.message
          : '首次啟動會直接從官方 GitHub main 分支下載。完成後遊戲保存在應用程式資料目錄，可離線啟動。'}
      </p>

      {busy ? (
        <InstallProgress state={gameState} />
      ) : (
        <div className="requirements" aria-label="安裝需求">
          <div><strong>約 500–800 MB</strong><span>網路下載</span></div>
          <div><strong>至少 1 GB</strong><span>可用磁碟空間</span></div>
        </div>
      )}

      {(gameState.error || actionError) && <InlineError message={gameState.error || actionError} />}

      <div className="actions">
        {canInstall && (
          <button className="primary-button" type="button" onClick={() => {
            setActionError('');
            void LauncherService.StartInstall().catch(error => setActionError(readError(error)));
          }}>
            {gameState.status === GameStatus.StatusError ? '重試安裝' : '下載並安裝'}
          </button>
        )}
        {busy && (
          <button className="secondary-button" type="button" onClick={() => {
            setActionError('');
            void LauncherService.CancelInstall().catch(error => setActionError(readError(error)));
          }}>
            取消
          </button>
        )}
      </div>
      <p className="privacy-note">不需要 Git 或 GitHub CLI；下載內容不會寫入 launcher 安裝包。</p>
    </StatusShell>
  );
}

function StatusShell({ children }: { children: React.ReactNode }) {
  return <main className="status-shell"><section className="status-card">{children}</section></main>;
}

function InstallProgress({ state }: { state: GameState }) {
  const percentage = state.totalBytes > 0
    ? Math.min(100, Math.round((state.receivedBytes / state.totalBytes) * 100))
    : null;

  return (
    <div className="progress-block" aria-live="polite">
      <div className="progress-heading">
        <span>{state.status === GameStatus.StatusResolving ? '確認版本' : '下載與解壓'}</span>
        <strong>{percentage === null ? '進行中' : `${percentage}%`}</strong>
      </div>
      <div
        className={`progress-track ${percentage === null ? 'indeterminate' : ''}`}
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percentage ?? undefined}
      >
        {percentage !== null && <span style={{ width: `${percentage}%` }} />}
      </div>
      <div className="progress-details">
        <span>{formatBytes(state.receivedBytes)}{state.totalBytes > 0 ? ` / ${formatBytes(state.totalBytes)}` : ' 已接收'}</span>
        <span>{state.filesExtracted.toLocaleString()} 個檔案已解壓</span>
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

function formatBytes(bytes: number) {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  const unit = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / 1024 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function readError(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

function NewWindowIcon() {
  return <svg viewBox="0 0 24 24" fill="none" strokeWidth="1.8"><rect x="3.5" y="5.5" width="13" height="13" rx="2"/><path d="M8 5.5V4.8A1.8 1.8 0 0 1 9.8 3h8.4A1.8 1.8 0 0 1 20 4.8v8.4a1.8 1.8 0 0 1-1.8 1.8h-1.7M10 9v6M7 12h6"/></svg>;
}

function ReloadIcon() {
  return <svg viewBox="0 0 24 24" fill="none" strokeWidth="1.8"><path d="M19 7v5h-5M5.4 17a8 8 0 0 0 13.1-2M5 12A8 8 0 0 1 18.5 7L19 7"/></svg>;
}

function FullscreenIcon() {
  return <svg viewBox="0 0 24 24" fill="none" strokeWidth="1.8"><path d="M8.5 4H4v4.5M15.5 4H20v4.5M20 15.5V20h-4.5M8.5 20H4v-4.5"/></svg>;
}

export default App;
