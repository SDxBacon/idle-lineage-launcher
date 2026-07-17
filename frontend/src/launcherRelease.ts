const latestReleaseURL = 'https://api.github.com/repos/SDxBacon/idle-lineage-launcher/releases/latest';
const launcherVersionPattern = /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;

type ParsedLauncherVersion = {
  normalized: string;
  parts: [number, number, number];
};

function parseLauncherVersion(version: string): ParsedLauncherVersion | null {
  const match = launcherVersionPattern.exec(version.trim());
  if (!match) {
    return null;
  }

  const parts: [number, number, number] = [
    Number(match[1]),
    Number(match[2]),
    Number(match[3]),
  ];
  if (!parts.every(Number.isSafeInteger)) {
    return null;
  }

  return {
    normalized: parts.join('.'),
    parts,
  };
}

export function newerLauncherVersion(currentVersion: string, candidateVersion: string) {
  const current = parseLauncherVersion(currentVersion);
  const candidate = parseLauncherVersion(candidateVersion);
  if (!current || !candidate) {
    return null;
  }

  for (let index = 0; index < current.parts.length; index += 1) {
    if (candidate.parts[index] > current.parts[index]) {
      return candidate.normalized;
    }
    if (candidate.parts[index] < current.parts[index]) {
      return null;
    }
  }

  return null;
}

export async function fetchNewerLauncherVersion(
  currentVersion: string,
  signal: AbortSignal,
): Promise<string | null> {
  if (!parseLauncherVersion(currentVersion)) {
    return null;
  }

  try {
    const response = await fetch(latestReleaseURL, {
      headers: {
        Accept: 'application/vnd.github+json',
      },
      signal,
    });
    if (!response.ok) {
      return null;
    }

    const release: unknown = await response.json();
    if (!release || typeof release !== 'object' || !('tag_name' in release)) {
      return null;
    }
    const tagName = (release as { tag_name: unknown }).tag_name;
    if (typeof tagName !== 'string') {
      return null;
    }

    return newerLauncherVersion(currentVersion, tagName);
  } catch {
    return null;
  }
}
