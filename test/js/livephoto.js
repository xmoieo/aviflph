/*
 * aviflph.js
 *
 * 从合并后的 AVIF(xomu_photo.avif)中解析 ISO-BMFF / HEIF 结构:
 *   - 读取 iloc + iinf,按 item_type 智能识别条目:
 *       av01 条目 -> 静态图;xomu 条目 -> 内嵌 MP4 视频
 *   - 把静态图条目重新封装为独立的 AVIF(Blob)显示在 <img>
 *   - 用第二次 fetch(带 Range)取下 xomu 条目的 MP4 字节,用 <video> 播放
 *
 * 注: AVIF 是 ISO-BMFF,盒子(box)结构为 [size:4][type:4][payload]。
 *     iloc 条目(本文件 version 0, offset/base 各 4 字节)给出每个 item
 *     在文件中的绝对偏移(= base_offset + extent_offset)。
 */

'use strict';

const AVIF_URL = new URLSearchParams(location.search).get('img') || './img/xomu_photo.avif';
let isLive = false;

/* ---------- 小工具 ---------- */

function readU32(view, off) { return view.getUint32(off, false); }   // 大端
function readU16(view, off) { return view.getUint16(off, false); }
function writeU32(arr, off, v) {
  arr[off] = (v >>> 24) & 0xff; arr[off + 1] = (v >>> 16) & 0xff;
  arr[off + 2] = (v >>> 8) & 0xff; arr[off + 3] = v & 0xff;
}
function writeU16(arr, off, v) { arr[off] = (v >>> 8) & 0xff; arr[off + 1] = v & 0xff; }
function fourcc(view, off) {
  return String.fromCharCode(view.getUint8(off), view.getUint8(off + 1),
                             view.getUint8(off + 2), view.getUint8(off + 3));
}

/* 在 [start, end) 范围内查找指定 type 的盒子,返回 {off, size}(相对文件) */
function findBox(view, start, end, type) {
  let off = start;
  while (off + 8 <= end) {
    let size = readU32(view, off);
    const t = fourcc(view, off + 4);
    if (size === 0) size = end - off;                 // 0 = 到文件末尾
    else if (size === 1) size = readU32(view, off + 8) + 8; // 64 位 largesize
    if (t === type) return { off, size };
    if (size < 8) break;
    off += size;
  }
  return null;
}

/* ---------- 解析 AVIF ---------- */

