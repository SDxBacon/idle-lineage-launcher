import { useEffect, useState, type ReactNode } from 'react';
import { Settings as SettingsIcon } from 'lucide-react';
import { Tooltip } from 'react-tooltip';
import { toast } from 'sonner';
import { Events } from '@wailsio/runtime';
// import components
import SettingsView from '@/SettingsView';
import InlineError from '@/components/InlineError';
import LoadingMark from '@/components/LoadingMark';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog';
import { Toaster } from '@/components/ui/sonner';
import { useGameBrowsers } from '@/hooks/useGameBrowsers';
import { fetchNewerLauncherVersion } from './launcherRelease';
// import store
import { useGameLaunchConfigStore } from '@/stores/useGameLaunchConfigStore';
// import bindings
import {
  GameState,
  GameStatus,
  LauncherService,
  type GameFolderInfo,
  type LauncherInfo,
} from '../bindings/github.com/SDxBacon/idle-lineage-launcher';
// import css
import './App.css';

function App() {
  return (
    <>
      <LauncherDashboard />
      <Toaster
        theme="dark"
        richColors
        closeButton
        position="bottom-right"
        containerAriaLabel="通知"
        toastOptions={{
          closeButtonAriaLabel: '關閉通知',
        }}
      />
    </>
  );
}

type LauncherView = 'dashboard' | 'settings';
type CloseGuard = {
  mode: 'confirm_cancel' | 'blocked';
  progressPhase: string;
};

