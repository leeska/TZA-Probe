import { esc } from "./api.js";

export function locationPicker(position) {
  return `<section class="region-location" aria-label="目标定位"><div class="region-location-heading"><strong>目标定位</strong><button type="button" class="quiet" data-ip-location>使用出口 IP 定位</button></div><p class="hint" data-location-status>${position ? `${esc(position.city || position.country)} · ${position.latitude.toFixed(5)}, ${position.longitude.toFixed(5)}` : "尚未设置精确定位，可按出口 IP 定位或在地图选点。"}</p><details data-location-picker><summary>地图选点 / 输入经纬度</summary><div class="region-map" data-map tabindex="0" role="application" aria-label="定位地图，方向键移动，回车选中中心位置"><div data-map-tiles></div><span class="region-map-pin" data-map-pin aria-hidden="true"></span><div class="region-map-zoom"><button type="button" data-map-zoom="1" aria-label="放大地图">+</button><button type="button" data-map-zoom="-1" aria-label="缩小地图">−</button></div><a class="region-map-credit" href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener noreferrer">© OpenStreetMap</a></div><div class="region-coordinates"><label>纬度<input name="latitude" type="number" min="-90" max="90" step="any" value="${position?.latitude ?? ""}" placeholder="例如 35.6895"></label><label>经度<input name="longitude" type="number" min="-180" max="180" step="any" value="${position?.longitude ?? ""}" placeholder="例如 139.6917"></label><button type="button" data-confirm-location>确认定位</button></div><p class="hint" data-map-status>拖动地图后点击选点，或输入经纬度并确认。查询成功后自动匹配地区、语言和当地时区。</p><p class="hint">地图来自 OpenStreetMap；确认选点时向 OpenStreetMap 和 TimeAPI 查询地区与时区。不读取您设备的实际位置。</p></details><p class="hint">定位仅用于地区校正尝试；不会更改出口 IP 或服务商数据库。独立检测与复测不注入定位。</p></section>`;
}