function parseAvif(buf) {
  const view = new DataView(buf);
  const len = buf.byteLength;

  const ftyp = findBox(view, 0, len, 'ftyp');
  const meta = findBox(view, 0, len, 'meta');
  if (!ftyp || !meta) throw new Error('不是有效的 AVIF(缺少 ftyp / meta)');

  // meta: [size][type][version+flags(4)][children...]
  const iloc = findBox(view, meta.off + 12, meta.off + meta.size, 'iloc');
  if (!iloc) throw new Error('未找到 iloc 盒子');

  const c = iloc.off + 8;                 // iloc 内容起点
  const version = view.getUint8(c);
  // 尺寸字段是打包的: 一个字节里高/低 4 位分别存两种 size
  const sizeByte1 = view.getUint8(c + 4);
  const sizeByte2 = view.getUint8(c + 5);
  const offsetSize     = (sizeByte1 >> 4) & 0xf;
  const lengthSize     = sizeByte1 & 0xf;
  const baseOffsetSize = (sizeByte2 >> 4) & 0xf;
  const indexSize      = sizeByte2 & 0xf;
  const itemCount = readU16(view, c + 6);

  let p = c + 8;                          // 条目起点
  const items = [];
  for (let i = 0; i < itemCount; i++) {
    const itemId = readU16(view, p);
    p += 2 + 2;                           // item_ID(2) + data_reference_index(2)

    let baseOffset = 0;
    if (baseOffsetSize === 4) { baseOffset = readU32(view, p); p += 4; }
    else if (baseOffsetSize === 8) {
      baseOffset = readU32(view, p) * 0x100000000 + readU32(view, p + 4); p += 8;
    }

    const extentCount = readU16(view, p); p += 2;
    let extentOffset = 0, extentLength = 0;
    for (let j = 0; j < extentCount; j++) {
      if (version === 1 || version === 2) p += indexSize;
      if (offsetSize === 4) { extentOffset = readU32(view, p); p += 4; }
      else if (offsetSize === 8) {
        extentOffset = readU32(view, p) * 0x100000000 + readU32(view, p + 4); p += 8;
      }
      if (lengthSize === 4) { extentLength = readU32(view, p); p += 4; }
      else if (lengthSize === 8) {
        extentLength = readU32(view, p) * 0x100000000 + readU32(view, p + 4); p += 8;
      }
    }
    items.push({ id: itemId, offset: baseOffset + extentOffset, length: extentLength });
  }

  // 解析 iinf:取得每个 item 的 type(av01 / xomu / Exif ...)
  const iinf = findBox(view, meta.off + 12, meta.off + meta.size, 'iinf');
  const types = {};   // id -> type
  if (iinf) {
    const ic = iinf.off + 8;
    const count = readU16(view, ic + 4);
    let q = ic + 6;
    for (let i = 0; i < count; i++) {
      const es = q;
      const esize = readU32(view, es);
      const infeVer = view.getUint8(es + 8);
      const idBytes = infeVer >= 3 ? 4 : 2;
      const id = idBytes === 2 ? readU16(view, es + 12) : readU32(view, es + 12);
      let type = null;
      if (infeVer >= 2) {
        const t = es + 14 + (idBytes === 2 ? 2 : 4);   // 跳过 protection_index
        type = fourcc(view, t);
      } else if (infeVer === 0) {
        const nameLen = view.getUint8(es + 15);
        type = nameLen ? fourcc(view, es + 16) : null;
      }
      if (type) types[id] = type.trim().toLowerCase();
      q += esize;
    }
  }

  return {
    view, buf, ftypOff: ftyp.off, ftypSize: ftyp.size,
    metaOff: meta.off, metaSize: meta.size, items, types,
  };
}

/* ---------- 按需重组:只保留静态图(item 1)的最小 AVIF ---------- */

function readU24(view, off) {
  return (view.getUint8(off) << 16) | (view.getUint8(off + 1) << 8) | view.getUint8(off + 2);
}

/* 遍历父盒子 [start, end) 内的直接子盒子 */
function* childBoxes(view, start, end) {
  let p = start;
  while (p + 8 <= end) {
    const size = view.getUint32(p);
    if (size < 8) break;
    const type = String.fromCharCode(
      view.getUint8(p + 4), view.getUint8(p + 5),
      view.getUint8(p + 6), view.getUint8(p + 7));
    yield { type, start: p, size, end: p + size };
    p += size;
  }
}

/* 解析 iloc 的全部条目 */
function parseIloc(view, box) {
  const p0 = box.start + 8;
  const version = view.getUint8(p0);
  const flags = readU24(view, p0 + 1);
  const sb1 = view.getUint8(p0 + 4);
  const sb2 = view.getUint8(p0 + 5);
  const offsetSize = (sb1 >> 4) & 0xf;
  const lengthSize = sb1 & 0xf;
  const baseOffsetSize = (sb2 >> 4) & 0xf;
  const indexSize = sb2 & 0xf;
  let q = p0 + 6;
  const itemCount = view.getUint16(q); q += 2;
  const entries = [];
  for (let i = 0; i < itemCount; i++) {
    const id = view.getUint16(q); q += 2;
    const dataRef = view.getUint16(q); q += 2;
    let baseOffset = 0;
    if (baseOffsetSize === 4) { baseOffset = view.getUint32(q); q += 4; }
    else if (baseOffsetSize === 8) { baseOffset = Number(view.getBigUint64(q)); q += 8; }
    const extentCount = view.getUint16(q); q += 2;
    const extents = [];
    for (let k = 0; k < extentCount; k++) {
      let off = 0, len = 0;
      if (offsetSize === 4) off = view.getUint32(q);
      else if (offsetSize === 8) off = Number(view.getBigUint64(q));
      q += offsetSize;
      if (lengthSize === 4) len = view.getUint32(q);
      else if (lengthSize === 8) len = Number(view.getBigUint64(q));
      q += lengthSize;
      if (indexSize) q += indexSize;
      extents.push({ offset: off, length: len });
    }
    entries.push({ id, dataRef, baseOffset, extents });
  }
  return { version, flags, offsetSize, lengthSize, baseOffsetSize, indexSize, entries };
}

