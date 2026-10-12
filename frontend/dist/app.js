// Interface logic.
//
// The Go side owns the library, the artist reduction and the plan. This file
// holds only what the user has chosen — which names to merge, which tracks are
// selected, what was typed into the bulk editor — and asks Go to price those
// choices after every change, so the footer always shows what would really
// happen to the files.

const go = () => window.go.main.App;
const $ = (id) => document.getElementById(id);

const state = {
  root: "",
  onPhone: false,
  summary: null,
  tracks: [],

  // Names in the files that must NOT be folded into their artist.
  detached: new Set(),
  // Artists the user pointed at another name, keyed by the proposed name.
  renamed: new Map(),
  // Single names in the files aimed somewhere else than their own artist:
  // "Future, Juice Wrld" put under Juice WRLD while Future itself stays.
  moved: new Map(),

  // Album names in the files kept apart from the album they fold into, by
  // albumId; and albums pointed at another name, by the id of the album.
  albumDetached: new Set(),
  albumRenamed: new Map(),
  // Album names to remove from their tracks altogether.
  albumCleared: new Set(),
  // Albums, by id, whose tracks are made to agree on the album artist. Only
  // what the user ticks: nothing is suggested into a change on its own.
  albumArtistFixed: new Set(),

  albumArtistMode: "keep",
  removeCompilation: false,
  removeSort: false,
  removeLegacy: false,
  shrinkArtwork: false,
  backup: false,

  busy: false,
};

/* Helpers ------------------------------------------------------------------ */

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;
const files = (n) => plural(n, "file");
const tracks = (n) => plural(n, "track");

function duration(seconds) {
  if (!seconds) return "";
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

const baseName = (path) => String(path).split(/[\\/]/).pop();

let toastTimer = null;
function toast(message, isError) {
  const node = $("toast");
  node.textContent = message;
  node.classList.toggle("error", Boolean(isError));
  node.hidden = false;

  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { node.hidden = true; }, isError ? 7000 : 3000);
}

function debounce(fn, ms) {
  let timer = null;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), ms);
  };
}

/* Theme -------------------------------------------------------------------- */

// The chosen theme is remembered per machine; with nothing remembered the
// system preference decides. Storage can throw in a locked-down webview, so
// neither reading nor writing it is allowed to break the page.
const THEME_KEY = "mlo-theme";

