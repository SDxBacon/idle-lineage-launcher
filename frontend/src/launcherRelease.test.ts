import { afterEach, describe, expect, it, vi } from 'vitest';
import { fetchNewerLauncherVersion, newerLauncherVersion } from './launcherRelease';

function responseWith(body: unknown, ok = true) {
  return {
    ok,
    json: vi.fn().mockResolvedValue(body),
  } as unknown as Response;
}

describe('newerLauncherVersion', () => {
  it.each([
    ['1.2.3', 'v2.0.0', '2.0.0'],
    ['1.2.3', '1.3.0', '1.3.0'],
    ['v1.2.3', '1.2.4', '1.2.4'],
    ['1.2.3', '1.2.3', null],
    ['1.2.3', '1.2.2', null],
    ['1.2.3', '1.1.99', null],
    ['1.2.3', '0.99.99', null],
  ])('compares %s with %s', (current, candidate, expected) => {
    expect(newerLauncherVersion(current, candidate)).toBe(expected);
  });

  it.each([
    ['', '1.2.3'],
    ['1.2', '1.2.3'],
    ['1.2.3', 'v1.2'],
    ['1.2.3-dev', '1.2.4'],
    ['1.2.3', '1.2.4-beta.1'],
    ['01.2.3', '1.2.4'],
    ['1.2.3', '1.02.4'],
  ])('rejects invalid versions %s and %s', (current, candidate) => {
    expect(newerLauncherVersion(current, candidate)).toBeNull();
  });
});

describe('fetchNewerLauncherVersion', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('returns a normalized newer tag from the latest GitHub release', async () => {
    const fetchMock = vi.fn().mockResolvedValue(responseWith({ tag_name: 'v1.4.0' }));
    vi.stubGlobal('fetch', fetchMock);
    const controller = new AbortController();

    await expect(fetchNewerLauncherVersion('1.3.9', controller.signal)).resolves.toBe('1.4.0');
    expect(fetchMock).toHaveBeenCalledWith(
      'https://api.github.com/repos/SDxBacon/idle-lineage-launcher/releases/latest',
      {
        headers: { Accept: 'application/vnd.github+json' },
        signal: controller.signal,
      },
    );
  });

  it('does not fetch when the injected version is invalid', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    await expect(fetchNewerLauncherVersion('', new AbortController().signal)).resolves.toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it.each([
    ['an HTTP error', responseWith({}, false)],
    ['a missing tag', responseWith({ name: 'v1.4.0' })],
    ['a non-string tag', responseWith({ tag_name: 140 })],
  ])('silently ignores %s', async (_label, response) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response));

    await expect(fetchNewerLauncherVersion('1.3.9', new AbortController().signal))
      .resolves.toBeNull();
  });

  it('silently ignores invalid JSON and network failures', async () => {
    const invalidJSON = responseWith({ tag_name: 'v1.4.0' });
    vi.mocked(invalidJSON.json).mockRejectedValueOnce(new SyntaxError('invalid JSON'));
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(invalidJSON)
      .mockRejectedValueOnce(new TypeError('network failure'));
    vi.stubGlobal('fetch', fetchMock);

    await expect(fetchNewerLauncherVersion('1.3.9', new AbortController().signal))
      .resolves.toBeNull();
    await expect(fetchNewerLauncherVersion('1.3.9', new AbortController().signal))
      .resolves.toBeNull();
  });
});
