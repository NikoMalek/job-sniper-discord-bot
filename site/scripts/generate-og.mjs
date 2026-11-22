import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import sharp from 'sharp';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const avatarSvg = await readFile(path.join(root, 'public', 'avatar.svg'));
const output = path.join(root, 'public', 'og.png');

const avatar = await sharp(avatarSvg).resize(190, 190).png().toBuffer();

const text = Buffer.from(`<svg xmlns="http://www.w3.org/2000/svg" width="880" height="260" viewBox="0 0 880 260">
  <text x="0" y="92" fill="#f2f3f5" font-family="Arial, Helvetica, sans-serif" font-size="76" font-weight="700" letter-spacing="-2">Job Sniper</text>
  <text x="0" y="152" fill="#b5bac1" font-family="Arial, Helvetica, sans-serif" font-size="32" font-weight="400">Alertas de empleo multiportal en Discord</text>
</svg>`);
const textLayer = await sharp(text).png().toBuffer();

await sharp({
  create: { width: 1200, height: 630, channels: 4, background: '#1e1f22' }
})
  .composite([
    { input: avatar, left: 96, top: 220 },
    { input: textLayer, left: 330, top: 180 }
  ])
  .png()
  .toFile(output);

console.log(`OG generado: ${output}`);