function readTheme() {
  try {
    const saved = localStorage.getItem(THEME_KEY);
    if (saved === "light" || saved === "dark") return saved;
  } catch {}
  return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

function applyTheme(theme) {
  document.documentElement.dataset.theme = theme;
  try {
    localStorage.setItem(THEME_KEY, theme);
  } catch {}
}

applyTheme(readTheme());

// On a Mac the window buttons are drawn over the page, and the top bar makes
// room for them.
if (/Mac/.test(navigator.platform || navigator.userAgent)) {
  document.documentElement.classList.add("mac");
}

$("theme").addEventListener("click", () => {
  applyTheme(document.documentElement.dataset.theme === "light" ? "dark" : "light");
});

/* Navigation --------------------------------------------------------------- */

function showView(name) {
  for (const nav of document.querySelectorAll(".nav")) {
    nav.setAttribute("aria-current", String(nav.dataset.view === name));
  }
  for (const view of document.querySelectorAll(".view")) {
    view.hidden = view.dataset.view !== name;
  }
  if (name === "tracks") renderTracks();
  if (name === "playlists") openPlaylists();
}

for (const nav of document.querySelectorAll(".nav")) {
  nav.addEventListener("click", () => showView(nav.dataset.view));
}

/* Source ------------------------------------------------------------------- */

const phone = { serial: "", path: "/sdcard/Music" };

$("pick").addEventListener("click", showSource);

function showSource() {
  // The source screen is the way a library is opened, so the topbar button
  // that leads here has nothing left to do while it is up — and a button that
  // reopens the screen you are already on reads as broken.
  $("pick").hidden = true;

  $("source").hidden = false;
  $("scanning").hidden = true;
  $("workspace").hidden = true;
  $("footer").hidden = true;
}

$("src-folder").addEventListener("click", async () => {
  try {
    const chosen = await go().ChooseFolder();
    if (chosen) {
      phone.serial = "";
      startScan(chosen);
    }
  } catch (err) {
    toast(String(err), true);
  }
});

$("src-phone").addEventListener("click", () => {
  $("src-phone").setAttribute("aria-pressed", "true");
  $("phone-panel").hidden = false;
  loadPhones();
});

$("phone-refresh").addEventListener("click", () => { loadPhones(); loadFolders(); });
$("phone-scan").addEventListener("click", startPhoneScan);

$("phone-path").addEventListener("change", () => {
  phone.path = $("phone-path").value.trim() || "/sdcard/Music";
  loadFolders();
});

$("phone-up").addEventListener("click", () => {
  const parts = phone.path.replace(/\/+$/, "").split("/");
  if (parts.length > 2) {
    parts.pop();
    setPhonePath(parts.join("/") || "/");
  }
});

function setPhonePath(value) {
  phone.path = value;
  $("phone-path").value = value;
  loadFolders();
}

async function loadPhones() {
  const list = $("phone-devices");
  list.replaceChildren(el("div", "row-item muted", "Looking for a phone…"));

  let status;
  try {
    status = await go().Phones();
  } catch (err) {
    list.replaceChildren();
    showPhoneHint(String(err));
    return;
  }

  showPhoneHint(status.hint);
  list.replaceChildren();

  const devices = status.devices || [];
  // With one phone plugged in there is nothing to choose between, so it is
  // selected and its folders listed straight away.
  const ready = devices.filter((d) => d.ready);
  const autoSelect = !phone.serial && ready.length === 1;
  if (autoSelect) phone.serial = ready[0].serial;

  for (const found of devices) list.append(deviceRow(found));
  $("phone-scan").disabled = !phone.serial;
  if (autoSelect) loadFolders();
}

function deviceRow(found) {
  const row = el("button", found.ready ? "device ready" : "device");
  row.disabled = !found.ready;
  row.setAttribute("aria-pressed", String(found.serial === phone.serial));

  const main = el("div", "row-main");
  main.append(
    el("div", "row-name", found.model || found.serial),
    el("div", "row-sub", found.ready ? found.serial : found.hint || found.state),
  );

  row.append(el("span", "dot"), main);
  row.addEventListener("click", () => { phone.serial = found.serial; loadPhones(); loadFolders(); });
  return row;
}

function showPhoneHint(text) {
  const hint = $("phone-hint");
  hint.hidden = !text;
  hint.textContent = text || "";
}

async function loadFolders() {
  const list = $("phone-folders");
  if (!phone.serial) {
    list.replaceChildren();
    return;
  }

  list.replaceChildren(el("div", "row-item muted", "Reading the folder…"));
  try {
    const names = await go().PhoneFolders(phone.serial, phone.path);
    list.replaceChildren();
    for (const name of names || []) {
      const row = el("div", "row-item");
      row.append(el("div", "row-main", name));
      row.addEventListener("click", () => setPhonePath(`${phone.path.replace(/\/+$/, "")}/${name}`));
      list.append(row);
    }
  } catch (err) {
    list.replaceChildren();
    showPhoneHint(String(err));
  }
  $("phone-scan").disabled = !phone.serial;
}

/* Scanning ----------------------------------------------------------------- */

$("rescan").addEventListener("click", rescan);
$("scan-cancel").addEventListener("click", () => go().Cancel());

function rescan() {
  if (!state.root) return;
  if (phone.serial) startPhoneScan();
  else startScan(state.root);
}

function beginScanScreen(root, status) {
  state.root = root;
  $("root-path").textContent = root;
  $("root-path").classList.remove("muted");
  $("rescan").hidden = false;

  $("source").hidden = true;
  $("workspace").hidden = true;
  $("footer").hidden = true;
  $("scanning").hidden = false;
  $("scan-bar").style.width = "0";
  $("scan-status").textContent = status;
  $("scan-current").textContent = "";
}

async function startScan(root) {
  beginScanScreen(root, "Looking for files…");
  try {
    applyScan(await go().Scan(root));
  } catch (err) {
    $("scanning").hidden = true;
    showSource();
    toast(String(err), true);
  }
}

async function startPhoneScan() {
  beginScanScreen(phone.path, "Preparing the phone…");
  try {
    applyScan(await go().ScanPhone(phone.serial, phone.path));
  } catch (err) {
    $("scanning").hidden = true;
    showSource();
    showPhoneHint(String(err));
    toast(String(err), true);
  }
}

window.runtime.EventsOn("scan:progress", (p) => {
  $("scan-bar").style.width = p.total ? `${(p.done / p.total) * 100}%` : "0";
  $("scan-status").textContent = `Read ${p.done} of ${files(p.total)}`;
  $("scan-current").textContent = p.current || "";
});

async function applyScan(result) {
  const summary = result.summary;
  for (const field of ["targets", "albumArtists", "albums", "junkAlbums"]) summary[field] = summary[field] || [];

  state.summary = summary;
  state.onPhone = result.source === "phone";
  state.detached.clear();
  state.renamed.clear();
  state.moved.clear();
  state.albumDetached.clear();
  state.albumRenamed.clear();
  state.albumArtistFixed.clear();
  // Every merge, fix and removal starts unticked: the lists are suggestions,
  // and nothing in the files changes until the user ticks it.
  state.albumCleared = new Set();
  for (const target of summary.targets) {
    for (const source of target.sources) {
      if (source.value !== target.name) state.detached.add(source.value);
    }
  }
  for (const album of summary.albums) {
    for (const source of album.sources) {
      if (source.value !== album.name) state.albumDetached.add(albumId(album.owner, source.value));
    }
  }
  expanded.clear();
  albumExpanded.clear();
  selection.clear();

  try {
    state.tracks = (await go().Tracks()) || [];
  } catch {
    state.tracks = [];
  }

  $("scanning").hidden = true;
  $("pick").hidden = false;
  $("workspace").hidden = false;
  $("footer").hidden = false;
  $("revert").hidden = true;

  renderArtists();
  renderAlbums();
  renderCleanup(result);
  renderTracks();
  updateCounts();
  showView("artists");
  refreshPending();

  if (result.cancelled) toast("Scan stopped — showing part of the library");
}

function updateCounts() {
  $("count-tracks").textContent = state.tracks.length || "";
  const missing = state.summary.trackCount - state.summary.withLyrics;
  $("count-lyrics").textContent = missing > 0 ? missing : "";
}

/* Rules -------------------------------------------------------------------- */

// renames turns the artist list into the map the plan needs: every name in the
// files pointed at the artist it ends up under, minus the ones detached.
function renames() {
  const out = {};
  for (const group of groups) {
    for (const source of group.sources) {
      if (source.value !== group.name) out[source.value] = group.name;
    }
  }
  return out;
}

function rules() {
  return {
    rename: renames(),
    albumRename: albumRenames(),
    clearAlbum: clearedPaths(),
    ...albumArtistRules(),
    albumArtistMode: state.albumArtistMode,
    removeCompilation: state.removeCompilation,
    removeSort: state.removeSort,
    removeLegacy: state.removeLegacy,
    shrinkArtwork: state.shrinkArtwork,
    backup: state.backup,
  };
}

/* Artists ------------------------------------------------------------------ */

$("artist-search").addEventListener("input", renderArtists);


// targetName is what an artist ends up called, once the renames apply.
function targetName(target) {
  return state.renamed.get(target.name) || target.name;
}

// buildGroups works out the artist list as it will be. Two artists pointed at
// the same name become one row, which is how "put Future under Juice WRLD"
// shows its result straight away instead of waiting for the next scan.
function buildGroups() {
  const nameOf = new Map(); // name in the files -> artist it ends up under
  const found = new Map();

  const ensure = (name) => {
    let group = found.get(name);
    if (!group) {
      group = { name, sources: [], tracks: [], albums: new Set(), targets: [] };
      found.set(name, group);
    }
    return group;
  };

  for (const target of state.summary.targets) {
    const name = targetName(target);
    ensure(name).targets.push(target);

    for (const source of target.sources) {
      // Where this name would go if it is kept ticked, and where it goes now.
      const proposed = state.moved.get(source.value) || name;
      const under = state.detached.has(source.value) ? source.value : proposed;
      nameOf.set(source.value, under);
      ensure(under).sources.push({ ...source, proposed });
    }
  }

  for (const track of state.tracks) {
    const seen = new Set();
    for (const raw of [track.artist, track.albumArtist]) {
      if (!raw) continue;
      const name = nameOf.get(raw) || raw;
      if (seen.has(name)) continue;
      seen.add(name);

      const group = ensure(name);
      group.tracks.push(track);
      if (track.album) group.albums.add(track.album);
    }
  }

  return [...found.values()].sort((a, b) =>
    b.tracks.length - a.tracks.length || a.name.localeCompare(b.name));
}

let groups = [];
// Which artists are expanded, by name, so a redraw keeps them open.
const expanded = new Set();

// keepingScroll redraws without moving anything under the pointer. Every tick
// redraws the whole list, and the moment it is empty the scrolled panels fall
// back to the top — so unticking a name far down the Details panel jumped it
// to the first. Where each was scrolled to is put back afterwards.
function keepingScroll(draw) {
  const scrolled = [document.querySelector(".panel"), $("merge-summary"), $("album-merge-summary")];
  const tops = scrolled.map((node) => node?.scrollTop ?? 0);
  draw();
  scrolled.forEach((node, i) => { if (node) node.scrollTop = tops[i]; });
}

function renderArtists() {
  keepingScroll(drawArtists);
}

function drawArtists() {
  if (!state.summary) return;

  groups = buildGroups();
  $("artists-from").textContent = state.summary.rawArtists;
  $("artists-to").textContent = groups.length;
  $("count-artists").textContent = groups.length || "";
  renderArtistNames();
  renderMergeSummary();

  const needle = $("artist-search").value.trim().toLowerCase();
  const list = $("artist-list");
  list.replaceChildren();

  for (const group of groups) {
    if (needle) {
      const haystack = [group.name, ...group.sources.map((s) => s.value)].join(" ").toLowerCase();
      if (!haystack.includes(needle)) continue;
    }
    list.append(artistRow(group));
  }
}

/* Ticking many at once ----------------------------------------------------- */

// tickAll is a box standing for several: ticked when every one is, a dash
// when some are, and ticking it sets them all.
function tickAll(items, isTicked, setTicked, label, redraw) {
  const wrap = el("label", label ? "tick-all" : "tick-group");
  const box = el("input");
  box.type = "checkbox";
  const ticked = items.filter(isTicked).length;
  box.checked = items.length > 0 && ticked === items.length;
  box.indeterminate = ticked > 0 && ticked < items.length;
  box.title = "Tick or untick all of these";
  box.addEventListener("change", () => {
    for (const item of items) setTicked(item, box.checked);
    redraw();
    refreshPending();
  });
  wrap.append(box);
  if (label) wrap.append(el("span", null, label));
  return wrap;
}

const artistTicked = (source) => !state.detached.has(source.value);
function setArtistTicked(source, on) {
  if (on) state.detached.delete(source.value);
  else state.detached.add(source.value);
}

/* The whole reduction on one screen ---------------------------------------- */

$("summary-toggle").addEventListener("click", () => {
  const panel = $("merge-summary");
  panel.hidden = !panel.hidden;
  $("summary-toggle").textContent = panel.hidden ? "Details" : "Hide";
  if (!panel.hidden) renderMergeSummary();
});

// renderMergeSummary lists every name that changes and what it becomes, so the
// whole reduction can be checked and corrected in one place.
//
// The rows are grouped by the name they are headed for rather than by where
// they ended up: a row that has been unticked stays under the same heading,
// unticked, instead of vanishing the moment it is turned off.
function renderMergeSummary() {
  const panel = $("merge-summary");
  if (panel.hidden) return;

  panel.replaceChildren();

  const byTarget = new Map();
  for (const group of groups) {
    for (const source of group.sources) {
      if (source.value === source.proposed) continue;
      if (!byTarget.has(source.proposed)) byTarget.set(source.proposed, []);
      byTarget.get(source.proposed).push(source);
    }
  }

  if (!byTarget.size) {
    panel.append(el("div", "row-sub", "Nothing to merge."));
    return;
  }

  const every = [...byTarget.values()].flat();
  panel.append(tickAll(every, artistTicked, setArtistTicked,
    `Tick all — ${plural(every.length, "name")} under ${plural(byTarget.size, "artist")}`, renderArtists));

  const blocks = [...byTarget.entries()].sort((a, b) =>
    b[1].length - a[1].length || a[0].localeCompare(b[0]));

  for (const [name, sources] of blocks) panel.append(summaryGroup(name, sources));
}

// summaryGroup is one heading — the artist these names become — and the names
// themselves. The heading is a text field: typing another artist there sends
// the ticked names to that artist instead, which is how a handful of "Future,
// Juice Wrld" credits end up under Juice WRLD without moving Future itself.
function summaryGroup(name, sources) {
  const block = el("div", "summary-group");

  const head = el("div", "summary-head");
  const field = el("input", "summary-target");
  field.type = "text";
  field.value = name;
  field.spellcheck = false;
  field.setAttribute("list", "artist-names");
  field.title = "Type another artist to send the ticked names there";

  // Both change and blur fire when a name is typed and the field is left, and
  // the first of them redraws the panel, so the second is ignored.
  let committed = false;
  const commit = () => {
    const wanted = field.value.trim();
    if (committed || !wanted || wanted === name) {
      field.value = name;
      return;
    }
    committed = true;
    retarget(sources, wanted);
  };
  field.addEventListener("keydown", (event) => {
    if (event.key === "Enter") field.blur();
    if (event.key === "Escape") { field.value = name; field.blur(); }
  });
  field.addEventListener("change", commit);
  field.addEventListener("blur", commit);

  const ticked = sources.filter((source) => !state.detached.has(source.value)).length;
  head.append(tickAll(sources, artistTicked, setArtistTicked, "", renderArtists),
    field, el("span", "count", `${ticked} of ${plural(sources.length, "name")}`));
  block.append(head);

  for (const source of sources) block.append(summaryRow(source, name));
  return block;
}

function summaryRow(source, name) {
  const row = el("label", "summary-row");
  if (state.detached.has(source.value)) row.classList.add("off");

  const box = el("input");
  box.type = "checkbox";
  box.checked = !state.detached.has(source.value);
  box.addEventListener("change", () => {
    if (box.checked) state.detached.delete(source.value);
    else state.detached.add(source.value);
    renderArtists();
    refreshPending();
  });

  row.append(
    box,
    el("span", "from", source.value),
    el("span", "arrow", "→"),
    el("span", "to", name),
    el("span", "count", tracks(source.tracks)),
  );
  return row;
}

// retarget sends the ticked names of one block to another artist. Names that
// were turned off stay where they were, so unticking is not undone by it.
function retarget(sources, wanted) {
  for (const source of sources) {
    if (state.detached.has(source.value)) continue;
    if (wanted === source.value) state.moved.delete(source.value);
    else state.moved.set(source.value, wanted);
  }
  renderArtists();
  refreshPending();
}

// The list behind the heading field, so an artist can be picked rather than
// typed out.
function renderArtistNames() {
  const list = $("artist-names");
  list.replaceChildren();
  for (const group of groups) {
    const option = document.createElement("option");
    option.value = group.name;
    list.append(option);
  }
}

/* One artist --------------------------------------------------------------- */

function artistRow(group) {
  const folded = group.sources.filter((s) => s.value !== group.name);
  const block = el("div", "group");
  const head = el("div", "group-head");

  const main = el("div", "row-main");
  const name = el("div", "row-name");
  name.append(el("b", null, group.name));
  main.append(name);

  const parts = [tracks(group.tracks.length)];
  if (group.albums.size) parts.push(plural(group.albums.size, "album"));
  if (folded.length) parts.push(`${folded.length} folded in`);
  main.append(el("div", "row-sub", parts.join(" · ")));

  const details = el("button", "btn small", expanded.has(group.name) ? "Hide" : "Details");
  details.addEventListener("click", () => {
    if (expanded.has(group.name)) expanded.delete(group.name);
    else expanded.add(group.name);
    renderArtists();
  });

  const mergeInto = el("button", "btn small", "Merge into…");
  mergeInto.addEventListener("click", () => openMergeInto(group));

  const rename = el("button", "btn small ghost", "Rename");
  rename.addEventListener("click", () => startRename(block, group));

  head.append(main, details, mergeInto, rename);
  block.append(head);

  if (expanded.has(group.name)) block.append(detailsPanel(group));
  if (folded.length) block.classList.add("changed");
  return block;
}

// detailsPanel lists what folds into this artist, then every album under it
// with its tracks.
function detailsPanel(group) {
  const panel = el("div", "details");

  if (group.sources.length > 1) {
    panel.append(el("div", "details-head", "Names in the files"));
    const variants = el("div", "group-variants");
    for (const source of group.sources) variants.append(sourceRow(group, source));
    panel.append(variants);
  }

  panel.append(el("div", "details-head", plural(group.albums.size || 1, "album")));

  const byAlbum = new Map();
  for (const track of group.tracks) {
    const album = track.album || "No album";
    if (!byAlbum.has(album)) byAlbum.set(album, []);
    byAlbum.get(album).push(track);
  }

  for (const [album, list] of [...byAlbum.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
    const block = el("div", "album");
    const title = el("div", "album-head");
    title.append(el("b", null, album), el("span", "count", tracks(list.length)));
    block.append(title);

    list.sort((a, b) => (a.discNo - b.discNo) || (a.trackNo - b.trackNo)
      || String(a.title).localeCompare(String(b.title)));

    for (const track of list) {
      const row = el("div", "album-track");
      row.append(
        el("span", "num", track.trackNo ? String(track.trackNo) : ""),
        el("span", "name", track.title || baseName(track.path)),
        el("span", "count", duration(track.seconds)),
      );
      row.addEventListener("contextmenu", (event) => openTrackMenu(event, track));
      block.append(row);
    }
    panel.append(block);
  }

  return panel;
}

function sourceRow(group, source) {
  const row = el("label", "variant");
  const box = el("input");
  box.type = "checkbox";
  box.checked = !state.detached.has(source.value);
  box.disabled = source.value === group.name;

  box.addEventListener("change", () => {
    if (box.checked) state.detached.delete(source.value);
    else state.detached.add(source.value);
    renderArtists();
    refreshPending();
  });

  row.append(box, el("span", null, source.value));
  row.append(el("span", "count", tracks(source.tracks)));
  return row;
}

// renameGroup points every artist behind a row at a new name. Aiming two of
// them at the same name is what merges them.
function renameGroup(group, value) {
  for (const target of group.targets) {
    if (value && value !== target.name) state.renamed.set(target.name, value);
    else state.renamed.delete(target.name);
  }
  // A detached name keeps its own row, and renaming that row means the user
  // wants it under the new name after all.
  for (const source of group.sources) {
    if (source.value === group.name) state.detached.delete(source.value);
  }

  expanded.delete(group.name);
  renderArtists();
  refreshPending();
}

// startRename swaps the row for a text field, committed with Enter.
function startRename(block, group) {
  const input = el("input", "rename-input");
  input.type = "text";
  input.value = group.name;

  const commit = () => renameGroup(group, input.value.trim());
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter") commit();
    if (event.key === "Escape") renderArtists();
  });
  input.addEventListener("blur", commit);

  block.replaceChildren(input);
  input.focus();
  input.select();
}

/* Merging one artist into another ------------------------------------------ */

let mergeSource = null;

$("merge-close").addEventListener("click", () => { $("merge-modal").hidden = true; });
$("merge-search").addEventListener("input", renderMergeOptions);