function LauncherDashboard() {
  const [gameState, setGameState] = useState<GameState | null>(null);
  const [launcherInfo, setLauncherInfo] = useState<LauncherInfo | null>(null);
  const [gameFolderInfo, setGameFolderInfo] = useState<GameFolderInfo | null>(null);
  const [latestLauncherVersion, setLatestLauncherVersion] = useState<string | null>(null);
  const [actionError, setActionError] = useState('');
  const [closeGuard, setCloseGuard] = useState<CloseGuard | null>(null);
  const [closeGuardBusy, setCloseGuardBusy] = useState(false);
  const [closeGuardError, setCloseGuardError] = useState('');
  const [view, setView] = useState<LauncherView>('dashboard');
  const { browsers, loadState } = useGameBrowsers();
  const browserID = useGameLaunchConfigStore(state => state.browserID);
  const setBrowserID = useGameLaunchConfigStore(state => state.setBrowserID);

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
    const unsubscribeCloseGuard = Events.On('launcher:close-guard', event => {
      if (!isCloseGuard(event.data)) {
        return;
      }
      setCloseGuard(event.data);
      setCloseGuardBusy(false);
      setCloseGuardError('');
    });

    LauncherService.GetGameState()
      .then(applyState)
      .catch(error => mounted && setActionError(readError(error)));
    LauncherService.GetLauncherInfo()
      .then(info => mounted && setLauncherInfo(info))
      .catch(error => mounted && setActionError(readError(error)));
    LauncherService.GetGameFolderInfo()
      .then(info => mounted && setGameFolderInfo(info))
      .catch(error => mounted && setActionError(readError(error)));

    return () => {
      mounted = false;
      unsubscribeState();
      unsubscribeCloseGuard();
    };
  }, []);

  const launcherVersion = launcherInfo?.version || '';
  useEffect(() => {
    setLatestLauncherVersion(null);
    if (!launcherVersion) {
      return;
    }

    const controller = new AbortController();
    void fetchNewerLauncherVersion(launcherVersion, controller.signal).then(latestVersion => {
      if (!controller.signal.aborted) {
        setLatestLauncherVersion(latestVersion);
      }
    });

    return () => controller.abort();
  }, [launcherVersion]);

  const runAction = (action: () => Promise<void>) => {
    setActionError('');
    void action().catch(error => setActionError(readError(error)));
  };

  const launchGame = () => {
    setActionError('');
    void LauncherService.LaunchGame(browserID)
      .then(result => {
        if (!result.fallbackToDefault) return;

        setBrowserID(null);
        toast.warning('所選瀏覽器無法開啟遊戲', {
          id: 'game-launch-browser-fallback',
          description: '已改用系統預設瀏覽器開啟，並重設瀏覽器選擇。',
          duration: Infinity,
          closeButton: true,
        });
      })
      .catch(error => setActionError(readError(error)));
  };

  const cancelUpdateAndClose = () => {
    setCloseGuardBusy(true);
    setCloseGuardError('');
    void LauncherService.CancelUpdateAndClose()
      .then(() => setCloseGuard(null))
      .catch(error => setCloseGuardError(readError(error)))
      .finally(() => setCloseGuardBusy(false));
  };

  if (!gameState) {
    return (
      <StatusShell
        launcherInfo={launcherInfo}
        latestLauncherVersion={latestLauncherVersion}
        onOpenLauncherRepository={() => runAction(() => LauncherService.OpenLauncherRepository())}
      >
        <LoadingMark />
        <p className="loading-copy">正在讀取遊戲狀態…</p>
        {actionError && <InlineError message={actionError} />}
      </StatusShell>
    );
  }

  const status = gameState.status;
  const installing =
    status === GameStatus.StatusResolving || status === GameStatus.StatusInstalling;
  const installed = isInstalledState(status);
  const canInstall =
    status === GameStatus.StatusMissing ||
    status === GameStatus.StatusCancelled ||
    status === GameStatus.StatusError;
  const canLaunch =
    status === GameStatus.StatusReady ||
    status === GameStatus.StatusChecking ||
    status === GameStatus.StatusUpdateAvailable;
  const showProgress =
    installing ||
    status === GameStatus.StatusChecking ||
    status === GameStatus.StatusUpdating ||
    status === GameStatus.StatusRecovering ||
    status === GameStatus.StatusMoving;
  const openGameRepository = () => runAction(() => LauncherService.OpenGameRepository());
  const recheckGameFolder = () =>
    runAction(async () => {
      await LauncherService.RecheckGameFolder();
      setGameFolderInfo(await LauncherService.GetGameFolderInfo());
    });

  return (
    <StatusShell
      launcherInfo={launcherInfo}
      latestLauncherVersion={latestLauncherVersion}
      onOpenLauncherRepository={() => runAction(() => LauncherService.OpenLauncherRepository())}
    >
      <LauncherHeader
        title={view === 'settings' ? '啟動器設置頁' : statusTitle(status)}
        launcherInfo={launcherInfo}
        onOpenGameRepository={openGameRepository}
      />

      {view === 'settings' ? (
        <SettingsView
          browsers={browsers}
          loadState={loadState}
          actionError={actionError}
          gameState={gameState}
          folderInfo={gameFolderInfo}
          onFolderInfoChange={setGameFolderInfo}
          onBack={() => setView('dashboard')}
        />
      ) : (
        <>
          <p className="lead">{gameState.message || statusDescription(status)}</p>

          {status === GameStatus.StatusStorageUnavailable && gameFolderInfo && (
            <div className="mt-4 rounded-xl border border-amber-300/20 bg-amber-300/5 p-4">
              <p className="m-0 text-xs text-amber-100/80">目前設定的儲存位置</p>
              <p className="mt-2 mb-0 select-text break-all font-mono text-sm text-amber-50">
                {gameFolderInfo.root}
              </p>
            </div>
          )}

          {installed ? (
            status !== GameStatus.StatusChecking ? (
              <VersionSummary state={gameState} />
            ) : null
          ) : canInstall ? (
            <div className="requirements" aria-label="下載需求">
              <div>
                <strong>約 500–800 MB</strong>
                <span>網路下載</span>
              </div>
              <div>
                <strong>至少 1 GB</strong>
                <span>可用磁碟空間</span>
              </div>
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

            {status === GameStatus.StatusUpdating && gameState.progressCancellable && (
              <button
                className="secondary-button"
                type="button"
                onClick={() => runAction(() => LauncherService.CancelUpdate())}
              >
                取消更新
              </button>
            )}

            {status === GameStatus.StatusStorageUnavailable && (
              <>
                <button className="primary-button" type="button" onClick={recheckGameFolder}>
                  重新檢查
                </button>
                <button
                  className="secondary-button"
                  type="button"
                  onClick={() => setView('settings')}
                >
                  前往設定
                </button>
              </>
            )}

            {installed && (
              <>
                <button
                  className="primary-button"
                  type="button"
                  disabled={!canLaunch}
                  onClick={launchGame}
                >
                  啟動遊戲
                </button>
                <UpdateAction state={gameState} runAction={runAction} />
              </>
            )}
            <div className="folder-settings-actions">
              {installed && (
                <button
                  className="secondary-button folder-button"
                  type="button"
                  disabled={status === GameStatus.StatusMoving}
                  onClick={() => runAction(() => LauncherService.OpenGameFolder())}
                >
                  遊戲資料夾
                </button>
              )}
              <button
                className="secondary-button settings-button"
                type="button"
                aria-label="開啟啟動器設置頁"
                title="開啟啟動器設置頁"
                disabled={
                  status === GameStatus.StatusUpdating || status === GameStatus.StatusRecovering
                }
                onClick={() => setView('settings')}
              >
                <SettingsIcon aria-hidden="true" />
              </button>
            </div>
          </div>
        </>
      )}

      <UpdateCloseGuardDialog
        guard={closeGuard}
        recovering={status === GameStatus.StatusRecovering}
        busy={closeGuardBusy}
        error={closeGuardError}
        onContinue={() => setCloseGuard(null)}
        onCancelAndClose={cancelUpdateAndClose}
      />
    </StatusShell>
  );
}

function LauncherHeader({
  title,
  launcherInfo,
  onOpenGameRepository,
}: {
  title: string;
  launcherInfo: LauncherInfo | null;
  onOpenGameRepository: () => void;
}) {
  return (
    <header className="launcher-header">
      <div className="brand-mark" aria-hidden="true">
        IL
      </div>
      <div>
        <p className="eyebrow">IDLE LINEAGE LAUNCHER</p>
        <h1>{title}</h1>
      </div>
      <div className="status-badge-field">
        <span className="status-badge-label">遊戲來源</span>
        <button
          className="status-badge"
          type="button"
          disabled={!launcherInfo?.gameRepository}
          aria-label={
            launcherInfo?.gameRepository
              ? `在 GitHub 開啟 ${launcherInfo.gameRepository}`
              : '正在讀取遊戲 repository'
          }
          onClick={onOpenGameRepository}
        >
          {launcherInfo?.gameRepository || '—'}
        </button>
      </div>
    </header>
  );
}

function StatusShell({
  children,
  launcherInfo,
  latestLauncherVersion,
  onOpenLauncherRepository,
}: {
  children: ReactNode;
  launcherInfo: LauncherInfo | null;
  latestLauncherVersion: string | null;
  onOpenLauncherRepository: () => void;
}) {
  return (
    <main className="status-shell">
      <div className="status-layout">
        <section className="status-card">{children}</section>
        <LauncherFooter
          version={launcherInfo?.version || ''}
          latestVersion={latestLauncherVersion}
          onOpenLauncherRepository={onOpenLauncherRepository}
        />
      </div>
    </main>
  );
}

function LauncherFooter({
  version,
  latestVersion,
  onOpenLauncherRepository,
}: {
  version: string;
  latestVersion: string | null;
  onOpenLauncherRepository: () => void;
}) {
  const updateTooltipID = 'launcher-update-tooltip';
  const updateMessage = latestVersion ? `有更新版本 v${latestVersion} 可供下載` : '';

  return (
    <>
      <footer className="launcher-footer">
        <span>{version ? `v${version}` : 'v—'}</span>
        <span aria-hidden="true">·</span>
        <span>SDxBacon</span>
        <span aria-hidden="true">·</span>
        <button
          className="github-button"
          type="button"
          aria-label={
            updateMessage
              ? `在 GitHub 開啟 Idle Lineage Launcher；${updateMessage}`
              : '在 GitHub 開啟 Idle Lineage Launcher'
          }
          data-tooltip-id={latestVersion ? updateTooltipID : undefined}
          data-tooltip-content={latestVersion ? updateMessage : undefined}
          onClick={onOpenLauncherRepository}
        >
          <span className="relative flex h-4 w-4">
            <svg viewBox="0 0 16 16" aria-hidden="true">
              <path d="M8 0C3.58 0 0 3.64 0 8.13c0 3.59 2.29 6.63 5.47 7.71.4.08.55-.18.55-.39 0-.19-.01-.83-.01-1.5-2.01.38-2.53-.5-2.69-.96-.09-.23-.48-.96-.82-1.15-.28-.15-.68-.53-.01-.54.63-.01 1.08.59 1.23.83.72 1.23 1.87.88 2.33.67.07-.53.28-.88.51-1.08-1.78-.21-3.64-.91-3.64-4.02 0-.89.31-1.62.82-2.19-.08-.2-.36-1.04.08-2.16 0 0 .67-.22 2.2.84A7.5 7.5 0 0 1 8 3.88a7.5 7.5 0 0 1 2 .28c1.53-1.06 2.2-.84 2.2-.84.44 1.12.16 1.96.08 2.16.51.57.82 1.3.82 2.19 0 3.12-1.87 3.81-3.65 4.02.29.25.54.74.54 1.51 0 1.09-.01 1.97-.01 2.24 0 .22.15.47.55.39A8.12 8.12 0 0 0 16 8.13C16 3.64 12.42 0 8 0Z" />
            </svg>
            {latestVersion && (
              <span
                className="absolute -top-1 -right-1 flex h-2 w-2"
                aria-hidden="true"
                data-testid="launcher-update-indicator"
              >
                <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-red-400 opacity-75 motion-reduce:animate-none" />
                <span className="relative inline-flex h-2 w-2 animate-pulse rounded-full bg-red-500 motion-reduce:animate-none" />
              </span>
            )}
          </span>
        </button>
      </footer>
      {latestVersion && <Tooltip id={updateTooltipID} place="top" />}
    </>
  );
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
      return (
        <button className="secondary-button" type="button" disabled>
          正在檢查更新
        </button>
      );
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
      return (
        <button className="secondary-button" type="button" disabled>
          正在更新遊戲
        </button>
      );
    case GameStatus.StatusRecovering:
      return (
        <button className="secondary-button" type="button" disabled>
          正在復原遊戲
        </button>
      );
    case GameStatus.StatusRecoveryFailed:
      return (
        <button
          className="secondary-button update-button"
          type="button"
          onClick={() => runAction(() => LauncherService.RetryUpdateRecovery())}
        >
          重試復原
        </button>
      );
    default:
      return null;
  }
}