/* 重建 iloc:仅 item 1,base_offset 指向新 mdat,extent 从 0 开始 */
function buildIlocChild(info, dataRef, baseOffset, imgLen) {
  const entryLen = 2 + 2 + info.baseOffsetSize + 2 + info.offsetSize + info.lengthSize + info.indexSize;
  const size = 8 + 6 + 2 + entryLen;
  const buf = new Uint8Array(size);
  const v = new DataView(buf.buffer);
  v.setUint32(0, size);
  buf.set([0x69, 0x6c, 0x6f, 0x63], 4);          // 'iloc'
  v.setUint8(8, info.version);
  v.setUint8(9, (info.flags >> 16) & 0xff);
  v.setUint8(10, (info.flags >> 8) & 0xff);
  v.setUint8(11, info.flags & 0xff);
  v.setUint8(12, (info.offsetSize << 4) | info.lengthSize);
  v.setUint8(13, (info.baseOffsetSize << 4) | info.indexSize);
  v.setUint16(14, 1);                            // item count = 1
  let q = 16;
  v.setUint16(q, 1); q += 2;                     // item id = 1
  v.setUint16(q, dataRef); q += 2;
  if (info.baseOffsetSize === 4) v.setUint32(q, baseOffset);
  else if (info.baseOffsetSize === 8) v.setBigUint64(q, BigInt(baseOffset));
  q += info.baseOffsetSize;
  v.setUint16(q, 1); q += 2;                     // extent count = 1
  if (info.offsetSize === 4) v.setUint32(q, 0);
  else if (info.offsetSize === 8) v.setBigUint64(q, 0n);
  q += info.offsetSize;
  if (info.lengthSize === 4) v.setUint32(q, imgLen);
  else if (info.lengthSize === 8) v.setBigUint64(q, BigInt(imgLen));
  q += info.lengthSize;
  return buf;
}

/* 重建 iinf:仅保留 item 1 的 infe 条目 */
function trimIinf(view, box, keepId) {
  const cs = box.start + 8;
  const version = view.getUint8(cs);
  const flags = readU24(view, cs + 1);
  const entryCount = view.getUint16(cs + 4);
  let q = cs + 6;
  let keep = null;
  for (let i = 0; i < entryCount; i++) {
    const es = q;
    const esize = view.getUint32(es);
    const infeVer = view.getUint8(es + 8);   // infe 是 FULL box: version 为 1 字节
    const idBytes = infeVer >= 3 ? 4 : 2;
    const id = idBytes === 2 ? view.getUint16(es + 12) : view.getUint32(es + 12);
    if (id === keepId) keep = new Uint8Array(view.buffer, view.byteOffset + es, esize);
    q += esize;
  }
  if (!keep) throw new Error('iinf 缺少条目 ' + keepId);
  const total = 8 + 4 + 2 + keep.length;
  const out = new Uint8Array(total);
  const v = new DataView(out.buffer);
  v.setUint32(0, total);
  out.set([0x69, 0x69, 0x6e, 0x66], 4);          // 'iinf'
  v.setUint8(8, version);
  v.setUint8(9, (flags >> 16) & 0xff);
  v.setUint8(10, (flags >> 8) & 0xff);
  v.setUint8(11, flags & 0xff);
  v.setUint16(12, 1);
  out.set(keep, 14);
  return out;
}