$("merge-search").addEventListener("keydown", (event) => {
  if (event.key !== "Enter") return;
  const typed = $("merge-search").value.trim();
  if (typed) commitMerge(typed);
});

function openMergeInto(group) {
  albumMergeSource = null;
  mergeSource = group;
  $("merge-title").textContent = `Put ${group.name} under…`;
  $("merge-search").value = "";
  renderMergeOptions();
  $("merge-modal").hidden = false;
  $("merge-search").focus();
}

function renderMergeOptions() {
  const needle = $("merge-search").value.trim().toLowerCase();
  const list = $("merge-options");
  list.replaceChildren();
  if (albumMergeSource) {
    renderAlbumMergeOptions(needle, list);
    return;
  }

  for (const group of groups) {
    if (group === mergeSource) continue;
    if (needle && !group.name.toLowerCase().includes(needle)) continue;

    const row = el("div", "row-item");
    const main = el("div", "row-main");
    main.append(el("div", "row-name", group.name), el("div", "row-sub", tracks(group.tracks.length)));
    row.append(main);
    row.addEventListener("click", () => commitMerge(group.name));
    list.append(row);
  }
}

function commitMerge(name) {
  if (mergeSource) renameGroup(mergeSource, name);
  else if (albumMergeSource) renameAlbumGroup(albumMergeSource, name);
  mergeSource = null;
  albumMergeSource = null;
  $("merge-modal").hidden = true;
}

/* Albums ------------------------------------------------------------------- */

// An album name means nothing without the artist it is by — two artists can
// both have a "Greatest Hits" — so every album and every name in the files is
// identified by the pair. The owner is worked out on the Go side.
const albumId = (owner, name) => `${owner}\u0000${name}`;

let albumGroups = [];
const albumExpanded = new Set();
let albumMergeSource = null;

$("album-search").addEventListener("input", renderAlbums);

$("albums-changed").addEventListener("click", () => {
  const button = $("albums-changed");
  button.setAttribute("aria-pressed", String(button.getAttribute("aria-pressed") !== "true"));
  renderAlbums();
});

// buildAlbumGroups works out the album list as it will be: every name in the
// files under the album it folds into, unless it was kept apart, and two
// albums of one artist renamed alike shown as one.
function buildAlbumGroups() {
  const byPath = new Map(state.tracks.map((t) => [t.path, t]));
  const cleared = new Set(clearedPaths());
  const found = new Map();

  const ensure = (owner, name, artist) => {
    const id = albumId(owner, name);
    let group = found.get(id);
    if (!group) {
      group = { id, owner, name, artist, sources: [], tracks: [], albums: [] };
      found.set(id, group);
    }
    return group;
  };

  for (const album of state.summary.albums) {
    const name = state.albumRenamed.get(albumId(album.owner, album.name)) || album.name;
    ensure(album.owner, name, album.artist).albums.push(album);

    for (const source of album.sources) {
      const kept = state.albumDetached.has(albumId(album.owner, source.value));
      const group = kept ? ensure(album.owner, source.value, album.artist) : ensure(album.owner, name, album.artist);
      group.sources.push({ ...source, owner: album.owner, proposed: name });
      for (const path of source.paths || []) {
        const track = byPath.get(path);
        if (track && !cleared.has(path)) group.tracks.push(track);
      }
    }
  }

  // An album whose name is being removed from every track is gone.
  for (const [id, group] of found) {
    if (!group.tracks.length) found.delete(id);
    else group.fix = albumArtistFix(group);
  }

  return [...found.values()].sort((a, b) =>
    b.tracks.length - a.tracks.length || a.name.localeCompare(b.name) || a.artist.localeCompare(b.artist));
}

const albumTicked = (source) => !state.albumDetached.has(albumId(source.owner, source.value));
function setAlbumTicked(source, on) {
  const id = albumId(source.owner, source.value);
  if (on) state.albumDetached.delete(id);
  else state.albumDetached.add(id);
}

/* The whole album reduction on one screen --------------------------------- */

$("album-summary-toggle").addEventListener("click", () => {
  const panel = $("album-merge-summary");
  panel.hidden = !panel.hidden;
  $("album-summary-toggle").textContent = panel.hidden ? "Details" : "Hide";
  if (!panel.hidden) renderAlbumMergeSummary();
});

// renderAlbumMergeSummary lists every album name that changes and the album it
// becomes, so the whole reduction can be checked and corrected in one place.
// As with artists, a row stays under its heading when unticked rather than
// vanishing, and the heading is where the album is renamed.
function renderAlbumMergeSummary() {
  const panel = $("album-merge-summary");
  if (panel.hidden) return;
  panel.replaceChildren();

  const byTarget = new Map();
  for (const group of albumGroups) {
    for (const source of group.sources) {
      if (source.value === source.proposed) continue;
      const id = albumId(source.owner, source.proposed);
      if (!byTarget.has(id)) {
        byTarget.set(id, { owner: source.owner, name: source.proposed, artist: group.artist, sources: [] });
      }
      byTarget.get(id).sources.push(source);
    }
  }

  const fixes = albumGroups.filter((g) => g.fix);
  if (!byTarget.size && !fixes.length) {
    panel.append(el("div", "row-sub", "Nothing to merge."));
    return;
  }

  // One box for every suggestion here, merges and album artists alike.
  const every = [
    ...[...byTarget.values()].flatMap((b) => b.sources.map((source) => ({ source }))),
    ...fixes.map((group) => ({ group })),
  ];
  panel.append(tickAll(every,
    (item) => (item.source ? albumTicked(item.source) : state.albumArtistFixed.has(item.group.id)),
    (item, on) => {
      if (item.source) setAlbumTicked(item.source, on);
      else if (on) state.albumArtistFixed.add(item.group.id);
      else state.albumArtistFixed.delete(item.group.id);
    },
    `Tick all — ${plural(every.length, "suggestion")}`, renderAlbums));

  const blocks = [...byTarget.values()].sort((a, b) =>
    b.sources.length - a.sources.length || a.name.localeCompare(b.name));
  for (const block of blocks) panel.append(albumSummaryGroup(block));
  if (fixes.length) panel.append(albumFixGroup(fixes));
}

// albumFixGroup lists the albums whose tracks are made to agree on the album
// artist, each with a tick to leave it as it is.
function albumFixGroup(fixes) {
  const block = el("div", "summary-group");
  const head = el("div", "summary-head");
  head.append(tickAll(fixes, (g) => state.albumArtistFixed.has(g.id), (g, on) => {
    if (on) state.albumArtistFixed.add(g.id);
    else state.albumArtistFixed.delete(g.id);
  }, "", renderAlbums), el("b", null, "One album, one album artist"),
    el("span", "count", "a phone shows an album twice when its tracks disagree"));
  block.append(head);

  for (const group of fixes) {
    const row = el("label", "summary-row");
    const kept = !state.albumArtistFixed.has(group.id);
    if (kept) row.classList.add("off");

    const box = el("input");
    box.type = "checkbox";
    box.checked = !kept;
    box.addEventListener("change", () => {
      if (box.checked) state.albumArtistFixed.add(group.id);
      else state.albumArtistFixed.delete(group.id);
      renderAlbums();
      refreshPending();
    });

    row.append(box, el("span", "from", group.name), el("span", "arrow", "·"),
      el("span", "to", fixText(group.fix)), el("span", "count", group.artist));
    block.append(row);
  }
  return block;
}

function albumSummaryGroup({ owner, name, artist, sources }) {
  const block = el("div", "summary-group");
  const head = el("div", "summary-head");

  const field = el("input", "summary-target");
  field.type = "text";
  field.value = name;
  field.spellcheck = false;
  field.setAttribute("list", "album-names");
  field.title = "Type another name to rename the album";

  // Change and blur both fire when the field is left, and the first redraws.
  let committed = false;
  const commit = () => {
    const wanted = field.value.trim();
    if (committed || !wanted || wanted === name) {
      field.value = name;
      return;
    }
    committed = true;
    renameAlbumTo(owner, name, wanted);
  };
  field.addEventListener("keydown", (event) => {
    if (event.key === "Enter") field.blur();
    if (event.key === "Escape") { field.value = name; field.blur(); }
  });
  field.addEventListener("change", commit);
  field.addEventListener("blur", commit);

  const ticked = sources.filter((s) => !state.albumDetached.has(albumId(s.owner, s.value))).length;
  head.append(tickAll(sources, albumTicked, setAlbumTicked, "", renderAlbums),
    field, el("span", "count", `${artist} · ${ticked} of ${plural(sources.length, "name")}`));
  block.append(head);

  for (const source of sources) {
    const id = albumId(source.owner, source.value);
    const row = el("label", "summary-row");
    if (state.albumDetached.has(id)) row.classList.add("off");

    const box = el("input");
    box.type = "checkbox";
    box.checked = !state.albumDetached.has(id);
    box.addEventListener("change", () => {
      if (box.checked) state.albumDetached.delete(id);
      else state.albumDetached.add(id);
      renderAlbums();
      refreshPending();
    });

    row.append(box, el("span", "from", source.value), el("span", "arrow", "→"), el("span", "to", name));
    if (albumKinds[source.kind]) row.append(el("span", "badge", albumKinds[source.kind]));
    row.append(el("span", "count", tracks(source.tracks)));
    block.append(row);
  }
  return block;
}

// renameAlbumTo points every album of one artist now headed for a name at
// another name; typing a name the artist already has merges the two.
function renameAlbumTo(owner, from, wanted) {
  for (const album of state.summary.albums) {
    if (album.owner !== owner) continue;
    const id = albumId(album.owner, album.name);
    if ((state.albumRenamed.get(id) || album.name) !== from) continue;
    if (wanted === album.name) state.albumRenamed.delete(id);
    else state.albumRenamed.set(id, wanted);
  }
  renderAlbums();
  refreshPending();
}

// clearedPaths lists the tracks whose album field goes.
function clearedPaths() {
  const out = [];
  for (const junk of state.summary.junkAlbums) {
    if (state.albumCleared.has(junk.value)) out.push(...(junk.paths || []));
  }
  return out;
}

// renderJunkAlbums offers the album names that are no album — the site a
// download came from, most often — for removal.
function renderJunkAlbums() {
  const junk = state.summary.junkAlbums;
  $("junk-albums").hidden = junk.length === 0;
  const list = $("junk-list");
  list.replaceChildren();

  for (const item of junk) {
    // A plain row rather than a label: a click on the name or the counts must
    // open the list, not tick the name for removal.
    const row = el("div", "variant");
    const box = el("input");
    box.type = "checkbox";
    box.checked = state.albumCleared.has(item.value);
    box.addEventListener("change", () => {
      if (box.checked) state.albumCleared.add(item.value);
      else state.albumCleared.delete(item.value);
      renderAlbums();
      refreshPending();
    });

    // The name opens the list of tracks that carry it, so it is plain what a
    // tick would touch before anything is ticked.
    const open = junkExpanded.has(item.value);
    const toggle = () => {
      if (junkExpanded.has(item.value)) junkExpanded.delete(item.value);
      else junkExpanded.add(item.value);
      renderJunkAlbums();
    };
    const name = el("button", "link", item.value);
    name.addEventListener("click", toggle);
    const counts = el("button", "link count", `${tracks(item.tracks)} · ${plural(item.artists, "artist")}`);
    counts.setAttribute("aria-expanded", String(open));
    counts.addEventListener("click", toggle);

    row.append(box, name);
    row.append(el("span", "badge", item.reason === "site" ? "site name" : `on ${item.artists} artists`), counts);
    list.append(row);

    if (open) {
      const byPath = new Map(state.tracks.map((t) => [t.path, t]));
      const detail = el("div", "junk-tracks");
      const rows = (item.paths || []).map((p) => byPath.get(p)).filter(Boolean)
        .sort((a, b) => String(a.artist).localeCompare(String(b.artist)) || String(a.title).localeCompare(String(b.title)));
      for (const track of rows) {
        const line = el("div", "album-track");
        line.append(el("span", "name", `${track.artist || "—"} — ${track.title || baseName(track.path)}`),
          el("span", "count path", track.path));
        line.addEventListener("contextmenu", (event) => openTrackMenu(event, track));
        detail.append(line);
      }
      list.append(detail);
    }
  }
}

