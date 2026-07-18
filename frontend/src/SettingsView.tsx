import { useLayoutEffect, useRef, useState } from 'react';
import { FolderOpen, RotateCcw } from 'lucide-react';
import { Tooltip } from 'react-tooltip';
import { toast } from 'sonner';

import InlineError from '@/components/InlineError';
import { GameLaunchSelect } from '@/components/GameLaunchSelect';
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { type GameBrowserOptions } from '@/hooks/useGameBrowsers';
import {
  GameStatus,
  LauncherService,
  type GameFolderChangeResult,
  type GameFolderInfo,
  type GameState,
} from '../bindings/github.com/SDxBacon/idle-lineage-launcher';

type FolderAction = 'select' | 'restore' | 'move' | 'recheck' | null;

function SettingsView({
  browsers,
  loadState,
  actionError,
  gameState,
  folderInfo,
  onFolderInfoChange,
  onBack,
}: GameBrowserOptions & {
  actionError: string;
  gameState: GameState;
  folderInfo: GameFolderInfo | null;
  onFolderInfoChange: (info: GameFolderInfo) => void;
  onBack: () => void;
}) {
  const [folderAction, setFolderAction] = useState<FolderAction>(null);
  const [folderError, setFolderError] = useState('');
  const [pendingChange, setPendingChange] = useState<GameFolderChangeResult | null>(null);
  const [isGamePathOverflowing, setIsGamePathOverflowing] = useState(false);
  const gamePathRef = useRef<HTMLSpanElement>(null);
  const gamePath = folderInfo?.gamePath || '—';
  const gamePathTooltipID = 'game-folder-path-tooltip';

  useLayoutEffect(() => {
    const element = gamePathRef.current;
    if (!element) return;

    const updateOverflowState = () => {
      setIsGamePathOverflowing(element.scrollWidth > element.clientWidth);
    };
    const resizeObserver = new ResizeObserver(updateOverflowState);

    updateOverflowState();
    resizeObserver.observe(element);

    return () => resizeObserver.disconnect();
  }, [gamePath]);

  const folderLocked =
    gameState.status === GameStatus.StatusResolving ||
    gameState.status === GameStatus.StatusInstalling ||
    gameState.status === GameStatus.StatusChecking ||
    gameState.status === GameStatus.StatusUpdating ||
    gameState.status === GameStatus.StatusMoving;
  const busy = folderAction !== null;

  const refreshFolderInfo = async () => {
    const info = await LauncherService.GetGameFolderInfo();
    onFolderInfoChange(info);
  };

  const applyChangeResult = async (result: GameFolderChangeResult) => {
    if (result.cancelled) return;
    if (result.requiresMove) {
      setPendingChange(result);
      return;
    }
    if (result.applied) {
      await refreshFolderInfo();
      toast.success('遊戲資料夾已更新');
    }
  };

  const chooseFolder = async () => {
    setFolderAction('select');
    setFolderError('');
    try {
      await applyChangeResult(await LauncherService.SelectGameFolder());
    } catch (error) {
      setFolderError(readError(error));
    } finally {
      setFolderAction(null);
    }
  };

  const restoreDefault = async () => {
    setFolderAction('restore');
    setFolderError('');
    try {
      await applyChangeResult(await LauncherService.RestoreDefaultGameFolder());
    } catch (error) {
      setFolderError(readError(error));
    } finally {
      setFolderAction(null);
    }
  };

  const confirmMove = async () => {
    if (!pendingChange) return;
    setFolderAction('move');
    setFolderError('');
    try {
      await LauncherService.ConfirmGameFolderMove(pendingChange.root);
      await refreshFolderInfo();
      setPendingChange(null);
      toast.success('遊戲已搬移至新位置');
    } catch (error) {
      setFolderError(readError(error));
    } finally {
      setFolderAction(null);
    }
  };

  const recheckFolder = async () => {
    setFolderAction('recheck');
    setFolderError('');
    try {
      await LauncherService.RecheckGameFolder();
      await refreshFolderInfo();
      toast.success('已重新檢查遊戲資料夾');
    } catch (error) {
      setFolderError(readError(error));
    } finally {
      setFolderAction(null);
    }
  };

  const movePercent =
    gameState.status === GameStatus.StatusMoving && gameState.progressPercent >= 0
      ? Math.min(100, gameState.progressPercent)
      : null;

  return (
    <div className="mt-7">
      <GameLaunchSelect browsers={browsers} loadState={loadState} />
      <section
        className="mt-3.5 rounded-[14px] border border-white/8 bg-white/2.5 p-[17px]"
        aria-labelledby="game-folder-settings-title"
      >
        <div className="flex items-start justify-between gap-4">
          <div>
            <h2
              id="game-folder-settings-title"
              className="m-0 text-sm font-semibold text-slate-200"
            >
              遊戲資料夾
            </h2>
            <p className="mt-1.5 mb-0 text-xs leading-5 text-slate-400">
              儲存遊戲檔案的根目錄
            </p>
          </div>
          {gameState.status === GameStatus.StatusStorageUnavailable && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => void recheckFolder()}
            >
              <RotateCcw aria-hidden="true" />
              {folderAction === 'recheck' ? '檢查中…' : '重新檢查'}
            </Button>
          )}
        </div>

        {/* <label className="mt-4 block text-xs font-medium text-slate-300" htmlFor="game-folder-root">
          儲存位置
        </label> */}
        <Input
          id="game-folder-root"
          className="mt-2 select-text font-mono text-xs"
          value={folderInfo?.root || ''}
          readOnly
          aria-describedby="game-folder-path-description"
        />
        <p
          id="game-folder-path-description"
          className="mt-2 mb-0 flex min-w-0 items-baseline gap-1 text-xs text-slate-400"
        >
          <span className="shrink-0">完整路徑：</span>
          <span
            ref={gamePathRef}
            className="min-w-0 flex-1 select-text truncate text-left font-mono text-slate-300 [direction:rtl]"
            data-tooltip-id={isGamePathOverflowing ? gamePathTooltipID : undefined}
            data-tooltip-content={isGamePathOverflowing ? gamePath : undefined}
          >
            {gamePath}
          </span>
        </p>
        {isGamePathOverflowing && (
          <Tooltip
            id={gamePathTooltipID}
            place="bottom"
            border="1px solid #7b828c"
            arrowColor="#59616c"
            style={{
              backgroundColor: '#59616c',
              color: '#f8fafc',
              boxShadow: '0 6px 18px rgba(0, 0, 0, 0.45)',
              zIndex: 100,
            }}
          />
        )}

        {folderLocked && (
          <p className="mt-3 mb-0 text-xs text-amber-200/80">
            請等待目前的遊戲作業完成，或先取消下載。
          </p>
        )}
        {folderError && (
          <div className="mt-3">
            <InlineError message={folderError} />
          </div>
        )}

        <div className="mt-4 flex flex-wrap justify-end gap-2">
          <Button
            type="button"
            variant="outline"
            className="disabled:pointer-events-auto disabled:cursor-not-allowed disabled:border-slate-700 disabled:bg-slate-800 disabled:text-slate-500 disabled:opacity-100 disabled:shadow-none"
            disabled={busy || folderLocked || !folderInfo || folderInfo.isDefault}
            onClick={() => void restoreDefault()}
          >
            <RotateCcw aria-hidden="true" />
            {folderAction === 'restore' ? '處理中…' : '恢復預設位置'}
          </Button>
          <Button
            type="button"
            disabled={busy || folderLocked || !folderInfo}
            onClick={() => void chooseFolder()}
          >
            <FolderOpen aria-hidden="true" />
            {folderAction === 'select' ? '選擇中…' : '選擇資料夾'}
          </Button>
        </div>
      </section>

      {actionError && <InlineError message={actionError} />}
      <div className="mt-7 flex justify-end">
        <Button type="button" variant="outline" onClick={onBack}>
          返回
        </Button>
      </div>

      <AlertDialog
        open={pendingChange !== null}
        onOpenChange={open => {
          if (!open && folderAction !== 'move') {
            setPendingChange(null);
            setFolderError('');
          }
        }}
      >
        <AlertDialogContent
          onEscapeKeyDown={event => {
            if (folderAction === 'move') event.preventDefault();
          }}
        >
          <AlertDialogHeader>
            <AlertDialogTitle>變更遊戲資料夾</AlertDialogTitle>
            <AlertDialogDescription asChild>
              <div>
                <p className="m-0">已安裝的遊戲將從</p>
                <p className="my-2 select-text break-all rounded-md bg-black/25 px-3 py-2 font-mono text-xs text-slate-200">
                  {pendingChange?.currentGamePath}
                </p>
                <p className="m-0">搬移至</p>
                <p className="my-2 select-text break-all rounded-md bg-black/25 px-3 py-2 font-mono text-xs text-slate-200">
                  {pendingChange?.gamePath}
                </p>
                <p className="mt-3 mb-0">
                  搬移完成後，舊位置將不再保留遊戲檔案。搬移期間無法啟動或更新遊戲；若不希望搬移，請返回。
                </p>
              </div>
            </AlertDialogDescription>
          </AlertDialogHeader>

          {folderAction === 'move' && (
            <div
              className="rounded-lg border border-primary/20 bg-primary/5 p-3"
              aria-live="polite"
            >
              <div className="flex items-center justify-between text-xs text-slate-300">
                <span>{gameState.progressPhase || '正在搬移遊戲'}</span>
                <strong>{movePercent === null ? '進行中' : `${movePercent}%`}</strong>
              </div>
              <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-black/30">
                <div
                  className={`h-full rounded-full bg-primary transition-[width] ${movePercent === null ? 'w-1/3 animate-pulse' : ''}`}
                  style={movePercent === null ? undefined : { width: `${movePercent}%` }}
                  role="progressbar"
                  aria-label="搬移遊戲"
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={movePercent ?? undefined}
                />
              </div>
              <p className="mt-2 mb-0 text-xs text-slate-400">
                {gameState.progressText || '正在準備遊戲檔案…'}
              </p>
            </div>
          )}
          {pendingChange && folderError && <InlineError message={folderError} />}

          <AlertDialogFooter className="justify-between">
            <AlertDialogCancel asChild>
              <Button type="button" variant="ghost" disabled={folderAction === 'move'}>
                返回
              </Button>
            </AlertDialogCancel>
            <Button
              type="button"
              disabled={folderAction === 'move'}
              onClick={() => void confirmMove()}
            >
              {folderAction === 'move' ? '搬移中…' : '確認'}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function readError(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

export default SettingsView;