function OperationProgress({ state }: { state: GameState }) {
  const percentage = state.progressPercent >= 0 ? Math.min(100, state.progressPercent) : null;
  const phase = state.progressPhase || defaultProgressPhase(state.status);
  const detail = state.progressText || state.message || '等待更新伺服器回應…';
  const stepLabels = progressStepLabels(state.status);
  const stepTotal = stepLabels
    ? state.progressStepTotal === stepLabels.length
      ? state.progressStepTotal
      : stepLabels.length
    : 0;
  const currentStep = stepLabels ? clampStep(state.progressStep, stepTotal) : 0;
  const phaseHeading = stepLabels ? `步驟 ${currentStep}/${stepTotal} · ${phase}` : phase;
  const critical =
    state.status === GameStatus.StatusRecovering ||
    (state.status === GameStatus.StatusUpdating && !state.progressCancellable);

  return (
    <div className="progress-block">
      {stepLabels && (
        <ol
          className="progress-stepper"
          aria-label={state.status === GameStatus.StatusRecovering ? '復原進度' : '更新進度'}
        >
          {stepLabels.map((label, index) => {
            const step = index + 1;
            const stateName =
              step < currentStep ? 'complete' : step === currentStep ? 'current' : 'pending';
            return (
              <li
                key={label}
                className={`progress-step ${stateName}`}
                aria-current={stateName === 'current' ? 'step' : undefined}
              >
                <span className="progress-step-marker" aria-hidden="true">
                  {stateName === 'complete' ? '✓' : step}
                </span>
                <span>{label}</span>
              </li>
            );
          })}
        </ol>
      )}
      <div className="progress-heading">
        <span aria-live="polite">{phaseHeading}</span>
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
        <span title={detail} aria-live="polite">
          {detail}
        </span>
        <span className="progress-elapsed" aria-live="off">
          {formatElapsed(state.progressSeconds)}
        </span>
      </div>
      {critical && <p className="progress-critical">正在保護遊戲檔案，請保持 Launcher 開啟</p>}
    </div>
  );
}