const junkExpanded = new Set();

// albumArtistFix works out how an album's tracks come to agree on who the
// album is by. A phone files an album by its name and its album artist, or by
// its name and its folder when there is no album artist — so "JUICE
// UNRELEASED" with the album artist on 1100 tracks and missing from 112 is two
// albums there. The album artist is not needed, only agreement: the tracks
// that disagree take whatever most of the album has, after the artist renames,
// and that can be none at all when the album sits in one folder. A stray
// compilation flag, which files a track apart the same way, goes too.
function albumArtistFix(group) {
  const rename = renames();
  const artistOf = (track) => {
    const raw = (track.albumArtist || "").trim();
    return rename[raw] || raw;
  };

  const folder = (track) => String(track.path).replace(/[\\/][^\\/]*$/, "");
  const oneFolder = group.tracks.every((t) => folder(t) === folder(group.tracks[0]));

  const counts = new Map();
  for (const track of group.tracks) {
    const name = artistOf(track);
    // Without an album artist the folder holds an album together, so none is
    // only a way to agree when there is one folder.
    if (name || oneFolder) counts.set(name, (counts.get(name) || 0) + 1);
  }
  const flagged = group.tracks.filter((t) => t.compilation);
  // Flags on most of the album mean it really is a compilation.
  const compilation = flagged.length && flagged.length * 2 < group.tracks.length ? flagged : [];

  let artist = null;
  for (const [name, count] of counts) {
    if (artist === null || count > counts.get(artist)) artist = name;
  }
  const paths = artist === null ? [] : group.tracks.filter((t) => artistOf(t) !== artist).map((t) => t.path);

  if (!paths.length && !compilation.length) return null;
  return { artist, paths, compilation: compilation.map((t) => t.path) };
}

// albumArtistRules is what the plan needs to make each album agree.
function albumArtistRules() {
  const albumArtistSet = {};
  const clearCompilation = [];
  for (const group of albumGroups) {
    if (!group.fix || !state.albumArtistFixed.has(group.id)) continue;
    for (const path of group.fix.paths) albumArtistSet[path] = group.fix.artist;
    clearCompilation.push(...group.fix.compilation);
  }
  return { albumArtistSet, clearCompilation };
}

// fixText says in a few words what making an album agree changes.
function fixText(fix) {
  const parts = [];
  if (fix.paths.length) {
    parts.push(fix.artist
      ? `album artist → ${fix.artist} on ${tracks(fix.paths.length)}`
      : `album artist removed from ${tracks(fix.paths.length)}`);
  }
  if (fix.compilation.length) parts.push(`compilation flag off on ${tracks(fix.compilation.length)}`);
  return parts.join(", ");
}

// albumRenames is what the plan needs: the new album name of every track
// whose album ends up called something else. It goes by path, because the
// same name can belong to several artists' albums and only one may be meant.
function albumRenames() {
  const out = {};
  for (const group of albumGroups) {
    for (const source of group.sources) {
      if (source.value === group.name) continue;
      for (const path of source.paths || []) out[path] = group.name;
    }
  }
  return out;
}

function renderAlbums() {
  keepingScroll(drawAlbums);
}

function drawAlbums() {
  if (!state.summary) return;

  albumGroups = buildAlbumGroups();
  renderJunkAlbums();
  renderAlbumMergeSummary();
  const names = $("album-names");
  names.replaceChildren(...[...new Set(albumGroups.map((g) => g.name))].map((name) => {
    const option = document.createElement("option");
    option.value = name;
    return option;
  }));
  $("albums-from").textContent = state.summary.rawAlbums;
  $("albums-to").textContent = albumGroups.length;
  $("count-albums").textContent = albumGroups.length || "";

  const needle = $("album-search").value.trim().toLowerCase();
  const onlyChanged = $("albums-changed").getAttribute("aria-pressed") === "true";
  const list = $("album-list");
  list.replaceChildren();

  let shown = 0;
  for (const group of albumGroups) {
    const folded = group.sources.some((s) => s.value !== group.name);
    // A name kept apart still counts as a merge to look at: it is a decision.
    const touched = folded || group.fix || group.sources.some((s) => s.value !== s.proposed);
    if (onlyChanged && !touched) continue;
    if (needle) {
      const haystack = [group.name, group.artist, ...group.sources.map((s) => s.value)].join(" ").toLowerCase();
      if (!haystack.includes(needle)) continue;
    }
    list.append(albumRow(group));
    shown++;
  }
  if (!shown) {
    list.append(el("div", "row-item muted", onlyChanged ? "No album needs merging." : "No albums match."));
  }
}

function albumRow(group) {
  const folded = group.sources.filter((s) => s.value !== group.name);
  const block = el("div", "group");
  const head = el("div", "group-head");

  const main = el("div", "row-main");
  const name = el("div", "row-name");
  name.append(el("b", null, group.name));
  main.append(name);

  const parts = [group.artist, tracks(group.tracks.length)];
  if (folded.length) parts.push(`${folded.length} folded in`);
  if (group.fix && state.albumArtistFixed.has(group.id)) parts.push(fixText(group.fix));
  main.append(el("div", "row-sub", parts.join(" · ")));

  const details = el("button", "btn small", albumExpanded.has(group.id) ? "Hide" : "Details");
  details.addEventListener("click", () => {
    if (albumExpanded.has(group.id)) albumExpanded.delete(group.id);
    else albumExpanded.add(group.id);
    renderAlbums();
  });

  const mergeInto = el("button", "btn small", "Merge into…");
  mergeInto.addEventListener("click", () => openAlbumMergeInto(group));

  const rename = el("button", "btn small ghost", "Rename");
  rename.addEventListener("click", () => startAlbumRename(block, group));

  head.append(main, details, mergeInto, rename);
  block.append(head);

  if (albumExpanded.has(group.id)) block.append(albumDetails(group));
  if (folded.length || (group.fix && state.albumArtistFixed.has(group.id))) block.classList.add("changed");
  return block;
}

const albumKinds = {
  spelling: "spelling",
  edition: "edition",
  watermark: "shared-by",
  disc: "disc",
  order: "word order",
};

// discOf is the disc a track is on: its own tag, or failing that the "(CD2)"
// its album name carries — which is what the plan fills the tag in from.
const discQualifier = /\s*(?:[([{]\s*(?:cd|disc|disk|диск)\s*(\d+)\s*[)\]}]|[-–—]\s*(?:cd|disc|disk|диск)\s*(\d+))\s*$/i;
function discOf(track) {
  if (track.discNo) return track.discNo;
  const m = discQualifier.exec(track.album || "");
  return m ? Number(m[1] || m[2]) : 0;
}

// albumDetails lists the names that fold into the album, then its tracks.
function albumDetails(group) {
  const panel = el("div", "details");

  if (group.sources.length > 1) {
    panel.append(el("div", "details-head", "Names in the files"));
    const variants = el("div", "group-variants");
    for (const source of group.sources) {
      const row = el("label", "variant");
      const box = el("input");
      box.type = "checkbox";
      box.checked = !state.albumDetached.has(albumId(source.owner, source.value));
      box.disabled = source.value === group.name;
      box.addEventListener("change", () => {
        const id = albumId(source.owner, source.value);
        if (box.checked) state.albumDetached.delete(id);
        else state.albumDetached.add(id);
        renderAlbums();
        refreshPending();
      });

      row.append(box, el("span", null, source.value));
      if (albumKinds[source.kind] && source.value !== group.name) {
        row.append(el("span", "badge", albumKinds[source.kind]));
      }
      row.append(el("span", "count", tracks(source.tracks)));
      variants.append(row);
    }
    panel.append(variants);
  }

  panel.append(el("div", "details-head", tracks(group.tracks.length)));
  const block = el("div", "album");
  const list = [...group.tracks].sort((a, b) => (discOf(a) - discOf(b)) || (a.trackNo - b.trackNo)
    || String(a.title).localeCompare(String(b.title)));

  for (const track of list) {
    const row = el("div", "album-track");
    row.append(
      el("span", "num", track.trackNo ? String(track.trackNo) : ""),
      el("span", "name", `${track.title || baseName(track.path)}${track.album !== group.name ? `  ·  ${track.album}` : ""}`),
      el("span", "count", duration(track.seconds)),
    );
    row.addEventListener("contextmenu", (event) => openTrackMenu(event, track));
    block.append(row);
  }
  panel.append(block);
  return panel;
}

// renameAlbumGroup points every album behind a row at a new name. Two albums
// of one artist aimed at the same name become one.
function renameAlbumGroup(group, value) {
  for (const album of group.albums) {
    const id = albumId(album.owner, album.name);
    if (value && value !== album.name) state.albumRenamed.set(id, value);
    else state.albumRenamed.delete(id);
  }
  // A name kept apart has its own row; renaming that row means it should go
  // with the new name after all.
  for (const source of group.sources) {
    if (source.value === group.name) state.albumDetached.delete(albumId(source.owner, source.value));
  }

  albumExpanded.delete(group.id);
  renderAlbums();
  refreshPending();
}

function startAlbumRename(block, group) {
  const input = el("input", "rename-input");
  input.type = "text";
  input.value = group.name;

  let done = false;
  const commit = () => {
    if (done) return;
    done = true;
    renameAlbumGroup(group, input.value.trim());
  };
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter") commit();
    if (event.key === "Escape") { done = true; renderAlbums(); }
  });
  input.addEventListener("blur", commit);

  block.replaceChildren(input);
  input.focus();
  input.select();
}

// Merging only offers the same artist's albums: an album by somebody else
// stays a separate album whatever it is called, because a player files albums
// by artist too.
function openAlbumMergeInto(group) {
  mergeSource = null;
  albumMergeSource = group;
  $("merge-title").textContent = `Put ${group.name} under…`;
  $("merge-search").value = "";
  renderMergeOptions();
  $("merge-modal").hidden = false;
  $("merge-search").focus();
}

function renderAlbumMergeOptions(needle, list) {
  const options = albumGroups.filter((g) => g !== albumMergeSource && g.owner === albumMergeSource.owner
    && (!needle || g.name.toLowerCase().includes(needle)));

  for (const group of options) {
    const row = el("div", "row-item");
    const main = el("div", "row-main");
    main.append(el("div", "row-name", group.name), el("div", "row-sub", `${group.artist} · ${tracks(group.tracks.length)}`));
    row.append(main);
    row.addEventListener("click", () => commitMerge(group.name));
    list.append(row);
  }
  if (!options.length) {
    list.append(el("div", "row-item muted",
      `No other album by ${albumMergeSource.artist}${needle ? " matches" : ""} — type a name and press Enter to rename`));
  }
}

/* Cleanup ------------------------------------------------------------------ */

for (const radio of document.querySelectorAll('input[name="aa-mode"]')) {
  radio.addEventListener("change", () => {
    state.albumArtistMode = radio.value;
    refreshPending();
  });
}

for (const [id, key] of [
  ["rule-compilation", "removeCompilation"],
  ["rule-sort", "removeSort"],
  ["rule-legacy", "removeLegacy"],
  ["rule-artwork", "shrinkArtwork"],
  ["rule-backup", "backup"],
]) {
  $(id).addEventListener("change", (event) => {
    state[key] = event.target.checked;
    refreshPending();
  });
}