/* 用头部 + 静态图字节,组装只含静态图的最小 AVIF(Blob 用) */
function buildImageAvif(headBuf, imgBytes, parsed, stillId) {
  const ftyp = new Uint8Array(headBuf, parsed.ftypOff, parsed.ftypSize);
  const meta = new Uint8Array(headBuf, parsed.metaOff, parsed.metaSize);
  const mv = new DataView(meta.buffer, meta.byteOffset, meta.byteLength);

  const parts = [];
  let ilocInfo = null;
  for (const b of childBoxes(mv, 12, meta.byteLength)) {
    if (b.type === 'iref') continue;            // 丢弃派生图引用
    if (b.type === 'iinf') {
      parts.push(trimIinf(mv, b, stillId));
    } else if (b.type === 'iloc') {
      ilocInfo = parseIloc(mv, b);
    } else {
      parts.push(new Uint8Array(meta.buffer, meta.byteOffset + b.start, b.size));
    }
  }
  if (!ilocInfo) throw new Error('未找到 iloc');
  const still = ilocInfo.entries.find(e => e.id === stillId);
  if (!still) throw new Error('iloc 缺少静态图条目 ' + stillId);

  const chunks = parts.slice();
  const ilocChild = buildIlocChild(ilocInfo, still.dataRef, 0, imgBytes.length);
  chunks.push(ilocChild);

  let contentLen = 0;
  for (const c of chunks) contentLen += c.length;
  const newMetaLen = 12 + contentLen;            // 12 = size+type+version/flags
  const baseOffset = ftyp.length + newMetaLen + 8; // +8 = 新 mdat 载荷起点(跳过 mdat 头)

  // 回填 base_offset
  const bv = new DataView(ilocChild.buffer);
  let q = 16 + 2 + 2;
  if (ilocInfo.baseOffsetSize === 4) bv.setUint32(q, baseOffset);
  else if (ilocInfo.baseOffsetSize === 8) bv.setBigUint64(q, BigInt(baseOffset));

  const newMeta = new Uint8Array(newMetaLen);
  const nv = new DataView(newMeta.buffer);
  nv.setUint32(0, newMetaLen);
  newMeta.set([0x6d, 0x65, 0x74, 0x61], 4);      // 'meta'
  newMeta.set(meta.slice(8, 12), 8);             // 复制 version+flags
  let wp = 12;
  for (const c of chunks) { newMeta.set(c, wp); wp += c.length; }

  const mdat = new Uint8Array(8 + imgBytes.length);
  const mds = new DataView(mdat.buffer);
  mds.setUint32(0, 8 + imgBytes.length);
  mdat.set([0x6d, 0x64, 0x61, 0x74], 4);         // 'mdat'
  mdat.set(imgBytes, 8);

  const out = new Uint8Array(ftyp.length + newMeta.length + mdat.length);
  out.set(ftyp, 0);
  out.set(newMeta, ftyp.length);
  out.set(mdat, ftyp.length + newMeta.length);
  return out;
}

/* ---------- UI 注入(单个 <img> 即可显示按钮) ---------- */

/* 在图片容器(相对定位)内注入播放按钮与叠加视频层 */
function injectUI() {
  const img = document.querySelector('.media img');
  const media = img.parentElement; // .media 需为 position:relative
  media.style.position = 'relative';

  const playBtn = document.createElement('button');
  playBtn.className = 'play-btn';
  playBtn.setAttribute('aria-label', '播放视频');
  playBtn.title = '播放视频';
  playBtn.innerHTML = '&#9654;';
  playBtn.style.display = 'none';

  const overlay = document.createElement('div');
  overlay.className = 'overlay';

  const closeBtn = document.createElement('button');
  closeBtn.className = 'close-btn';
  closeBtn.setAttribute('aria-label', '返回照片');
  closeBtn.title = '返回照片';
  closeBtn.innerHTML = '&#10005;';

  const overlayVideo = document.createElement('video');
  overlayVideo.className = 'overlay-video';
  overlayVideo.setAttribute('playsinline', '');

  overlay.appendChild(closeBtn);
  overlay.appendChild(overlayVideo);
  media.appendChild(playBtn);
  media.appendChild(overlay);

  const spinner = document.createElement('div');
  spinner.className = 'media-spinner';
  media.appendChild(spinner);

  const hideSpinner = () => { spinner.style.display = 'none'; };
  img.addEventListener('load', () => {
    img.classList.add('loaded');
    hideSpinner();
    if (isLive) playBtn.style.display = '';
  });
  img.addEventListener('error', hideSpinner);

  return { img, playBtn, closeBtn, overlay, overlayVideo };
}

