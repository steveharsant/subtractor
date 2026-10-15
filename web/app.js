"use strict";

const $ = (id) => document.getElementById(id);

const state = {
  current: null, // { kind: "video" | "collection", item } shown in the detail view
  downloadUrl: null, // base download URL of the shown result, without ?format=
  pending: [], // files chosen but not yet uploaded, in upload order
  uploadXHR: null,
  uploadCancelled: false,
  whisper: null, // last /api/whisper status
  libraryPending: false, // some library item has a queued or running transcript
};

// ---------- API helpers ----------

async function api(path, options = {}) {
  const res = await fetch(path, { credentials: "same-origin", ...options });
  if (res.status === 401) {
    showLogin();
    throw new Error("authentication required");
  }
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || `request failed (${res.status})`);
  return body;
}

// ---------- View switching ----------

function showLogin() {
  $("app-view").classList.add("hidden");
  $("login-view").classList.remove("hidden");
  $("password").value = "";
}

function showApp() {
  $("login-view").classList.add("hidden");
  $("app-view").classList.remove("hidden");
  refreshWhisper();
  refreshLibrary();
}

// ---------- Formatting ----------

function formatBytes(n) {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(2) + " GiB";
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + " MiB";
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(0) + " KiB";
  return n + " B";
}

function formatDuration(seconds) {
  if (!seconds) return "";
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  return h > 0 ? `${h}h ${m}m ${s}s` : `${m}m ${s}s`;
}

function daysUntil(iso) {
  const ms = new Date(iso).getTime() - Date.now();
  return Math.max(0, Math.ceil(ms / 86400000));
}

function streamLabel(stream) {
  const parts = [];
  if (stream.title) parts.push(stream.title);
  if (stream.language) parts.push(stream.language.toUpperCase());
  if (parts.length === 0) parts.push(`Track ${stream.index}`);
  return parts.join(" · ");
}

// ---------- Login / logout ----------

$("login-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const btn = $("login-btn");
  const errEl = $("login-error");
  errEl.classList.add("hidden");
  btn.disabled = true;
  try {
    const res = await fetch("/api/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify({
        username: $("username").value,
        password: $("password").value,
      }),
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(body.error || "login failed");
    showApp();
  } catch (err) {
    errEl.textContent = err.message;
    errEl.classList.remove("hidden");
  } finally {
    btn.disabled = false;
  }
});

$("logout-btn").addEventListener("click", async () => {
  try {
    await fetch("/api/logout", { method: "POST", credentials: "same-origin" });
  } finally {
    clearPending();
    closeDetail();
    showLogin();
  }
});

// ---------- Whisper status ----------

async function refreshWhisper() {
  try {
    state.whisper = await api("/api/whisper");
  } catch {
    return;
  }
  const w = state.whisper;
  const el = $("whisper-status");
  el.classList.remove("error");
  if (w.state === "unavailable") {
    el.textContent = "Whisper speech-to-text is not installed on this server.";
  } else if (w.state === "downloading") {
    const pct = w.total ? Math.floor((w.downloaded / w.total) * 100) : 0;
    el.textContent = `Whisper: downloading the ${w.model} model (first run)… ${pct}% of ${formatBytes(w.total)}. Transcripts start once it finishes.`;
  } else if (w.state === "error") {
    el.textContent = `Whisper: ${w.error}`;
    el.classList.add("error");
  } else {
    el.textContent = `Whisper: ${w.model} model ready (${w.reason}).`;
  }
  updateOptions();
}

function whisperAvailable() {
  return !!state.whisper && state.whisper.state !== "unavailable";
}

// ---------- File selection ----------

const dropzone = $("dropzone");
const fileInput = $("file-input");

dropzone.addEventListener("click", () => fileInput.click());
dropzone.addEventListener("keydown", (e) => {
  if (e.key === "Enter" || e.key === " ") {
    e.preventDefault();
    fileInput.click();
  }
});
fileInput.addEventListener("change", () => {
  addPending(fileInput.files);
  fileInput.value = "";
});

["dragenter", "dragover"].forEach((evt) =>
  dropzone.addEventListener(evt, (e) => {
    e.preventDefault();
    dropzone.classList.add("dragover");
  })
);
["dragleave", "drop"].forEach((evt) =>
  dropzone.addEventListener(evt, (e) => {
    e.preventDefault();
    dropzone.classList.remove("dragover");
  })
);
dropzone.addEventListener("drop", (e) => addPending(e.dataTransfer.files));

