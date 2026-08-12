/**
 * Generate the add-in's icon PNGs.
 *
 * The manifest requires real raster icons at several fixed sizes and Outlook
 * will refuse a manifest whose icon URLs 404. Rather than commit binaries with
 * no provenance, this draws them: a rounded square in Outlook's blue with a pale
 * sorting glyph, written as raw PNG via zlib. No image dependencies.
 *
 *   node scripts/make-icons.mjs
 */

import { deflateSync } from 'node:zlib';
import { writeFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const OUT = join(dirname(fileURLToPath(import.meta.url)), '..', 'assets');
const SIZES = [16, 32, 64, 80, 128];

const BLUE = [15, 108, 189];
const PALE = [235, 243, 251];

function crc32(buf) {
  let table = crc32.table;
  if (!table) {
    table = crc32.table = new Int32Array(256);
    for (let i = 0; i < 256; i++) {
      let c = i;
      for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
      table[i] = c;
    }
  }
  let crc = -1;
  for (const byte of buf) crc = (crc >>> 8) ^ table[(crc ^ byte) & 0xff];
  return (crc ^ -1) >>> 0;
}

function chunk(type, data) {
  const length = Buffer.alloc(4);
  length.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, 'ascii'), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body));
  return Buffer.concat([length, body, crc]);
}

/** Three horizontal bars of decreasing width - a "sorted" glyph. */
function glyph(size, x, y) {
  const unit = size / 16;
  const bars = [
    { top: 4.5, left: 3.5, width: 9 },
    { top: 7.5, left: 3.5, width: 6.5 },
    { top: 10.5, left: 3.5, width: 4 },
  ];
  const thickness = Math.max(1, Math.round(1.4 * unit));

  for (const bar of bars) {
    const top = bar.top * unit;
    const left = bar.left * unit;
    const right = (bar.left + bar.width) * unit;
    if (y >= top && y < top + thickness && x >= left && x < right) return true;
  }
  return false;
}

function render(size) {
  const radius = size * 0.18;
  // 4 bytes/pixel RGBA plus a filter byte per scanline.
  const raw = Buffer.alloc(size * (size * 4 + 1));
  let offset = 0;

  for (let y = 0; y < size; y++) {
    raw[offset++] = 0; // filter: none
    for (let x = 0; x < size; x++) {
      // Rounded-corner mask.
      const cx = Math.min(x, size - 1 - x);
      const cy = Math.min(y, size - 1 - y);
      let inside = true;
      if (cx < radius && cy < radius) {
        const dx = radius - cx;
        const dy = radius - cy;
        inside = dx * dx + dy * dy <= radius * radius;
      }

      const [r, g, b] = glyph(size, x, y) ? PALE : BLUE;
      raw[offset++] = r;
      raw[offset++] = g;
      raw[offset++] = b;
      raw[offset++] = inside ? 255 : 0;
    }
  }

  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // colour type: RGBA
  ihdr[10] = 0;
  ihdr[11] = 0;
  ihdr[12] = 0;

  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

mkdirSync(OUT, { recursive: true });
for (const size of SIZES) {
  const file = join(OUT, `icon-${size}.png`);
  writeFileSync(file, render(size));
  console.log(`wrote ${file}`);
}
