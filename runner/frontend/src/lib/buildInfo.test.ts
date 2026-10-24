import { describe, it, expect } from 'vitest'
import { formatBuild } from './buildInfo'

describe('formatBuild', () => {
  it('shows the release with the commit as a suffix', () => {
    expect(formatBuild({ version: '510d0c0', release: '0.2.0', commit: '510d0c0' })).toMatchObject({ main: 'v0.2.0', commit: '510d0c0' })
  })
  it('drops a commit that is the release', () => {
    expect(formatBuild({ release: '0.2.0', commit: '0.2.0' }).commit).toBe('')
  })
  it('falls back to the single version string when no release is reported', () => {
    expect(formatBuild({ version: 'dev' })).toMatchObject({ main: 'dev', commit: '' })
  })
})

describe('formatBuild update notice', () => {
  const url = 'https://github.com/blerglab/blerg-ai/releases/tag/v0.4.1';
  it('offers a link to a newer release', () => {
    expect(formatBuild({ release: '0.4.0', latestVersion: '0.4.1', latestUrl: url }).update).toEqual({
      label: 'v0.4.1 available',
      href: url,
      title: 'A newer Blerg release is available',
    });
  });
  it('offers nothing when absent, malformed or not a github.com link', () => {
    expect(formatBuild({ release: '0.4.0' }).update).toBeNull();
    expect(formatBuild({ release: '0.4.0', latestVersion: '0.4.1' }).update).toBeNull();
    expect(formatBuild({ release: '0.4.0', latestVersion: 'soon', latestUrl: url }).update).toBeNull();
    expect(formatBuild({ release: '0.4.0', latestVersion: '0.4.1', latestUrl: 'https://evil.example/x' }).update).toBeNull();
    expect(formatBuild({ release: '0.4.0', latestVersion: '0.4.1', latestUrl: 'javascript:alert(1)' }).update).toBeNull();
    expect(formatBuild(null).update).toBeNull();
  });
});

it("rejects equal or older mixed-component releases", () => {
 const good = "https://github.com/blerglab/blerg-ai/releases/tag/v0.5.1";
 for (const release of ["0.5.1", "0.6.0", "dev", "0.05.0", "9999999999.0.0"]) {
   expect(formatBuild({release,latestVersion:"0.5.1",latestUrl:good}).update).toBeNull();
 }
});
it("rejects unrelated release paths", () => {
 const good = "https://github.com/blerglab/blerg-ai/releases/tag/v0.5.1";
 for (const latestUrl of [good+"?x",good+"#x",good.replace("v0.5.1","v0.1.0"),good.replace("blerglab/blerg-ai","attacker/fake"),good.replace("v0.5.1","%760.5.1")]) {
   expect(formatBuild({release:"0.5.0",latestVersion:"0.5.1",latestUrl}).update).toBeNull();
 }
 expect(formatBuild({release:"0.5.0",latestVersion:"00.6.0",latestUrl:good}).update).toBeNull();
});