function renderCleanup(result) {
  const s = state.summary;
  $("aa-count").textContent = `${s.albumArtistCount} of ${tracks(s.trackCount)} carry one`;
  $("compilation-count").textContent = s.compilationCount
    ? `set on ${tracks(s.compilationCount)}` : "not set anywhere";
  $("sort-count").textContent = s.sortCount
    ? `set on ${tracks(s.sortCount)}` : "not set anywhere";
  $("artwork-count").textContent = s.oversizedCount
    ? `${tracks(s.oversizedCount)} carry tags over 3 MB — a phone reads none of them`
    : "every file's tags are small enough to read";
  $("legacy-count").textContent = s.legacyCount
    ? `${tracks(s.legacyCount)} carry one — a phone reads it instead of the real tags`
    : "no file carries one";

  state.broken = result.errors || [];
  brokenTicked.clear();
  renderBroken();
}

/* Files that could not be read --------------------------------------------- */

// The files a scan could not read, each with where it is and why, so the
// damaged ones can be found — and deleted, which nothing else here does. Only
// ticked files go, and only after the user confirms the list once more.
const brokenTicked = new Set();

function renderBroken() {
  const broken = state.broken || [];
  $("broken").hidden = broken.length === 0;
  const list = $("broken-list");
  list.replaceChildren();

  for (const failure of broken) {
    const row = el("label", "broken-row");
    const box = el("input");
    box.type = "checkbox";
    box.checked = brokenTicked.has(failure.path);
    box.addEventListener("change", () => {
      if (box.checked) brokenTicked.add(failure.path);
      else brokenTicked.delete(failure.path);
      updateBrokenButtons();
    });

    const text = el("div", "row-main");
    text.append(el("div", "row-name", baseName(failure.path)),
      el("div", "row-sub path", failure.path),
      el("div", "row-sub", failure.reason));
    row.append(box, text);
    list.append(row);
  }
  updateBrokenButtons();
}

function updateBrokenButtons() {
  const broken = state.broken || [];
  $("broken-delete").disabled = brokenTicked.size === 0;
  $("broken-delete").textContent = brokenTicked.size
    ? `Delete ${files(brokenTicked.size)}…` : "Delete ticked files…";
  $("broken-all").checked = broken.length > 0 && broken.every((f) => brokenTicked.has(f.path));
}

$("broken-all").addEventListener("change", (event) => {
  for (const failure of state.broken || []) {
    if (event.target.checked) brokenTicked.add(failure.path);
    else brokenTicked.delete(failure.path);
  }
  renderBroken();
});

$("broken-delete").addEventListener("click", async () => {
  const chosen = [...brokenTicked];
  if (!chosen.length) return;
  // Go asks for confirmation in a system dialog, and deletes nothing when the
  // answer is no.
  $("broken-delete").disabled = true;
  try {
    const result = await go().DeleteUnreadable(chosen);
    if (result.cancelled) {
      updateBrokenButtons();
      return;
    }
    const gone = result.gone || [];
    const removed = new Set(gone);
    state.broken = state.broken.filter((f) => !removed.has(f.path));
    for (const path of gone) brokenTicked.delete(path);
    renderBroken();
    const left = chosen.length - gone.length;
    toast(left ? `Deleted ${files(gone.length)}, ${left} could not be deleted` : `Deleted ${files(gone.length)}`, left > 0);
  } catch (err) {
    toast(String(err), true);
    updateBrokenButtons();
  }
});

/* Tracks ------------------------------------------------------------------- */

// The columns share the width available: a minimum of zero lets them shrink
// and ellipsise rather than pushing the table into a horizontal scroll.
const columns = [
  { key: "trackNo", label: "#", width: "44px", value: (t) => t.trackNo || "", numeric: true },
  { key: "title", label: "Title", width: "minmax(0, 2.2fr)", value: (t) => t.title || baseName(t.path) },
  { key: "artist", label: "Artist", width: "minmax(0, 1.5fr)", value: (t) => t.artist },
  { key: "albumArtist", label: "Album artist", width: "minmax(0, 1.3fr)", value: (t) => t.albumArtist },
  { key: "album", label: "Album", width: "minmax(0, 1.5fr)", value: (t) => t.album },
  { key: "genre", label: "Genre", width: "minmax(0, 1fr)", value: (t) => t.genre },
  { key: "year", label: "Year", width: "54px", value: (t) => t.year || "", numeric: true },
  { key: "seconds", label: "Time", width: "54px", value: (t) => duration(t.seconds), numeric: true },
];

const ROW_HEIGHT = 30;
const gridTemplate = () => `30px ${columns.map((c) => c.width).join(" ")}`;
const selection = new Set();

let sortKey = "artist";
let sortAsc = true;
let visible = [];
let lastClicked = -1;

$("track-search").addEventListener("input", renderTracks);
$("track-body").addEventListener("scroll", paintRows);
$("edit-selected").addEventListener("click", openBulkEditor);

function filteredTracks() {
  const needle = $("track-search").value.trim().toLowerCase();
  let rows = state.tracks;

  if (needle) {
    rows = rows.filter((t) =>
      `${t.title} ${t.artist} ${t.albumArtist} ${t.album} ${t.genre}`.toLowerCase().includes(needle));
  }

  const column = columns.find((c) => c.key === sortKey) || columns[1];
  const direction = sortAsc ? 1 : -1;

  return [...rows].sort((a, b) => {
    const left = a[column.key] ?? "";
    const right = b[column.key] ?? "";
    if (column.numeric) return (left - right) * direction;
    return String(left).localeCompare(String(right), undefined, { sensitivity: "base" }) * direction;
  });
}

function renderTracks() {
  if (!state.summary) return;

  visible = filteredTracks();
  renderHead();
  $("track-count").textContent = tracks(visible.length);

  // The list is a fixed-height window over the rows, so a library of several
  // thousand tracks scrolls without putting all of them in the page at once.
  $("track-spacer").style.height = `${visible.length * ROW_HEIGHT}px`;
  paintRows();
  updateSelectionUI();
}

function renderHead() {
  const head = $("track-head");
  head.style.gridTemplateColumns = gridTemplate();
  head.replaceChildren();

  const all = el("input");
  all.type = "checkbox";
  all.checked = visible.length > 0 && visible.every((t) => selection.has(t.path));
  all.addEventListener("change", () => {
    for (const track of visible) {
      if (all.checked) selection.add(track.path);
      else selection.delete(track.path);
    }
    paintRows();
    updateSelectionUI();
  });

  const cell = el("div", "th check");
  cell.append(all);
  head.append(cell);

  for (const column of columns) {
    const header = el("div", "th sortable", column.label);
    if (column.numeric) header.classList.add("num");
    if (column.key === sortKey) header.classList.add(sortAsc ? "asc" : "desc");
    header.addEventListener("click", () => {
      if (sortKey === column.key) sortAsc = !sortAsc;
      else { sortKey = column.key; sortAsc = true; }
      renderTracks();
    });
    head.append(header);
  }
}

function paintRows() {
  const body = $("track-body");
  const rows = $("track-rows");

  const first = Math.max(0, Math.floor(body.scrollTop / ROW_HEIGHT) - 8);
  const count = Math.ceil(body.clientHeight / ROW_HEIGHT) + 16;

  rows.style.transform = `translateY(${first * ROW_HEIGHT}px)`;
  rows.replaceChildren();

  visible.slice(first, first + count)
    .forEach((track, offset) => rows.append(trackRow(track, first + offset)));
}

function trackRow(track, index) {
  const row = el("div", "tr");
  row.style.gridTemplateColumns = gridTemplate();
  if (selection.has(track.path)) row.classList.add("selected");

  const box = el("input");
  box.type = "checkbox";
  box.checked = selection.has(track.path);
  box.addEventListener("click", (event) => { event.stopPropagation(); toggle(index, event); });

  const cell = el("div", "td check");
  cell.append(box);
  row.append(cell);

  for (const column of columns) {
    const value = el("div", "td", String(column.value(track) ?? ""));
    if (column.numeric) value.classList.add("num");
    row.append(value);
  }

  row.addEventListener("click", (event) => toggle(index, event));
  row.addEventListener("contextmenu", (event) => {
    // Right-clicking outside the selection acts on that row instead.
    if (!selection.has(track.path)) {
      selection.clear();
      selection.add(track.path);
      lastClicked = index;
      paintRows();
      updateSelectionUI();
    }
    openTrackMenu(event, track);
  });
  return row;
}

// toggle follows the conventions of every track list: a plain click replaces
// the selection, a modifier adds one row, and shift takes the range.
function toggle(index, event) {
  const track = visible[index];
  if (!track) return;

  if (event.shiftKey && lastClicked >= 0) {
    const [from, to] = index < lastClicked ? [index, lastClicked] : [lastClicked, index];
    for (let i = from; i <= to; i++) selection.add(visible[i].path);
  } else if (event.ctrlKey || event.metaKey) {
    if (selection.has(track.path)) selection.delete(track.path);
    else selection.add(track.path);
    lastClicked = index;
  } else {
    const onlyThis = selection.size === 1 && selection.has(track.path);
    selection.clear();
    if (!onlyThis) selection.add(track.path);
    lastClicked = index;
  }

  paintRows();
  updateSelectionUI();
}

function updateSelectionUI() {
  $("selected-count").textContent = selection.size ? `${selection.size} selected` : "";
  $("edit-selected").disabled = selection.size === 0;
  renderHead();
}

/* Right-click menu --------------------------------------------------------- */

const menu = $("context-menu");

document.addEventListener("click", () => { menu.hidden = true; });
document.addEventListener("contextmenu", (event) => {
  if (!event.target.closest(".tr, .album-track")) menu.hidden = true;
});
window.addEventListener("blur", () => { menu.hidden = true; });

function openTrackMenu(event, track) {
  event.preventDefault();
  event.stopPropagation();

  const chosen = selection.size > 1 ? `${selection.size} tracks` : "this track";
  const items = [
    {
      label: "Show in file manager",
      disabled: state.onPhone,
      run: async () => {
        try {
          await go().RevealFile(track.path);
        } catch (err) {
          toast(String(err), true);
        }
      },
    },
    { label: "Copy file path", run: () => copyText(track.path) },
    { label: "Copy artist and title", run: () => copyText(`${track.artist} — ${track.title}`) },
    { separator: true },
    {
      label: `Select everything by ${track.artist || "this artist"}`,
      disabled: !track.artist,
      run: () => selectWhere((t) => t.artist === track.artist),
    },
    {
      label: `Select the album ${track.album || ""}`.trim(),
      disabled: !track.album,
      run: () => selectWhere((t) => t.album === track.album),
    },
    { separator: true },
    { label: "Lyrics from a link…", run: () => openLinkLyrics(track) },
    { label: `Edit ${chosen}…`, run: () => { ensureSelected(track); openBulkEditor(); } },
  ];

  menu.replaceChildren();
  for (const item of items) {
    if (item.separator) {
      menu.append(el("div", "menu-separator"));
      continue;
    }
    const button = el("button", "menu-item", item.label);
    button.disabled = Boolean(item.disabled);
    button.addEventListener("click", () => { menu.hidden = true; item.run(); });
    menu.append(button);
  }

  // Shown first so it can be measured, then nudged back inside the window.
  menu.hidden = false;
  const { width, height } = menu.getBoundingClientRect();
  menu.style.left = `${Math.min(event.clientX, window.innerWidth - width - 8)}px`;
  menu.style.top = `${Math.min(event.clientY, window.innerHeight - height - 8)}px`;
}

function ensureSelected(track) {
  if (!selection.has(track.path)) {
    selection.clear();
    selection.add(track.path);
    paintRows();
    updateSelectionUI();
  }
}

function selectWhere(predicate) {
  selection.clear();
  for (const track of state.tracks) {
    if (predicate(track)) selection.add(track.path);
  }
  showView("tracks");
  updateSelectionUI();
  paintRows();
  toast(`${selection.size} selected`);
}

function copyText(text) {
  navigator.clipboard.writeText(text)
    .then(() => toast("Copied"))
    .catch(() => toast("Could not copy", true));
}

/* Bulk editing ------------------------------------------------------------- */