function UpdateCloseGuardDialog({
  guard,
  recovering,
  busy,
  error,
  onContinue,
  onCancelAndClose,
}: {
  guard: CloseGuard | null;
  recovering: boolean;
  busy: boolean;
  error: string;
  onContinue: () => void;
  onCancelAndClose: () => void;
}) {
  const confirmCancel = guard?.mode === 'confirm_cancel';

  return (
    <AlertDialog
      open={guard !== null}
      onOpenChange={open => {
        if (!open && !busy) onContinue();
      }}
    >
      <AlertDialogContent
        onEscapeKeyDown={event => {
          if (busy) event.preventDefault();
        }}
      >
        <AlertDialogHeader>
          <AlertDialogTitle>
            {confirmCancel ? '更新仍在下載' : recovering ? '正在復原遊戲' : '正在套用更新'}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {confirmCancel
              ? '取消更新不會影響目前可用的遊戲版本。'
              : recovering
                ? '為避免遊戲檔案不完整，復原完成前無法關閉 Launcher。'
                : '為避免遊戲檔案不完整，套用與驗證完成前無法關閉 Launcher。'}
          </AlertDialogDescription>
          {guard?.progressPhase && (
            <p className="close-guard-phase">目前進度：{guard.progressPhase}</p>
          )}
        </AlertDialogHeader>

        {error && <InlineError message={error} />}

        <AlertDialogFooter>
          <AlertDialogCancel asChild>
            <button className="secondary-button" type="button" disabled={busy}>
              {confirmCancel ? '繼續更新' : '繼續等待'}
            </button>
          </AlertDialogCancel>
          {confirmCancel && (
            <AlertDialogAction asChild>
              <button
                className="primary-button"
                type="button"
                disabled={busy}
                onClick={event => {
                  event.preventDefault();
                  onCancelAndClose();
                }}
              >
                {busy ? '正在取消…' : '取消更新並關閉'}
              </button>
            </AlertDialogAction>
          )}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
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
    case GameStatus.StatusRecovering:
      return '正在復原遊戲';
    case GameStatus.StatusRecoveryFailed:
      return '無法復原遊戲';
    case GameStatus.StatusMoving:
      return '正在搬移遊戲';
    case GameStatus.StatusStorageUnavailable:
      return '遊戲儲存位置無法使用';
    case GameStatus.StatusCancelled:
    case GameStatus.StatusError:
      return '尚未下載遊戲';
    default:
      return '尚未下載遊戲';
  }
}