const byName = (a, b) => a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: "base" });

function addPending(files) {
  if (!files || files.length === 0) return;
  $("upload-error").classList.add("hidden");
  // New files are sorted by name ("part 2" before "part 10") and appended.
  state.pending.push(...Array.from(files).sort(byName));
  $("combine-name").placeholder = suggestName(state.pending);
  renderPending();
}

function clearPending() {
  state.pending = [];
  $("opt-combine").checked = false;
  $("combine-name").value = "";
  renderPending();
}

// suggestName proposes a combined transcript name from the files' common prefix.
function suggestName(files) {
  const names = files.map((f) => f.name.replace(/\.[^.]+$/, ""));
  let prefix = names[0] || "";
  for (const n of names) {
    while (!n.startsWith(prefix)) prefix = prefix.slice(0, -1);
  }
  prefix = prefix.replace(/[\s._\-([]*(part|pt|p)?[\s._\-]*\d*$/i, "").trim();
  return prefix.length >= 3 ? prefix : names[0] || "Combined transcript";
}

function renderPending() {
  const list = $("pending-list");
  list.textContent = "";
  $("pending").classList.toggle("hidden", state.pending.length === 0);

  state.pending.forEach((file, i) => {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.className = "pending-name";
    name.textContent = file.name;
    const size = document.createElement("span");
    size.className = "muted";
    size.textContent = formatBytes(file.size);

    const actions = document.createElement("span");
    actions.className = "pending-buttons";
    actions.append(
      iconButton("↑", `Move ${file.name} up`, i === 0, () => movePending(i, -1)),
      iconButton("↓", `Move ${file.name} down`, i === state.pending.length - 1, () => movePending(i, 1)),
      iconButton("✕", `Remove ${file.name}`, false, () => {
        state.pending.splice(i, 1);
        renderPending();
      })
    );
    li.append(name, size, actions);
    list.appendChild(li);
  });
  updateOptions();
}

function iconButton(text, label, disabled, onClick) {
  const b = document.createElement("button");
  b.className = "ghost icon";
  b.textContent = text;
  b.title = label;
  b.setAttribute("aria-label", label);
  b.disabled = disabled;
  b.addEventListener("click", onClick);
  return b;
}

function movePending(i, delta) {
  const [file] = state.pending.splice(i, 1);
  state.pending.splice(i + delta, 0, file);
  renderPending();
}

function updateOptions() {
  const available = whisperAvailable();
  const multi = state.pending.length >= 2;
  const combine = $("opt-combine");
  $("opt-combine-row").classList.toggle("hidden", !multi);
  if (!multi || !available) combine.checked = false;
  combine.disabled = !available;

  const transcribe = $("opt-transcribe");
  if (!available) transcribe.checked = false;
  if (combine.checked) transcribe.checked = true; // a combined transcript needs every part transcribed
  transcribe.disabled = !available || combine.checked;
  $("combine-name-row").classList.toggle("hidden", !combine.checked);
  $("pending-upload").textContent =
    state.pending.length > 1 ? `Upload ${state.pending.length} files` : "Upload";
}

$("opt-combine").addEventListener("change", updateOptions);
$("pending-clear").addEventListener("click", clearPending);
$("pending-upload").addEventListener("click", startUpload);

// ---------- Upload ----------

async function startUpload() {
  const files = state.pending.slice();
  if (files.length === 0) return;
  const transcribe = $("opt-transcribe").checked;
  const combine = $("opt-combine").checked && files.length >= 2;
  const name = $("combine-name").value.trim() || $("combine-name").placeholder;

  const errEl = $("upload-error");
  errEl.classList.add("hidden");
  $("pending").classList.add("hidden");
  dropzone.classList.add("hidden");
  $("upload-progress").classList.remove("hidden");
  state.uploadCancelled = false;

  const total = files.reduce((n, f) => n + f.size, 0) || 1;
  let done = 0;
  const uploaded = [];
  try {
    for (const [i, file] of files.entries()) {
      const label = files.length > 1 ? `file ${i + 1} of ${files.length}: ${file.name}` : file.name;
      const meta = await uploadFile(file, transcribe, label, (loaded) => {
        setProgress(((done + loaded) / total) * 100);
      });
      uploaded.push(meta);
      done += file.size;
    }

    clearPending();
    if (combine) {
      $("upload-status").textContent = "Creating combined transcript…";
      const collection = await api("/api/collections", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name, videoIds: uploaded.map((v) => v.id) }),
      });
      openCollection(collection);
    } else if (uploaded.length === 1) {
      openDetail(uploaded[0]);
    }
  } catch (err) {
    if (!state.uploadCancelled) {
      // Keep the files that did not make it so they can be retried.
      state.pending = files.slice(uploaded.length);
      let msg = err.message;
      if (uploaded.length > 0) {
        msg += ` (${uploaded.length} file${uploaded.length === 1 ? " was" : "s were"} uploaded before the failure and are in the library)`;
      }
      errEl.textContent = msg;
      errEl.classList.remove("hidden");
    }
    renderPending();
  } finally {
    resetUploadUI();
    refreshLibrary();
  }
}

