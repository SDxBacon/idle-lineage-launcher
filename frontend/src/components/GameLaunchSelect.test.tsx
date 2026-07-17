import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import { useGameLaunchConfigStore } from '@/stores/useGameLaunchConfigStore';
import { GameLaunchSelect } from './GameLaunchSelect';

const browsers = [
  { id: 'macos:com.apple.Safari', name: 'Safari' },
  { id: 'macos:com.google.Chrome', name: 'Google Chrome' },
];

describe('GameLaunchSelect', () => {
  beforeEach(() => {
    localStorage.clear();
    useGameLaunchConfigStore.setState({ browserID: null });
  });

  afterEach(cleanup);

  it('shows system default first and persists a selected installed browser', async () => {
    render(<GameLaunchSelect browsers={browsers} loadState="ready" />);

    const trigger = screen.getByRole('combobox', { name: '遊戲瀏覽器' });
    expect(trigger).toBeEnabled();
    expect(trigger).toHaveTextContent('系統預設');
    expect(screen.getByText('遊戲存檔會依瀏覽器分開保存。')).toBeInTheDocument();

    fireEvent.click(trigger);
    const options = await screen.findAllByRole('option');
    expect(options.map(option => option.textContent)).toEqual([
      '系統預設',
      'Safari',
      'Google Chrome',
    ]);
    fireEvent.click(screen.getByRole('option', { name: 'Google Chrome' }));

    await waitFor(() => {
      expect(useGameLaunchConfigStore.getState().browserID).toBe('macos:com.google.Chrome');
    });
    expect(trigger).toHaveTextContent('Google Chrome');
  });

  it('renders the browser selected in the store', () => {
    useGameLaunchConfigStore.getState().setBrowserID('macos:com.apple.Safari');

    render(<GameLaunchSelect browsers={browsers} loadState="ready" />);

    expect(screen.getByRole('combobox', { name: '遊戲瀏覽器' })).toHaveTextContent('Safari');
  });

  it.each([
    ['loading', '正在讀取瀏覽器…'],
    ['error', '無法讀取瀏覽器'],
  ] as const)('disables the select in the %s state', (loadState, message) => {
    render(<GameLaunchSelect browsers={[]} loadState={loadState} />);

    const trigger = screen.getByRole('combobox', { name: '遊戲瀏覽器' });
    expect(trigger).toBeDisabled();
    expect(trigger).toHaveTextContent(message);
  });
});