const ui = injectUI();

function closeOverlay() {
  ui.overlay.classList.remove('show');
  ui.overlayVideo.pause();
}

ui.closeBtn.addEventListener('click', closeOverlay);
ui.overlayVideo.addEventListener('ended', closeOverlay);

/* ---------- 按需请求:只取需要的字节,避免下载整图(MP4 段) ---------- */
/* 静态图:只请求文件头部(ftyp + meta,很小)+ item 1 图像字节,重组成最小
 *         AVIF 显示;<img> 不再指向整文件,因此不会下载内嵌的 MP4。
 * 视频:点击播放后才请求头部(已缓存)+ item 3 的 MP4 字节。 */

const HEAD_END = 65535;          // 取前 64KB,足以覆盖 ftyp + meta(通常仅数百字节)
let metaCache = null;

async function getMeta() {
  if (metaCache) return metaCache;
  const head = await fetch(AVIF_URL, { headers: { Range: `bytes=0-${HEAD_END}` } })
    .then(r => {
      if (!r.ok && r.status !== 206) throw new Error('无法获取 ' + AVIF_URL);
      return r.arrayBuffer();
    });
  const parsed = parseAvif(head);
  metaCache = { head, parsed };
  return metaCache;
}

/* 取 [s, e] 区间字节;服务器不支持 Range 时回退为整文件再切片 */
async function rangeBytes(s, e) {
  const resp = await fetch(AVIF_URL, { headers: { Range: `bytes=${s}-${e}` } });
  let buf = await resp.arrayBuffer();
  if (buf.byteLength > (e - s + 1)) buf = buf.slice(s, e + 1);
  return new Uint8Array(buf);
}

async function loadStaticImage() {
  try {
    const { head, parsed } = await getMeta();
    // 智能识别:视频条目类型为 xomu;静态图为 av01 条目
    const videoItem = parsed.items.find(i => (parsed.types[i.id] || '') === 'xomu');
    isLive = !!videoItem;
    const stillItems = parsed.items.filter(i => (parsed.types[i.id] || '') === 'av01');
    const imgItem = stillItems[0] || parsed.items.find(i => i.id === 1);
    if (!imgItem) throw new Error('找不到静态图条目');
    const imgBytes = await rangeBytes(imgItem.offset, imgItem.offset + imgItem.length - 1);
    const avif = buildImageAvif(head, imgBytes, parsed, imgItem.id);
    ui.img.src = URL.createObjectURL(new Blob([avif], { type: 'image/avif' }));
  } catch (err) {
    isLive = false;
    ui.img.src = AVIF_URL;
    console.warn('静态图按需加载失败,退回整图加载:', err && err.message ? err.message : err);
  }
}

let videoReady = false;

async function loadVideo() {
  const { parsed } = await getMeta();
  // 智能识别:取 item_type 为 xomu 的视频条目
  const mp4Item = parsed.items.find(it => (parsed.types[it.id] || '') === 'xomu');
  if (!mp4Item) throw new Error('该图不是 Live 图(缺少 xomu 视频条目)');

  console.log('解析结果:', parsed.items);

  const mp4Buf = await rangeBytes(mp4Item.offset, mp4Item.offset + mp4Item.length - 1);
  ui.overlayVideo.src = URL.createObjectURL(new Blob([mp4Buf], { type: 'video/mp4' }));
}

ui.playBtn.addEventListener('click', async () => {
  if (!videoReady) {
    try {
      await loadVideo();
      videoReady = true;
    } catch (err) {
      // 取视频失败(非 Live 图 / 跨域被拦截)时,不破坏图片显示:
      // 隐藏播放按钮,图片仍正常渲染。
      ui.playBtn.style.display = 'none';
      console.warn('视频加载失败,图片仍正常显示:', err && err.message ? err.message : err);
      return;
    }
  }
  ui.overlay.classList.add('show');
  ui.overlayVideo.currentTime = 0;
  ui.overlayVideo.muted = false;
  ui.overlayVideo.play().catch(() => {});
});

/* 页面加载即按需显示静态图(只下载 item 1) */
loadStaticImage();