function setProgress(pct) {
  $("progress-bar").style.width = Math.min(100, pct).toFixed(1) + "%";
}

// uploadFile sends one file and resolves with its VideoMeta.
function uploadFile(file, transcribe, label, onProgress) {
  return new Promise((resolve, reject) => {
    const form = new FormData();
    form.append("transcribe", transcribe ? "1" : "0"); // must precede the file
    form.append("file", file);

    const xhr = new XMLHttpRequest();
    state.uploadXHR = xhr;
    xhr.open("POST", "/api/upload");
    xhr.withCredentials = true;

    xhr.upload.addEventListener("progress", (e) => {
      if (!e.lengthComputable) return;
      onProgress(e.loaded * (file.size / e.total));
      $("upload-status").textContent =
        e.loaded < e.total
          ? `Uploading ${label}… ${Math.round((e.loaded / e.total) * 100)}%`
          : `Analysing ${label}…`;
    });
    xhr.addEventListener("load", () => {
      let body = {};
      try {
        body = JSON.parse(xhr.responseText);
      } catch {}
      if (xhr.status === 401) {
        showLogin();
        return reject(new Error("authentication required"));
      }
      if (xhr.status >= 400) return reject(new Error(`${file.name}: ${body.error || `upload failed (${xhr.status})`}`));
      resolve(body);
    });
    xhr.addEventListener("error", () => reject(new Error(`${file.name}: upload failed (network error)`)));
    xhr.addEventListener("abort", () => reject(new Error("upload cancelled")));

    $("upload-status").textContent = `Uploading ${label}… 0%`;
    xhr.send(form);
  });
}

function resetUploadUI() {
  state.uploadXHR = null;
  $("upload-progress").classList.add("hidden");
  setProgress(0);
  dropzone.classList.remove("hidden");
}

$("upload-cancel").addEventListener("click", () => {
  state.uploadCancelled = true;
  if (state.uploadXHR) state.uploadXHR.abort();
});

// ---------- Library ----------

async function refreshLibrary() {
  let videos, collections;
  try {
    [videos, collections] = await Promise.all([api("/api/videos"), api("/api/collections")]);
  } catch {
    return;
  }
  // Parts of a combined transcript are listed under it, not on their own.
  const grouped = new Set(collections.flatMap((c) => c.videoIds));
  const items = [
    ...videos.filter((v) => !grouped.has(v.id)).map((v) => ({ kind: "video", item: v, date: v.uploadedAt })),
    ...collections.map((c) => ({ kind: "collection", item: c, date: c.createdAt })),
  ].sort((a, b) => new Date(b.date) - new Date(a.date));

  state.libraryPending = items.some(({ kind, item }) => isPending(kind === "video" ? item.transcript : item));

  const list = $("video-list");
  list.textContent = "";
  $("library-empty").classList.toggle("hidden", items.length > 0);

  for (const { kind, item: v } of items) {
    const li = document.createElement("li");
    const info = document.createElement("div");
    info.className = "video-info";
    const name = document.createElement("span");
    name.className = "video-name";
    const meta = document.createElement("span");
    meta.className = "muted";
    info.append(name, meta);

    if (kind === "video") {
      name.textContent = v.filename;
      const subs = v.streams ? v.streams.length : 0;
      meta.textContent = `${formatBytes(v.size)} · ${subs} subtitle track${subs === 1 ? "" : "s"} · uploaded ${new Date(v.uploadedAt).toLocaleDateString()}`;
      const badge = transcriptBadge(v.transcript);
      if (badge) info.appendChild(badge);
      li.addEventListener("click", () => openDetail(v));
    } else {
      name.textContent = v.name;
      meta.textContent = `Combined transcript · ${v.parts.length} parts · ${formatBytes(v.size)} · created ${new Date(v.createdAt).toLocaleDateString()}`;
      const badge = transcriptBadge(v);
      if (badge) info.appendChild(badge);
      li.addEventListener("click", () => openCollection(v));
    }

    const expiry = document.createElement("span");
    expiry.className = "muted video-expiry";
    const days = daysUntil(v.expiresAt);
    expiry.textContent = days <= 1 ? "expires soon" : `expires in ${days} days`;

    li.append(info, expiry);
    list.appendChild(li);
  }
}