export function wireLocationPicker(root, initial, lookup, search) {
  const map = root.querySelector("[data-map]");
  const tiles = root.querySelector("[data-map-tiles]");
  const visibleTiles = new Map();
  const latitude = root.querySelector('[name="latitude"]');
  const longitude = root.querySelector('[name="longitude"]');
  let center = initial || { latitude: 20, longitude: 0 }, point = initial, zoom = initial ? 5 : 2, start, frame, disposed = false;
  const details = root.querySelector("[data-location-picker]");
  const searchBox = document.createElement("div");
  searchBox.className = "region-place-search";
  searchBox.innerHTML = `<label>地点搜索<input type="search" maxlength="200" placeholder="城市、地址或地标" aria-label="地点搜索"></label><button type="button">搜索</button><ul class="region-place-results" aria-label="地点搜索结果"></ul><p class="hint" role="status" data-place-status></p>`;
  details.querySelector("summary").after(searchBox);
  const searchInput = searchBox.querySelector("input"), searchButton = searchBox.querySelector("button"), results = searchBox.querySelector("ul"), status = searchBox.querySelector("[data-place-status]");
  const runSearch = async () => {
    if (searchButton.disabled) return;
    const query = searchInput.value.trim();
    if (query.length < 2) { status.textContent = "请输入至少两个字符的地点名称。"; return; }
    searchButton.disabled = true; status.textContent = "正在搜索…"; results.replaceChildren();
    try {
      const places = await search(query);
      if (!root.isConnected || searchInput.value.trim() !== query) return;
      status.textContent = places.length ? "请选择地点，自动查询地区与时区。" : "没有找到地点，请换个名称或直接在地图选点。";
      for (const place of places) {
        const item = document.createElement("li"), button = document.createElement("button");
        button.type = "button"; button.className = "quiet"; button.textContent = place.name;
        button.onclick = () => { center = place; zoom = 11; choose(place); lookup({ latitude: place.latitude, longitude: place.longitude }); };
        item.append(button); results.append(item);
      }
    } catch (error) { status.textContent = `搜索失败：${error.message}`; }
    finally { searchButton.disabled = false; }
  };
  searchButton.onclick = runSearch;
  searchInput.onkeydown = event => { if (event.key === "Enter") { event.preventDefault(); event.stopPropagation(); runSearch(); } };
  const project = position => {
    const size = 256 * 2 ** zoom;
    const sine = Math.sin(Math.max(-85.0511, Math.min(85.0511, position.latitude)) * Math.PI / 180);
    return { x: (position.longitude + 180) / 360 * size, y: (0.5 - Math.log((1 + sine) / (1 - sine)) / (4 * Math.PI)) * size };
  };
  const unproject = (horizontal, vertical) => {
    const size = 256 * 2 ** zoom;
    return { longitude: ((horizontal / size * 360) % 360 + 360) % 360 - 180, latitude: Math.atan(Math.sinh(Math.PI * (1 - 2 * Math.max(0, Math.min(size, vertical)) / size))) * 180 / Math.PI };
  };
  const draw = () => {
    if (disposed || !details.open || !map.clientWidth) return;
    const origin = project(center), width = map.clientWidth, height = map.clientHeight, count = 2 ** zoom;
    const left = origin.x - width / 2, top = origin.y - height / 2;
    const visible = new Set();
    for (let column = Math.floor(left / 256); column <= Math.floor((left + width) / 256); column++) {
      for (let row = Math.max(0, Math.floor(top / 256)); row <= Math.min(count - 1, Math.floor((top + height) / 256)); row++) {
        const key = `${zoom}/${column}/${row}`;
        visible.add(key);
        let image = visibleTiles.get(key);
        if (!image) {
          image = document.createElement("img");
          image.alt = ""; image.draggable = false;
          image.referrerPolicy = "origin";
          image.onerror = () => { if (!disposed && image.isConnected) root.querySelector("[data-map-status]").textContent = "地图瓦片暂未加载，可继续搜索地点或输入经纬度。"; };
          image.src = `https://tile.openstreetmap.org/${zoom}/${(column % count + count) % count}/${row}.png`;
          visibleTiles.set(key, image);
          tiles.append(image);
        }
        image.style.left = `${column * 256 - left}px`; image.style.top = `${row * 256 - top}px`;
      }
    }
    for (const [key, image] of visibleTiles) {
      if (!visible.has(key)) { image.remove(); visibleTiles.delete(key); }
    }
    const marker = root.querySelector("[data-map-pin]");
    marker.hidden = !point;
    if (point) { const pixel = project(point); marker.style.left = `${pixel.x - left}px`; marker.style.top = `${pixel.y - top}px`; }
  };
  const scheduleDraw = () => {
    if (disposed || frame !== undefined) return;
    frame = requestAnimationFrame(() => { frame = undefined; draw(); });
  };
  const choose = position => {
    point = position;
    latitude.value = position.latitude.toFixed(5); longitude.value = position.longitude.toFixed(5);
    latitude.dispatchEvent(new Event("input", { bubbles: true }));
    root.querySelector("[data-map-status]").textContent = "已选点，点击“确认定位”查询地区与时区。";
    draw();
  };
  root.querySelector("[data-location-picker]").addEventListener("toggle", draw);
  root.querySelectorAll("[data-map-zoom]").forEach(button => { button.onclick = () => { zoom = Math.max(1, Math.min(16, zoom + Number(button.dataset.mapZoom))); draw(); }; });
  map.onpointerdown = event => {
    if (event.target.closest("button, a")) return;
    start = { x: event.clientX, y: event.clientY, center: project(center), moved: false };
    map.setPointerCapture(event.pointerId);
  };
  map.onpointermove = event => {
    if (!start) return;
    const horizontal = event.clientX - start.x, vertical = event.clientY - start.y;
    if (Math.abs(horizontal) + Math.abs(vertical) > 5) start.moved = true;
    if (start.moved) { center = unproject(start.center.x - horizontal, start.center.y - vertical); scheduleDraw(); }
  };
  map.onpointerup = event => {
    if (!start) return;
    if (!start.moved) {
      const box = map.getBoundingClientRect(), pixel = project(center);
      choose(unproject(pixel.x + event.clientX - box.left - box.width / 2, pixel.y + event.clientY - box.top - box.height / 2));
    }
    start = null;
  };
  map.onpointercancel = () => { start = null; };
  map.onkeydown = event => {
    if (event.target !== map) return;
    const offsets = { ArrowLeft: [-64, 0], ArrowRight: [64, 0], ArrowUp: [0, -64], ArrowDown: [0, 64] };
    if (event.key === "Enter") { event.preventDefault(); choose(center); }
    if (offsets[event.key]) { event.preventDefault(); const pixel = project(center), offset = offsets[event.key]; center = unproject(pixel.x + offset[0], pixel.y + offset[1]); draw(); }
  };
  root.querySelector("[data-confirm-location]").onclick = () => {
    if (!latitude.value || !longitude.value || !latitude.checkValidity() || !longitude.checkValidity()) { root.querySelector("[data-map-status]").textContent = "请填写有效经纬度：纬度 −90～90，经度 −180～180。"; return; }
    lookup({ latitude: Number(latitude.value), longitude: Number(longitude.value) });
  };
  const resize = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(scheduleDraw);
  resize?.observe(map);
  const update = position => { point = position; if (position) { center = position; latitude.value = position.latitude; longitude.value = position.longitude; } else { latitude.value = ""; longitude.value = ""; } draw(); };
  update.dispose = () => { disposed = true; resize?.disconnect(); if (frame !== undefined) cancelAnimationFrame(frame); visibleTiles.clear(); };
  return update;
}