function statusDescription(status: GameStatus) {
  switch (status) {
    case GameStatus.StatusResolving:
      return '正在確認官方版本與下載位置。';
    case GameStatus.StatusInstalling:
      return '正在從官方伺服器下載遊戲。';
    case GameStatus.StatusReady:
      return '可以啟動遊戲，或手動檢查是否有新版本。';
    case GameStatus.StatusChecking:
      return '正在查詢官方新版本；目前版本仍可啟動。';
    case GameStatus.StatusUpdateAvailable:
      return '新版本已可下載；更新前仍可啟動目前版本。';
    case GameStatus.StatusUpdating:
      return '正在套用新版本，完成前暫時無法啟動。';
    case GameStatus.StatusRecovering:
      return '偵測到上次更新未完成，正在自動復原可用版本。';
    case GameStatus.StatusRecoveryFailed:
      return '無法完成自動復原，請查看錯誤後重試。';
    case GameStatus.StatusMoving:
      return '正在將已安裝的遊戲搬移至新位置。';
    case GameStatus.StatusStorageUnavailable:
      return '請重新連接磁碟、恢復資料夾權限，或前往設定檢查位置。';
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
      return '檢查官方版本';
    case GameStatus.StatusUpdating:
      return '同步官方版本';
    case GameStatus.StatusRecovering:
      return '復原可用版本';
    case GameStatus.StatusMoving:
      return '搬移遊戲';
    case GameStatus.StatusResolving:
      return '確認官方版本';
    default:
      return '下載官方版本';
  }
}

function progressStepLabels(status: GameStatus) {
  if (status === GameStatus.StatusUpdating) {
    return ['連線 GitHub', '下載更新檔案', '套用遊戲版本', '驗證遊戲檔案'];
  }
  if (status === GameStatus.StatusRecovering) {
    return ['復原可用版本', '驗證遊戲檔案'];
  }
  return null;
}

function clampStep(step: number, total: number) {
  if (!Number.isFinite(step) || step < 1) return 1;
  return Math.min(total, Math.floor(step));
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
      <span className="version-separator" aria-hidden="true">
        ·
      </span>
      <span className="version-time">{formattedTime}</span>
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

function isCloseGuard(value: unknown): value is CloseGuard {
  if (!value || typeof value !== 'object') return false;
  const candidate = value as Partial<CloseGuard>;
  return (
    (candidate.mode === 'confirm_cancel' || candidate.mode === 'blocked') &&
    typeof candidate.progressPhase === 'string'
  );
}

function isInstalledState(status: GameStatus) {
  return (
    status === GameStatus.StatusReady ||
    status === GameStatus.StatusChecking ||
    status === GameStatus.StatusUpdateAvailable ||
    status === GameStatus.StatusUpdating ||
    status === GameStatus.StatusRecovering ||
    status === GameStatus.StatusRecoveryFailed ||
    status === GameStatus.StatusMoving
  );
}

export default App;