function isPending(t) {
  return !!t && (t.status === "queued" || t.status === "running");
}

// transcriptBadge summarises a video transcript or a collection's status.
function transcriptBadge(t) {
  if (!t) return null;
  const text = {
    queued: "transcript queued",
    running: `transcribing ${t.progress || 0}%`,
    done: "transcript ready",
    error: "transcript failed",
    missing: "parts missing",
  }[t.status];
  if (!text) return null;
  const el = makeBadge(text);
  el.classList.add("status-" + t.status);
  const wrap = document.createElement("div");
  wrap.className = "badges";
  wrap.appendChild(el);
  return wrap;
}

// ---------- Detail view ----------

function openDetail(video) {
  state.current = { kind: "video", item: video };
  hideResult();

  $("detail-title").textContent = video.filename;
  const bits = [formatBytes(video.size)];
  if (video.duration) bits.push(formatDuration(video.duration));
  if (video.format) bits.push(video.format);
  $("detail-meta").textContent = bits.join(" · ");

  $("parts-block").classList.add("hidden");
  $("streams-block").classList.remove("hidden");
  renderTranscriptBlock();

  const list = $("stream-list");
  list.textContent = "";
  const streams = video.streams || [];
  $("no-streams").classList.toggle("hidden", streams.length > 0);

  for (const s of streams) {
    const li = document.createElement("li");

    const info = document.createElement("div");
    info.className = "stream-info";
    const name = document.createElement("span");
    name.className = "stream-name";
    name.textContent = streamLabel(s);
    const badges = document.createElement("div");
    badges.className = "badges";
    const codec = document.createElement("span");
    codec.className = "badge";
    codec.textContent = s.codec;
    badges.appendChild(codec);
    if (s.language) {
      const lang = document.createElement("span");
      lang.className = "badge lang";
      lang.textContent = s.language;
      badges.appendChild(lang);
    }
    if (s.default) badges.appendChild(makeBadge("default"));
    if (s.forced) badges.appendChild(makeBadge("forced"));
    if (!s.extractable) {
      const warn = document.createElement("span");
      warn.className = "badge warn";
      warn.textContent = "bitmap — not extractable";
      badges.appendChild(warn);
    }
    info.append(name, badges);

    const btn = document.createElement("button");
    btn.textContent = "Extract";
    btn.disabled = !s.extractable;
    if (!s.extractable) btn.title = "Bitmap subtitles (e.g. PGS/VobSub) require OCR and cannot be converted to text";
    btn.addEventListener("click", () => extractStream(video, s, btn));

    li.append(info, btn);
    list.appendChild(li);
  }

  showDetailSection();
}

function openCollection(collection) {
  state.current = { kind: "collection", item: collection };
  hideResult();

  $("detail-title").textContent = collection.name;
  const bits = [`${collection.parts.length} parts`, formatBytes(collection.size)];
  if (collection.duration) bits.push(formatDuration(collection.duration));
  $("detail-meta").textContent = bits.join(" · ");

  $("streams-block").classList.add("hidden");
  $("parts-block").classList.remove("hidden");
  renderTranscriptBlock();
  showDetailSection();
}

function showDetailSection() {
  $("detail-section").classList.remove("hidden");
  $("detail-section").scrollIntoView({ behavior: "smooth", block: "start" });
}