const bulkFields = [
  { key: "artist", label: "Artist" },
  { key: "albumArtist", label: "Album artist" },
  { key: "album", label: "Album" },
  { key: "title", label: "Title" },
  { key: "genre", label: "Genre" },
  { key: "year", label: "Year", numeric: true },
  { key: "trackNo", label: "Track number", numeric: true },
  { key: "discNo", label: "Disc number", numeric: true },
];

$("bulk-close").addEventListener("click", () => { $("bulk-modal").hidden = true; });
$("bulk-confirm").addEventListener("click", applyBulkEdit);

function selectedTracks() {
  return state.tracks.filter((t) => selection.has(t.path));
}

function openBulkEditor() {
  const chosen = selectedTracks();
  if (!chosen.length) return;

  $("bulk-title").textContent = `Edit ${tracks(chosen.length)}`;
  const container = $("bulk-fields");
  container.replaceChildren();

  for (const field of bulkFields) {
    const values = new Set(chosen.map((t) => String(t[field.key] ?? "")));
    const shared = values.size === 1 ? [...values][0] : "";

    const wrap = el("label", "bulk-field");
    wrap.append(el("span", null, field.label));

    const input = el("input");
    input.type = field.numeric ? "number" : "text";
    input.dataset.key = field.key;
    input.dataset.original = shared;
    input.value = shared;
    if (values.size > 1) input.placeholder = "(mixed)";

    const box = el("input");
    box.type = "checkbox";
    box.dataset.clear = field.key;
    box.addEventListener("change", () => { input.disabled = box.checked; });

    const clear = el("label", "bulk-clear");
    clear.append(box, el("span", null, "clear"));

    const row = el("div", "row");
    row.append(input, clear);
    wrap.append(row);
    container.append(wrap);
  }

  $("bulk-modal").hidden = false;
}

async function applyBulkEdit() {
  const edit = {};
  let touched = false;

  for (const field of bulkFields) {
    const input = document.querySelector(`#bulk-fields input[data-key="${field.key}"]`);
    const clear = document.querySelector(`#bulk-fields input[data-clear="${field.key}"]`);

    if (clear.checked) {
      edit[field.key] = field.numeric ? 0 : "";
      touched = true;
      continue;
    }
    // A field nobody typed into is left alone, which is what makes it safe to
    // edit a mixed selection one column at a time.
    if (input.value === input.dataset.original) continue;

    edit[field.key] = field.numeric ? Number(input.value) || 0 : input.value.trim();
    touched = true;
  }

  $("bulk-modal").hidden = true;
  if (!touched) return;

  const paths = [...selection];
  try {
    await go().EditTracks(paths, edit);
    $("revert").hidden = false;
    toast(`Queued changes for ${tracks(paths.length)}`);
    refreshPending();
  } catch (err) {
    toast(String(err), true);
  }
}

$("revert").addEventListener("click", async () => {
  try {
    await go().ClearEdits();
    $("revert").hidden = true;
    refreshPending();
  } catch (err) {
    toast(String(err), true);
  }
});

/* Lyrics from a link ------------------------------------------------------- */

// Searching cannot place every track: a song filed under a transliteration of
// its artist is unreachable from what the file says. Pasting the page settles
// it, and what came back is shown before anything is staged.
let linkTrack = null;
let linkLyrics = "";

$("link-close").addEventListener("click", () => { $("link-modal").hidden = true; });
$("link-fetch").addEventListener("click", fetchLink);
$("link-url").addEventListener("keydown", (event) => {
  if (event.key === "Enter") fetchLink();
});

function openLinkLyrics(track) {
  linkTrack = track;
  linkLyrics = "";

  $("link-title").textContent = `Lyrics for ${track.title || baseName(track.path)}`;
  $("link-url").value = "";
  $("link-status").textContent = "Paste the song's page from a lyrics site.";
  $("link-preview").hidden = true;
  $("link-preview").textContent = "";
  $("link-confirm").disabled = true;
  $("link-modal").hidden = false;
  $("link-url").focus();
}

async function fetchLink() {
  const link = $("link-url").value.trim();
  if (!link) return;

  $("link-fetch").disabled = true;
  $("link-status").textContent = "Reading the page…";
  try {
    const found = await go().LyricsFromLink(link);
    linkLyrics = found.lyrics;

    const lines = linkLyrics.split("\n").length;
    $("link-status").textContent = `${found.source}: ${plural(lines, "line")}`;
    $("link-preview").textContent = linkLyrics;
    $("link-preview").hidden = false;
    $("link-confirm").disabled = false;
  } catch (err) {
    linkLyrics = "";
    $("link-status").textContent = String(err);
    $("link-preview").hidden = true;
    $("link-confirm").disabled = true;
  } finally {
    $("link-fetch").disabled = false;
  }
}

$("link-confirm").addEventListener("click", async () => {
  if (!linkTrack || !linkLyrics) return;
  try {
    await go().EditTracks([linkTrack.path], { lyrics: linkLyrics });
    $("link-modal").hidden = true;
    toast(`Lyrics staged for ${linkTrack.title || baseName(linkTrack.path)}`);
    refreshPending();
  } catch (err) {
    toast(String(err), true);
  }
});

/* Pending plan ------------------------------------------------------------- */

const refreshPending = debounce(async () => {
  if (!state.summary || state.busy) return;

  try {
    const preview = await go().Preview(rules());
    const count = preview.summary.files;
    lastPreview = preview;
    if (!$("pending-bubble").hidden) fillBubble();

    $("pending").textContent = count ? `${files(count)} will change: ${pendingParts(preview.summary)}` : "No changes yet";
    $("pending").classList.toggle("ready", count > 0);
    $("apply").disabled = count === 0;
    $("preview").disabled = count === 0;
  } catch (err) {
    toast(String(err), true);
  }
}, 120);

/* What the count is made of, on hover -------------------------------------- */

// Hovering the count in the footer opens a bubble with the changes that make
// it up: each change once — "Album artist: — → Juice WRLD" — with how many
// songs it touches and on which albums. A change opens to its songs, and a
// song to its file. The bubble stays open while the pointer is over it, so the
// list can be scrolled and clicked through.
let lastPreview = null;
let bubbleTimer = null;
const bubbleOpen = new Set(); // changes and songs opened, by key

function fillBubble() {
  const bubble = $("pending-bubble");
  const preview = lastPreview;
  const scroll = bubble.querySelector(".bubble-list")?.scrollTop || 0;
  bubble.replaceChildren();
  if (!preview || !preview.summary.files) return;

  const total = preview.summary.files;
  bubble.append(el("div", "bubble-head", `${files(total)} will change`),
    el("div", "bubble-sub", pendingParts(preview.summary)));

  // One entry per distinct change, holding the files it is made on.
  const byPath = new Map(state.tracks.map((t) => [t.path, t]));
  const changes = new Map();
  for (const change of preview.changes || []) {
    for (const field of change.fields || []) {
      const key = `${field.name}\u0000${field.before}\u0000${field.after}`;
      if (!changes.has(key)) changes.set(key, { key, field, paths: [] });
      changes.get(key).paths.push(change.path);
    }
  }

  const list = el("div", "bubble-list");
  const entries = [...changes.values()].sort((a, b) => b.paths.length - a.paths.length);
  for (const entry of entries) {
    const songs = entry.paths.map((path) => byPath.get(path) || { path, title: baseName(path), album: "" });
    const albums = [...new Set(songs.map((s) => s.album).filter(Boolean))];
    const open = bubbleOpen.has(entry.key);

    const head = el("button", "bubble-change");
    const where = `${tracks(songs.length)}${albums.length
      ? ` · ${albums.slice(0, 2).join(", ")}${albums.length > 2 ? ` +${albums.length - 2}` : ""}` : ""}`;
    const sub = el("span", "bubble-sub", where);
    sub.title = where; // All of it, when it is cut short.
    head.append(
      el("span", "bubble-caret", open ? "▾" : "▸"),
      el("span", "bubble-what", `${entry.field.name}: ${entry.field.before || "—"} → ${entry.field.after || "—"}`),
      sub,
    );
    head.addEventListener("click", () => toggleBubble(entry.key));
    list.append(head);

    if (!open) continue;
    for (const song of songs.sort((a, b) => String(a.title).localeCompare(String(b.title)))) {
      const songKey = `${entry.key}\u0000${song.path}`;
      const row = el("button", "bubble-song");
      row.append(el("span", "bubble-title", song.title || baseName(song.path)),
        el("span", "bubble-sub", [song.artist, song.album].filter(Boolean).join(" · ")));
      row.addEventListener("click", () => toggleBubble(songKey));
      list.append(row);
      if (bubbleOpen.has(songKey)) list.append(el("div", "bubble-path", song.path));
    }
  }
  bubble.append(list);
  list.scrollTop = scroll;
  if (preview.shown < total) {
    bubble.append(el("div", "bubble-sub", `From the first ${preview.shown} of ${files(total)} — Show changes lists every one`));
  }
}

function toggleBubble(key) {
  if (bubbleOpen.has(key)) bubbleOpen.delete(key);
  else bubbleOpen.add(key);
  fillBubble();
}

function showBubble() {
  clearTimeout(bubbleTimer);
  if (!lastPreview || !lastPreview.summary.files || state.busy) return;
  const bubble = $("pending-bubble");
  if (bubble.hidden) {
    fillBubble();
    bubble.hidden = false;
  }
  placeBubble();
}

// placeBubble sets the bubble just above the count, and again whenever the
// window changes size while it is open.
function placeBubble() {
  const bubble = $("pending-bubble");
  if (bubble.hidden) return;
  const anchor = $("pending").getBoundingClientRect();
  bubble.style.left = `${Math.max(12, anchor.left)}px`;
  bubble.style.bottom = `${window.innerHeight - anchor.top + 12}px`;
}

window.addEventListener("resize", placeBubble);

function hideBubbleSoon() {
  clearTimeout(bubbleTimer);
  bubbleTimer = setTimeout(() => { $("pending-bubble").hidden = true; }, 300);
}

$("pending").addEventListener("mouseenter", showBubble);
$("pending").addEventListener("mouseleave", hideBubbleSoon);
$("pending-bubble").addEventListener("mouseenter", () => clearTimeout(bubbleTimer));
$("pending-bubble").addEventListener("mouseleave", hideBubbleSoon);

// pendingParts says what the changes are, so a number in the footer that
// nobody asked for can be traced to the suggestion behind it.
function pendingParts(s) {
  const parts = [
    [s.artistRenamed, "artist renamed"],
    [s.albumRenamed, "album changed"],
    [s.albumArtistSet, "album artist set"],
    [s.albumArtistCleared, "album artist removed"],
    [s.compilationRemoved, "compilation flag off"],
    [s.genreSet, "genre set"],
  ].filter(([n]) => n > 0).map(([n, what]) => `${what} on ${n}`);
  return parts.length ? parts.join(", ") : "other tags";
}

/* Preview and apply -------------------------------------------------------- */

$("preview").addEventListener("click", showPreview);
$("modal-close").addEventListener("click", () => { $("modal").hidden = true; });
$("modal-apply").addEventListener("click", () => { $("modal").hidden = true; runApply(); });
$("apply").addEventListener("click", runApply);

async function showPreview() {
  try {
    const preview = await go().Preview(rules());
    const body = $("modal-body");
    body.replaceChildren();

    for (const change of preview.changes || []) {
      const block = el("div", "change");
      block.append(el("div", "change-path", change.path));
      for (const field of change.fields) {
        const line = el("div", "change-field");
        line.append(
          el("span", "label", field.name),
          el("span", "before", field.before || "—"),
          el("span", "arrow", "→"),
          el("span", "after", field.after),
        );
        block.append(line);
      }
      body.append(block);
    }

    const total = preview.summary.files;
    $("modal-title").textContent = `${files(total)} will change`;
    $("modal-note").textContent = preview.shown < total ? `Showing the first ${preview.shown}` : "";
    $("modal").hidden = false;
  } catch (err) {
    toast(String(err), true);
  }
}

