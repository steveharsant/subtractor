"use strict";

const $ = (id) => document.getElementById(id);

const state = {
  currentVideo: null, // VideoMeta of the open detail view
  currentStream: null, // stream index of the shown extraction result
  uploadXHR: null,
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
    closeDetail();
    showLogin();
  }
});

// ---------- Upload ----------

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
  if (fileInput.files.length > 0) uploadFile(fileInput.files[0]);
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
dropzone.addEventListener("drop", (e) => {
  if (e.dataTransfer.files.length > 0) uploadFile(e.dataTransfer.files[0]);
});

function uploadFile(file) {
  const errEl = $("upload-error");
  errEl.classList.add("hidden");
  $("upload-progress").classList.remove("hidden");
  dropzone.classList.add("hidden");

  const form = new FormData();
  form.append("file", file);

  const xhr = new XMLHttpRequest();
  state.uploadXHR = xhr;
  xhr.open("POST", "/api/upload");
  xhr.withCredentials = true;

  xhr.upload.addEventListener("progress", (e) => {
    if (!e.lengthComputable) return;
    const pct = Math.round((e.loaded / e.total) * 100);
    $("progress-bar").style.width = pct + "%";
    $("upload-status").textContent =
      pct < 100
        ? `Uploading ${file.name}… ${pct}%`
        : `Analysing ${file.name} for subtitle tracks…`;
  });

  xhr.addEventListener("load", () => {
    resetUploadUI();
    let body = {};
    try {
      body = JSON.parse(xhr.responseText);
    } catch {}
    if (xhr.status === 401) return showLogin();
    if (xhr.status >= 400) {
      errEl.textContent = body.error || `upload failed (${xhr.status})`;
      errEl.classList.remove("hidden");
      return;
    }
    refreshLibrary();
    openDetail(body);
  });

  xhr.addEventListener("error", () => {
    resetUploadUI();
    errEl.textContent = "upload failed: network error";
    errEl.classList.remove("hidden");
  });
  xhr.addEventListener("abort", resetUploadUI);

  $("upload-status").textContent = `Uploading ${file.name}… 0%`;
  $("progress-bar").style.width = "0%";
  xhr.send(form);
}

function resetUploadUI() {
  state.uploadXHR = null;
  $("upload-progress").classList.add("hidden");
  $("progress-bar").style.width = "0%";
  dropzone.classList.remove("hidden");
}

$("upload-cancel").addEventListener("click", () => {
  if (state.uploadXHR) state.uploadXHR.abort();
});

// ---------- Library ----------

async function refreshLibrary() {
  let videos;
  try {
    videos = await api("/api/videos");
  } catch {
    return;
  }
  const list = $("video-list");
  list.textContent = "";
  $("library-empty").classList.toggle("hidden", videos.length > 0);

  for (const v of videos) {
    const li = document.createElement("li");

    const info = document.createElement("div");
    info.className = "video-info";
    const name = document.createElement("span");
    name.className = "video-name";
    name.textContent = v.filename;
    const meta = document.createElement("span");
    meta.className = "muted";
    const subs = v.streams ? v.streams.length : 0;
    meta.textContent = `${formatBytes(v.size)} · ${subs} subtitle track${subs === 1 ? "" : "s"} · uploaded ${new Date(v.uploadedAt).toLocaleDateString()}`;
    info.append(name, meta);

    const expiry = document.createElement("span");
    expiry.className = "muted video-expiry";
    const days = daysUntil(v.expiresAt);
    expiry.textContent = days <= 1 ? "expires soon" : `expires in ${days} days`;

    li.append(info, expiry);
    li.addEventListener("click", () => openDetail(v));
    list.appendChild(li);
  }
}

// ---------- Detail view ----------

function openDetail(video) {
  state.currentVideo = video;
  state.currentStream = null;

  $("detail-title").textContent = video.filename;
  const bits = [formatBytes(video.size)];
  if (video.duration) bits.push(formatDuration(video.duration));
  if (video.format) bits.push(video.format);
  $("detail-meta").textContent = bits.join(" · ");

  const list = $("stream-list");
  list.textContent = "";
  const streams = video.streams || [];
  $("no-streams").classList.toggle("hidden", streams.length > 0);
  $("result-panel").classList.add("hidden");

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

  $("detail-section").classList.remove("hidden");
  $("detail-section").scrollIntoView({ behavior: "smooth", block: "start" });
}

function makeBadge(text) {
  const el = document.createElement("span");
  el.className = "badge";
  el.textContent = text;
  return el;
}

function closeDetail() {
  state.currentVideo = null;
  state.currentStream = null;
  $("detail-section").classList.add("hidden");
}

$("detail-close").addEventListener("click", closeDetail);

$("detail-delete").addEventListener("click", async () => {
  const video = state.currentVideo;
  if (!video) return;
  if (!confirm(`Delete "${video.filename}" and its extracted subtitles?`)) return;
  try {
    await api(`/api/videos/${video.id}`, { method: "DELETE" });
    closeDetail();
    refreshLibrary();
  } catch (err) {
    alert(err.message);
  }
});

// ---------- Extraction ----------

async function extractStream(video, stream, btn) {
  btn.disabled = true;
  btn.textContent = "Extracting…";
  try {
    const res = await api(`/api/videos/${video.id}/extract/${stream.index}`, { method: "POST" });
    state.currentStream = stream.index;
    $("result-title").textContent = `Extracted: ${streamLabel(stream)}`;
    $("result-text").value = res.content;
    $("result-panel").classList.remove("hidden");
    $("result-panel").scrollIntoView({ behavior: "smooth", block: "nearest" });
  } catch (err) {
    alert(`Extraction failed: ${err.message}`);
  } finally {
    btn.disabled = false;
    btn.textContent = "Extract";
  }
}

function download(format) {
  if (!state.currentVideo || state.currentStream === null) return;
  window.location.href = `/api/videos/${state.currentVideo.id}/download/${state.currentStream}?format=${format}`;
}

$("download-srt").addEventListener("click", () => download("srt"));
$("download-txt").addEventListener("click", () => download("txt"));

// ---------- Init ----------

(async function init() {
  try {
    await api("/api/me");
    showApp();
  } catch {
    showLogin();
  }
})();