function renderParts(collection) {
  const list = $("part-list");
  list.textContent = "";
  for (const p of collection.parts) {
    const li = document.createElement("li");
    const info = document.createElement("div");
    info.className = "stream-info";
    const name = document.createElement("span");
    name.className = "stream-name";
    name.textContent = p.missing ? "Deleted or expired video" : p.filename;
    info.appendChild(name);
    if (!p.missing) {
      const meta = document.createElement("span");
      meta.className = "muted";
      meta.textContent = [formatBytes(p.size), formatDuration(p.duration)].filter(Boolean).join(" · ");
      info.appendChild(meta);
    }
    const badge = transcriptBadge(p.missing ? { status: "missing" } : p.transcript || { status: "queued" });
    if (badge) info.appendChild(badge);
    if (p.transcript && p.transcript.status === "error") {
      const err = document.createElement("p");
      err.className = "error";
      err.textContent = p.transcript.error;
      info.appendChild(err);
    }
    li.appendChild(info);
    list.appendChild(li);
  }
}

// renderTranscriptBlock shows Whisper state and actions for the open item.
function renderTranscriptBlock() {
  const block = $("transcript-block");
  block.textContent = "";
  const cur = state.current;
  if (!cur) return;

  const isCollection = cur.kind === "collection";
  if (isCollection) renderParts(cur.item);

  const t = isCollection ? cur.item : cur.item.transcript;
  const line = document.createElement("p");
  line.className = "muted";
  block.appendChild(line);

  if (!whisperAvailable() && !(t && t.status === "done")) {
    line.textContent = "Speech-to-text is not available on this server.";
    return;
  }

  if (!t) {
    line.textContent = "Transcribe the spoken audio of this video into subtitles.";
    block.appendChild(actionButton("Generate transcript", () => requestTranscript(cur.item.id)));
    return;
  }

  switch (t.status) {
    case "queued":
    case "running": {
      if (isCollection) {
        const doneParts = cur.item.parts.filter((p) => p.transcript && p.transcript.status === "done").length;
        line.textContent = `Transcribing… ${t.progress}% (${doneParts} of ${cur.item.parts.length} parts done)`;
      } else if (t.status === "running") {
        line.textContent = `Transcribing… ${t.progress}%`;
      } else {
        line.textContent = "Queued for transcription.";
      }
      if (state.whisper && state.whisper.state !== "ready") {
        line.textContent += " Waiting for the Whisper model download to finish.";
      }
      const track = document.createElement("div");
      track.className = "progress-track";
      const fill = document.createElement("div");
      fill.className = "progress-fill";
      fill.style.width = (t.progress || 0) + "%";
      track.appendChild(fill);
      block.appendChild(track);
      break;
    }
    case "done":
      if (isCollection) {
        line.textContent = `Combined transcript of ${cur.item.parts.length} parts is ready.`;
        block.appendChild(actionButton("View transcript", (btn) => showCollectionTranscript(cur.item, btn)));
      } else {
        const lang = t.language && t.language !== "auto" ? `, language ${t.language.toUpperCase()}` : "";
        line.textContent = `Transcript ready (Whisper ${t.model}${lang}).`;
        block.appendChild(actionButton("View transcript", (btn) => showVideoTranscript(cur.item, btn)));
      }
      break;
    case "error":
      line.className = "error";
      if (isCollection) {
        line.textContent = "One or more parts failed to transcribe (see below).";
        const failed = cur.item.parts.filter((p) => p.transcript && p.transcript.status === "error");
        block.appendChild(actionButton("Retry failed parts", () => retryParts(failed.map((p) => p.id))));
      } else {
        line.textContent = `Transcription failed: ${t.error}`;
        block.appendChild(actionButton("Retry", () => requestTranscript(cur.item.id)));
      }
      break;
    case "missing":
      line.className = "error";
      line.textContent = "A part of this transcript was deleted or has expired, so it can no longer be combined.";
      break;
  }
}

function actionButton(text, onClick) {
  const b = document.createElement("button");
  b.textContent = text;
  b.addEventListener("click", () => onClick(b));
  return b;
}

async function requestTranscript(id) {
  try {
    const meta = await api(`/api/videos/${id}/transcribe`, { method: "POST" });
    if (state.current && state.current.kind === "video" && state.current.item.id === id) {
      state.current.item = meta;
      renderTranscriptBlock();
    }
    refreshLibrary();
  } catch (err) {
    alert(err.message);
  }
}