async function runApply() {
  state.busy = true;
  $("pending-bubble").hidden = true;
  $("apply").disabled = true;
  $("preview").disabled = true;
  $("pending").textContent = "Applying…";

  try {
    const report = await go().Apply();
    const failed = report.failed || [];

    let message = `Changed ${files(report.written)}`;
    if (failed.length) message += `, ${failed.length} failed`;
    if (report.cancelled) message += " (stopped)";
    toast(message, failed.length > 0);

    if (failed.length) console.warn("Could not write:", failed);
  } catch (err) {
    toast(String(err), true);
  } finally {
    state.busy = false;
  }

  // The files on disk have changed, so everything is rebuilt from them.
  rescan();
}

// Writing to a phone has three stages, and the writing is the quick one: the
// phone then has to read every changed file again, and put its playlists
// back. Each is named, so a long wait is never a number that stopped moving.
const applyStages = {
  write: (p) => (state.onPhone ? `Writing tags on the phone: ${p.done} of ${p.total}` : `Writing tags: ${p.done} of ${p.total}`),
  rescan: (p) => `Phone is re-reading the changed files: ${p.done} of ${p.total}`,
  playlists: () => "Putting the playlists back on the phone…",
};

window.runtime.EventsOn("apply:progress", (p) => {
  const describe = applyStages[p.stage] || applyStages.write;
  $("pending").textContent = describe(p);
});

/* Lyrics ------------------------------------------------------------------- */

// Every track the run reported, in order, with the log line drawn for it. The
// line is kept rather than redrawn, so a link typed into it survives switching
// what the log shows.
const lyricsRows = [];
const lyricsByPath = new Map();
// Which counter the log is narrowed to; empty shows everything.
let lyricsFilter = "";

const lyricsMatches = {
  found: (row) => row.status === "found",
  missing: (row) => row.status === "missing",
  error: (row) => row.status === "error" || row.status === "unwritten",
  synced: (row) => row.status === "found" && row.synced,
};

const showsRow = (row) => !lyricsFilter || lyricsMatches[lyricsFilter](row);

$("lyrics-start").addEventListener("click", runLyrics);
$("lyrics-cancel").addEventListener("click", () => go().Cancel());

$("lyrics-filters").addEventListener("click", (event) => {
  const button = event.target.closest("button[data-filter]");
  if (!button) return;
  lyricsFilter = lyricsFilter === button.dataset.filter ? "" : button.dataset.filter;
  paintLyricsLog();
});

async function runLyrics() {
  lyricsRows.length = 0;
  lyricsByPath.clear();
  lyricsFilter = "";
  paintLyricsLog();

  $("lyrics-bar").style.width = "0";
  $("lyrics-start").disabled = true;
  $("lyrics-cancel").disabled = false;
  $("lyrics-status").textContent = "Searching…";

  try {
    const report = await go().FetchLyrics({
      onlyMissing: $("lyrics-missing").checked,
      backup: $("lyrics-backup").checked,
      preferSynced: $("lyrics-synced").checked,
      artists: [],
    });

    $("lyrics-status").textContent = report.cancelled
      ? `Stopped — found ${report.found} of ${report.total}`
      : `Done — found ${report.found} of ${report.total}`;
  } catch (err) {
    $("lyrics-status").textContent = "Error";
    toast(String(err), true);
  } finally {
    $("lyrics-start").disabled = false;
    $("lyrics-cancel").disabled = true;
  }
}

function paintLyricsCounts() {
  const count = (kind) => lyricsRows.filter(lyricsMatches[kind]).length;
  $("lyrics-found").textContent = count("found");
  $("lyrics-missing-count").textContent = count("missing");
  $("lyrics-failed").textContent = count("error");
  $("lyrics-synced-count").textContent = count("synced");
}

function paintLyricsLog() {
  for (const button of $("lyrics-filters").querySelectorAll("button")) {
    button.classList.toggle("active", button.dataset.filter === lyricsFilter);
  }
  $("lyrics-log").replaceChildren(...lyricsRows.filter(showsRow).map((row) => row.node));
  paintLyricsCounts();
}

// drawLyricsRow fills a row's line for what is now known about the track.
function drawLyricsRow(row) {
  const label = `${row.artist} — ${row.title}`;
  const node = row.node;
  node.replaceChildren();

  switch (row.status) {
    case "found":
      node.className = "found";
      node.textContent = `✓ ${label}  [${row.source}]${row.synced ? "  · timed" : ""}`;
      return;
    case "unwritten":
      node.className = "error";
      node.textContent = `✗ ${label} — found, but not written: ${row.detail}`;
      return;
    case "missing":
      node.className = "missing";
      node.append(el("div", "", `· ${label} — not found${row.detail ? ` (${row.detail})` : ""}`));
      break;
    default:
      node.className = "error";
      node.append(el("div", "", `✗ ${label} — ${row.detail}`));
  }
  node.append(linkControls(row));
}

// linkControls is the place on a line the search could not fill where the
// song's page can be pasted instead. What comes back is shown, and only
// staged — like any other edit, it is written from the footer.
function linkControls(row) {
  const box = el("div", "link-box");
  const line = el("div", "link-row");
  const input = el("input", "search");
  input.type = "url";
  input.spellcheck = false;
  input.placeholder = "Song page on Genius, AZLyrics, Musixmatch…";
  const fetchButton = el("button", "btn small", "Fetch");
  const status = el("span", "muted");
  const use = el("button", "btn small primary", "Use these lyrics");
  use.hidden = true;
  const preview = el("div", "lyrics-preview compact");
  preview.hidden = true;
  line.append(input, fetchButton, status, use);
  box.append(line, preview);

  let text = "";

  const fetchPage = async () => {
    const link = input.value.trim();
    if (!link) return;
    fetchButton.disabled = true;
    use.hidden = true;
    status.textContent = "Reading the page…";
    try {
      const found = await go().LyricsFromLink(link);
      text = found.lyrics;
      status.textContent = `${found.source}: ${plural(text.split("\n").length, "line")}`;
      preview.textContent = text;
      preview.hidden = false;
      use.hidden = false;
    } catch (err) {
      text = "";
      status.textContent = String(err);
      preview.hidden = true;
    } finally {
      fetchButton.disabled = false;
    }
  };

  fetchButton.addEventListener("click", fetchPage);
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter") fetchPage();
  });
  use.addEventListener("click", async () => {
    if (!text) return;
    try {
      await go().EditTracks([row.path], { lyrics: text });
      input.disabled = fetchButton.disabled = true;
      use.hidden = true;
      preview.hidden = true;
      status.textContent = "Staged — written when you apply";
      refreshPending();
    } catch (err) {
      toast(String(err), true);
    }
  });

  return box;
}

window.runtime.EventsOn("lyrics:progress", (p) => {
  $("lyrics-bar").style.width = p.total ? `${(p.done / p.total) * 100}%` : "0";
  $("lyrics-status").textContent = `Processed ${p.done} of ${p.total}`;
});

// What the run is doing once every track has been looked up; on a phone that
// takes long enough to look like a hang otherwise.
window.runtime.EventsOn("lyrics:phase", (text) => {
  $("lyrics-status").textContent = text;
});

window.runtime.EventsOn("lyrics:track", (result) => {
  // A track that was found and then failed to write comes back a second
  // time, and its line is corrected rather than added again.
  let row = lyricsByPath.get(result.path);
  if (row && result.status === "unwritten") {
    row.status = "unwritten";
    row.detail = result.detail;
  } else {
    row = { ...result, node: el("div") };
    lyricsRows.push(row);
    if (result.path) lyricsByPath.set(result.path, row);
  }
  drawLyricsRow(row);

  if (!showsRow(row)) {
    row.node.remove();
  } else if (!row.node.isConnected) {
    const log = $("lyrics-log");
    const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
    log.append(row.node);
    if (atBottom) log.scrollTop = log.scrollHeight;
  }
  paintLyricsCounts();
});

/* Playlists ---------------------------------------------------------------- */

// The playlists read so far, each with the name it will be saved under. Go
// keeps the playlists themselves; this keeps what the user chose about them.
const lists = {
  items: [],
  targets: [],
  target: "android",
  folder: "",
  phone: null,      // a ready phone, for a library on this computer
  loadedFor: "",    // the library the list was first filled from
  busy: false,
};

const PLAYLIST_TARGET_KEY = "mlo-playlist-target";

async function openPlaylists() {
  if (!lists.targets.length) {
    try {
      lists.targets = (await go().PlaylistTargets()) || [];
      const saved = localStorage.getItem(PLAYLIST_TARGET_KEY);
      if (lists.targets.some((t) => t.id === saved)) lists.target = saved;
    } catch {}
    renderTargets();
  }
  updateDestinations();
  if (lists.loadedFor !== state.root) {
    lists.loadedFor = state.root;
    lists.items = [];
    $("pl-log").replaceChildren();
    renderPlaylists();
    await loadPlaylists(() => go().LibraryPlaylists(),
      state.onPhone ? "Reading the playlists on the phone…" : "Looking for playlists in the library…");
  }
  if (!state.onPhone) findPhone();
}

function setPlaylistsBusy(busy, status) {
  lists.busy = busy;
  for (const id of ["pl-add", "pl-library", "pl-clear"]) $(id).disabled = busy;
  $("pl-phone").disabled = busy || (!state.onPhone && !lists.phone);
  $("pl-cancel").hidden = !busy;
  if (status !== undefined) $("pl-status").textContent = status;
  updateSaveButton();
}

// loadPlaylists adds what fetch returns to the list, leaving out a playlist
// that is already there.
async function loadPlaylists(fetch, status) {
  setPlaylistsBusy(true, status);
  let added = 0;
  try {
    const found = (await fetch()) || [];
    const known = new Set(lists.items.map((item) => `${item.path}\u0000${item.name}`));
    for (const item of found) {
      const k = `${item.path}\u0000${item.name}`;
      if (known.has(k)) continue;
      known.add(k);
      lists.items.push({ ...item, ticked: true, saveAs: item.name, result: null });
      added++;
    }
    $("pl-status").textContent = found.length
      ? `${plural(added, "playlist")} added`
      : "No playlists found";
  } catch (err) {
    $("pl-status").textContent = "";
    toast(String(err), true);
  } finally {
    setPlaylistsBusy(false);
    renderPlaylists();
  }
}

$("pl-add").addEventListener("click", () => loadPlaylists(() => go().ChoosePlaylistFiles(), "Reading…"));
$("pl-library").addEventListener("click", () => loadPlaylists(() => go().LibraryPlaylists(),
  state.onPhone ? "Reading the playlists on the phone…" : "Looking for playlists in the library…"));
$("pl-phone").addEventListener("click", () => {
  if (state.onPhone) {
    loadPlaylists(() => go().LibraryPlaylists(), "Reading the playlists on the phone…");
  } else if (lists.phone) {
    loadPlaylists(() => go().PhonePlaylists(lists.phone.serial), `Reading the playlists on ${lists.phone.model || "the phone"}…`);
  }
});
$("pl-clear").addEventListener("click", () => {
  lists.items = [];
  $("pl-log").replaceChildren();
  $("pl-status").textContent = "";
  renderPlaylists();
});
$("pl-all").addEventListener("change", () => {
  for (const item of lists.items) item.ticked = $("pl-all").checked;
  renderPlaylists();
});
$("pl-cancel").addEventListener("click", () => go().Cancel());

// findPhone looks for a phone to save on, for a library on this computer.
async function findPhone() {
  try {
    const status = await go().Phones();
    lists.phone = (status.devices || []).find((d) => d.ready) || null;
  } catch {
    lists.phone = null;
  }
  updateDestinations();
  setPlaylistsBusy(lists.busy);
}

// The list is grouped by format — iTunes', Android's, and so on — and each
// group has a box of its own, so all of one player's playlists can be taken or
// left together. A group's key is the player it is written for, which is how
// the playlists already made for the chosen player are found.
const playlistGroups = [
  { key: "itunes", title: "iTunes and Music" },
  { key: "android", title: "Android — Phonograph and the rest (.m3u)" },
  { key: "m3u8", title: "VLC, foobar2000, AIMP, Winamp, MusicBee (.m3u8)" },
  { key: "wpl", title: "Windows Media Player (.wpl)" },
  { key: "xspf", title: "XSPF (.xspf)" },
  { key: "pls", title: "PLS (.pls)" },
];

