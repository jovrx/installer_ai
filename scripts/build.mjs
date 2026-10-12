import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const args = process.argv.slice(2);
if (args.some(arg => arg !== '--worker-only')) throw new Error('Usage: node scripts/build.mjs [--worker-only]');
const workerOnly = args.includes('--worker-only');
function run(command, args, cwd = root, env = {}) {
  const result = spawnSync(command, args, { cwd, stdio: 'inherit', env: { ...process.env, ...env } });
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status ?? 1);
}
function npm(args) {
  if (process.platform === 'win32') run(process.env.ComSpec ?? 'cmd.exe', ['/d', '/s', '/c', `npm ${args.join(' ')}`], join(root, 'web'));
  else run('npm', args, join(root, 'web'));
}
const output = join(root, 'dist');
mkdirSync(output, { recursive: true });
if (!workerOnly) {
  npm(['ci']);
  npm(['run', 'typecheck']);
  npm(['run', 'build']);
  const assets = join(root, 'internal', 'webui', 'assets');
  const within = relative(root, assets);
  if (within.startsWith('..') || resolve(assets) === root) throw new Error('Invalid embedded assets target');
  if (existsSync(assets)) rmSync(assets, { recursive: true });
  mkdirSync(assets, { recursive: true });
  cpSync(join(root, 'web', 'dist'), assets, { recursive: true });
  for (const [os, arch] of [['windows','amd64'],['linux','amd64'],['darwin','arm64'],['darwin','amd64']]) {
    for (const program of ['controller', 'remoteops-mcp']) {
      const name = `remoteops-${program === 'remoteops-mcp' ? 'mcp' : program}-${os}-${arch}${os === 'windows' ? '.exe' : ''}`;
      run('go', ['build', '-trimpath', '-ldflags=-s -w', '-o', join(output, name), `./cmd/${program}`], root, { GOOS: os, GOARCH: arch, CGO_ENABLED: '0' });
    }
  }
}
for (const [name, flags] of [
  ['remoteops-worker-windows-amd64.exe', '-s -w -X main.defaultUI=tui'],
  ['remoteops-worker-gui-windows-amd64.exe', '-s -w -H=windowsgui -X main.defaultUI=gui'],
]) {
  run('go', ['build', '-trimpath', `-ldflags=${flags}`, '-o', join(output, name), './cmd/worker'], root, { GOOS: 'windows', GOARCH: 'amd64', CGO_ENABLED: '0' });
}
const lines = readdirSync(output).filter(name => name.startsWith('remoteops-')).sort().map(name => `${createHash('sha256').update(readFileSync(join(output,name))).digest('hex')}  ${name}`);
writeFileSync(join(output,'SHA256SUMS'), lines.join('\n') + '\n');
console.log(`Build artifacts: ${output}`);
