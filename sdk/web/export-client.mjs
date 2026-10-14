// Export the integration SDK into a host's bundler without a runtime dependency.
import { createHash } from 'node:crypto';
import { mkdir, readFile, realpath, writeFile } from 'node:fs/promises';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

export async function exportClient(destination) {
  const source = dirname(fileURLToPath(import.meta.url));
  const files = {};
  await mkdir(destination, { recursive: true });
  for (const name of ['LICENSE', 'agenstra-client.js', 'agenstra-client.d.ts', 'agenstra-chat.js', 'agenstra-chat.d.ts', 'agenstra-session.js', 'agenstra-session.d.ts']) {
    const bytes = await readFile(resolve(source, name));
    files[name] = createHash('sha256').update(bytes).digest('hex');
    await writeFile(resolve(destination, name), bytes);
  }
  await writeFile(resolve(destination, 'agenstra-sdk.json'), JSON.stringify({
    schema: 'agenstra.vendored-sdk.v1',
    source: 'https://github.com/KHG420/agenstra/tree/main/sdk/web',
    files,
  }, null, 2) + '\n');
  return files;
}

// An imported tool may receive a non-file argv[1], such as node --eval arguments.
const invokedFile = process.argv[1] ? await realpath(resolve(process.argv[1])).catch(() => null) : null;
if (invokedFile === fileURLToPath(import.meta.url)) {
  if (process.argv.length !== 3) throw new Error('Usage: node sdk/web/export-client.mjs <host-vendor-directory>');
  await exportClient(resolve(process.argv[2]));
  console.log('Exported Agenstra integration SDK, license and SHA-256 manifest.');
}