function playlistGroup(item) {
  switch (item.format) {
    case "Music": case "iTunes": case "iTunes XML": return "itunes";
    case "M3U": return "android";
    case "M3U8": return "m3u8";
    case "WPL": case "ZPL": return "wpl";
    case "XSPF": return "xspf";
    case "PLS": return "pls";
  }
  return "android";
}

function renderPlaylists() {
  const list = $("pl-list");
  const rows = [];
  for (const group of playlistGroups) {
    const items = lists.items.filter((item) => playlistGroup(item) === group.key);
    if (!items.length) continue;
    rows.push(playlistGroupHead(group, items), ...items.map(playlistRow));
  }
  list.replaceChildren(...rows);
  $("count-playlists").textContent = lists.items.length || "";
  $("pl-all").checked = lists.items.length > 0 && lists.items.every((item) => item.ticked);
  renderExisting();
  updateSaveButton();
}

function playlistGroupHead(group, items) {
  const head = el("label", "row-item playlist-group");
  const box = el("input");
  box.type = "checkbox";
  const ticked = items.filter((item) => item.ticked).length;
  box.checked = ticked === items.length;
  box.indeterminate = ticked > 0 && ticked < items.length;
  box.addEventListener("change", () => {
    for (const item of items) item.ticked = box.checked;
    renderPlaylists();
  });
  head.append(box, el("b", "", group.title), el("span", "muted", `${ticked} of ${plural(items.length, "playlist")} ticked`));
  return head;
}

function playlistRow(item) {
  const row = el("div", "row-item playlist");
  const box = el("input");
  box.type = "checkbox";
  box.checked = item.ticked;
  box.addEventListener("change", () => {
    item.ticked = box.checked;
    renderPlaylists();
  });

  const main = el("div", "row-main");
  const name = el("input", "rename-input");
  name.type = "text";
  name.spellcheck = false;
  name.value = item.saveAs;
  name.title = "The name it is saved under";
  name.addEventListener("input", () => { item.saveAs = name.value; });
  const where = item.where === "phone" ? "on the phone" : "on this computer";
  const from = item.format === "Music" || item.path === "iTunes" ? `playlist in ${item.path}` : `${item.format} · ${where} · ${item.path}`;
  main.append(name, el("div", "row-sub path", from));
  if (item.result) main.append(resultLine(item.result));
  if (item.showMissing && (item.missing || []).length) main.append(missingList(item.missing));

  const facts = el("div", "playlist-facts");
  if (item.found >= 0) {
    const missing = item.missing || [];
    const found = el(missing.length ? "button" : "span", missing.length ? "badge certain clickable" : "badge",
      `${item.found} of ${tracks(item.entries)} in the library${missing.length ? (item.showMissing ? " ▴" : " ▾") : ""}`);
    found.title = missing.length ? "Show the tracks that are not in the library" : "Tracks of the playlist found in the open library";
    if (missing.length) {
      found.addEventListener("click", () => {
        item.showMissing = !item.showMissing;
        renderPlaylists();
      });
    }
    facts.append(found);
  } else {
    facts.append(el("span", "badge", tracks(item.entries)));
  }
  if (item.visible >= 0) {
    const shows = el("span", item.visible < item.entries ? "badge certain" : "badge", `phone shows ${item.visible}`);
    shows.title = "Tracks the phone's media library matched — what Phonograph and other players show";
    facts.append(shows);
  }

  row.append(box, main, facts);
  return row;
}

// missingList names the entries of a playlist the library has no file for:
// what the playlist calls the track, and the path it gives.
function missingList(missing) {
  const list = el("div", "missing-list");
  list.append(el("div", "missing-head", `Not in the library (${missing.length}):`));
  for (const entry of missing) {
    const row = el("div", "missing-entry");
    const label = entry.title ? (entry.artist ? `${entry.artist} — ${entry.title}` : entry.title) : baseName(entry.location);
    row.append(el("div", "", label), el("div", "row-sub path", entry.location));
    row.title = entry.location;
    list.append(row);
  }
  return list;
}

function resultLine(result) {
  if (result.error) return el("div", "row-sub error-text", `✗ ${result.error}`);
  if (result.retired) return el("div", "row-sub", "↷ removed");
  let text = `→ ${baseName(result.file)}: ${result.found} of ${tracks(result.entries)}`;
  if (result.visible >= 0) text += ` · the phone shows ${result.visible}`;
  return el("div", result.found < result.entries ? "row-sub warn-text" : "row-sub ok-text", text);
}

function renderTargets() {
  const box = $("pl-targets");
  box.replaceChildren();
  for (const target of lists.targets) {
    const label = el("label", "choice");
    const radio = el("input");
    radio.type = "radio";
    radio.name = "pl-target";
    radio.value = target.id;
    radio.checked = target.id === lists.target;
    radio.dataset.absolute = String(target.absolute);
    radio.addEventListener("change", () => {
      lists.target = target.id;
      try { localStorage.setItem(PLAYLIST_TARGET_KEY, target.id); } catch {}
      updateDestinations();
    });
    const text = el("span");
    text.append(el("b", "", target.title), el("em", "", target.detail));
    label.append(radio, text);
    box.append(label);
  }
}

const destination = () => document.querySelector('input[name="pl-dest"]:checked')?.value || "library";

function updateDestinations() {
  $("pl-dest-library").textContent = state.onPhone ? `${state.root} on the phone` : state.root;
  $("pl-dest-folder-row").hidden = state.onPhone;
  $("pl-dest-phone-row").hidden = state.onPhone || !lists.phone;
  if (lists.phone) $("pl-phone-name").textContent = `On the phone · ${lists.phone.model || lists.phone.serial}`;
  $("pl-folder").textContent = lists.folder || "none chosen";

  // A choice that is no longer offered falls back to beside the music.
  const chosen = document.querySelector('input[name="pl-dest"]:checked');
  if (chosen && chosen.closest("label").hidden) {
    document.querySelector('input[name="pl-dest"][value="library"]').checked = true;
  }

  const onPhone = state.onPhone || destination() === "phone";
  const target = lists.targets.find((t) => t.id === lists.target);
  const note = $("pl-dest-note");
  note.hidden = !(onPhone && target && target.absolute);
  note.textContent = "iTunes and Music need paths on a computer: save these on this computer, or pick another player for the phone.";
  renderExisting();
  updateSaveButton();
}

for (const radio of document.querySelectorAll('input[name="pl-dest"]')) {
  radio.addEventListener("change", updateDestinations);
}
$("pl-phone-folder").addEventListener("input", renderExisting);

$("pl-folder-pick").addEventListener("click", async (event) => {
  event.preventDefault();
  try {
    const folder = await go().ChoosePlaylistFolder();
    if (folder) {
      lists.folder = folder;
      document.querySelector('input[name="pl-dest"][value="folder"]').checked = true;
      updateDestinations();
    }
  } catch (err) {
    toast(String(err), true);
  }
});

// Paths compared as folders: a phone's storage goes by several names.
function folderKey(path) {
  return String(path || "").replace(/\\/g, "/").replace(/^\/sdcard(?=\/|$)/, "/storage/emulated/0").replace(/\/+$/, "").toLowerCase();
}

// destinationFolder is where the playlists are about to go, and whether that
// is on a phone.
function destinationFolder() {
  switch (destination()) {
    case "folder": return { folder: lists.folder, phone: false };
    case "phone": return { folder: $("pl-phone-folder").value.trim(), phone: true };
    default: return { folder: state.root, phone: state.onPhone };
  }
}

// existingPlaylists are the playlists for the chosen player that already lie
// where the new ones will go.
function existingPlaylists() {
  const dest = destinationFolder();
  if (!dest.folder) return [];
  return lists.items.filter((item) => playlistGroup(item) === lists.target
    && (item.where === "phone") === dest.phone
    && !(item.result && item.result.retired)
    && folderKey(item.path.slice(0, item.path.replace(/\\/g, "/").lastIndexOf("/"))) === folderKey(dest.folder));
}

function renderExisting() {
  const box = $("pl-existing");
  if (!box) return;
  const existing = existingPlaylists();
  box.hidden = existing.length === 0;
  if (!existing.length) return;
  const target = lists.targets.find((t) => t.id === lists.target);
  $("pl-existing-title").textContent = `${plural(existing.length, "playlist")} for ${target ? target.title.split(" — ")[0] : "this player"} already there: ${existing.map((i) => i.name).join(", ")}`;
}

function updateSaveButton() {
  const ticked = lists.items.filter((item) => item.ticked).length;
  const blocked = !$("pl-dest-note").hidden || (destination() === "folder" && !lists.folder);
  $("pl-save").disabled = lists.busy || ticked === 0 || blocked;
  $("pl-save").textContent = ticked ? `Save ${plural(ticked, "playlist")}` : "Save playlists";
}

$("pl-save").addEventListener("click", async () => {
  const chosen = lists.items.filter((item) => item.ticked);
  if (!chosen.length) return;
  const request = {
    items: chosen.map((item) => ({ id: item.id, name: item.saveAs.trim() || item.name })),
    target: lists.target,
    destination: destination(),
    folder: destination() === "folder" ? lists.folder : destination() === "phone" ? $("pl-phone-folder").value.trim() : "",
    serial: destination() === "phone" && lists.phone ? lists.phone.serial : "",
    backup: $("pl-backup").checked,
    retire: $("pl-existing").hidden || !document.querySelector('input[name="pl-mode"][value="replace"]').checked
      ? [] : existingPlaylists().map((item) => item.id),
  };
  for (const item of chosen) item.result = null;
  $("pl-log").replaceChildren();
  renderPlaylists();

  setPlaylistsBusy(true, "Saving…");
  try {
    const results = (await go().SavePlaylists(request)) || [];
    const failed = results.filter((r) => r.error).length;
    $("pl-status").textContent = failed
      ? `Saved ${results.length - failed} of ${results.length} — see below`
      : `Saved ${plural(results.length, "playlist")}`;
  } catch (err) {
    $("pl-status").textContent = "";
    toast(String(err), true);
  } finally {
    setPlaylistsBusy(false);
  }
});

window.runtime.EventsOn("playlists:notice", (text) => toast(text, true));

window.runtime.EventsOn("playlists:phase", (text) => {
  if (text) $("pl-status").textContent = text;
});

window.runtime.EventsOn("playlists:saved", (result) => {
  const item = lists.items.find((i) => i.id === result.id);
  if (item) item.result = result;
  renderPlaylists();

  const log = $("pl-log");
  if (result.retired) {
    log.append(el("div", result.error ? "error" : "note", result.error
      ? `✗ ${result.file}: could not remove it — ${result.error}` : `↷ ${result.file} removed`));
    return;
  }
  if (result.error) {
    log.append(el("div", "error", `✗ ${result.name}: ${result.error}`));
  } else {
    log.append(el("div", "found", `✓ ${result.file} — ${result.found} of ${tracks(result.entries)}`));
    if (result.visible >= 0) {
      log.append(el("div", result.visible < result.found ? "error" : "note",
        `  the phone shows ${result.visible} of them${result.visible < result.found ? " — it may still be reading the file; look again in a minute" : ""}`));
    }
  }
  const missing = result.missing || [];
  if (missing.length) {
    log.append(el("div", "missing", `  ${plural(missing.length, "track")} not found here:`));
    for (const location of missing.slice(0, 25)) log.append(el("div", "missing", `    ${location}`));
    if (missing.length > 25) log.append(el("div", "missing", `    …and ${missing.length - 25} more`));
  }
});

// A folder passed on the command line, or dropped onto the application, opens
// without waiting for the picker.
window.runtime.EventsOn("open:folder", (folder) => startScan(folder));