async function retryParts(ids) {
  try {
    for (const id of ids) await api(`/api/videos/${id}/transcribe`, { method: "POST" });
  } catch (err) {
    alert(err.message);
  }
  await refreshCurrent();
  refreshLibrary();
}

// refreshCurrent reloads the open item to update transcript progress.
async function refreshCurrent() {
  const cur = state.current;
  if (!cur) return;
  const path = cur.kind === "video" ? `/api/videos/${cur.item.id}` : `/api/collections/${cur.item.id}`;
  try {
    const fresh = await api(path);
    if (state.current !== cur) return; // something else was opened meanwhile
    cur.item = fresh;
    renderTranscriptBlock();
  } catch {}
}

function makeBadge(text) {
  const el = document.createElement("span");
  el.className = "badge";
  el.textContent = text;
  return el;
}

function closeDetail() {
  state.current = null;
  hideResult();
  $("detail-section").classList.add("hidden");
}

$("detail-close").addEventListener("click", closeDetail);

$("detail-delete").addEventListener("click", async () => {
  const cur = state.current;
  if (!cur) return;
  const question =
    cur.kind === "video"
      ? `Delete "${cur.item.filename}" and its subtitles and transcript?`
      : `Delete the combined transcript "${cur.item.name}" and all ${cur.item.parts.length} of its videos?`;
  if (!confirm(question)) return;
  const path = cur.kind === "video" ? `/api/videos/${cur.item.id}` : `/api/collections/${cur.item.id}`;
  try {
    await api(path, { method: "DELETE" });
    closeDetail();
    refreshLibrary();
  } catch (err) {
    alert(err.message);
  }
});

// ---------- Results ----------

function showResult(title, content, downloadUrl) {
  state.downloadUrl = downloadUrl;
  $("result-title").textContent = title;
  $("result-text").value = content;
  $("result-panel").classList.remove("hidden");
  $("result-panel").scrollIntoView({ behavior: "smooth", block: "nearest" });
}

function hideResult() {
  state.downloadUrl = null;
  $("result-panel").classList.add("hidden");
}

async function withBusy(btn, busyText, fn) {
  const text = btn.textContent;
  btn.disabled = true;
  btn.textContent = busyText;
  try {
    await fn();
  } finally {
    btn.disabled = false;
    btn.textContent = text;
  }
}

function extractStream(video, stream, btn) {
  return withBusy(btn, "Extracting…", async () => {
    try {
      const res = await api(`/api/videos/${video.id}/extract/${stream.index}`, { method: "POST" });
      showResult(`Extracted: ${streamLabel(stream)}`, res.content, `/api/videos/${video.id}/download/${stream.index}`);
    } catch (err) {
      alert(`Extraction failed: ${err.message}`);
    }
  });
}

function showVideoTranscript(video, btn) {
  return withBusy(btn, "Loading…", async () => {
    try {
      const res = await api(`/api/videos/${video.id}/transcript`);
      showResult("Whisper transcript", res.content, `/api/videos/${video.id}/transcript/download`);
    } catch (err) {
      alert(err.message);
    }
  });
}

function showCollectionTranscript(collection, btn) {
  return withBusy(btn, "Loading…", async () => {
    try {
      const res = await api(`/api/collections/${collection.id}/transcript`);
      showResult("Combined Whisper transcript", res.content, `/api/collections/${collection.id}/download`);
    } catch (err) {
      alert(err.message);
    }
  });
}

function download(format) {
  if (!state.downloadUrl) return;
  window.location.href = `${state.downloadUrl}?format=${format}`;
}

$("download-srt").addEventListener("click", () => download("srt"));
$("download-txt").addEventListener("click", () => download("txt"));

// ---------- Polling ----------

// While the model downloads or transcripts are in progress, refresh every few
// seconds so progress stays live without a manual reload.
setInterval(() => {
  if ($("app-view").classList.contains("hidden")) return;
  if (state.whisper && (state.whisper.state === "downloading" || state.whisper.state === "error")) {
    refreshWhisper().then(renderTranscriptBlock);
  }
  const cur = state.current;
  if (cur && isPending(cur.kind === "video" ? cur.item.transcript : cur.item)) refreshCurrent();
  if (state.libraryPending) refreshLibrary();
}, 3000);

// ---------- Init ----------

(async function init() {
  try {
    await api("/api/me");
    showApp();
  } catch {
    showLogin();
  }
})();
