"use strict";

// The panel. Plain DOM, no framework, no build step — see static.go.
//
// Everything it can see is decided by the backends: the gateway attaches the
// role to each proxied request and the services enforce it. Hiding a control
// here is only about not offering something that would be refused.

const $ = (sel) => document.querySelector(sel);

let me = null;
let timer = null;

// ---- transport -------------------------------------------------------------

async function api(path, opts = {}) {
  const res = await fetch(path, { credentials: "same-origin", ...opts });
  if (res.status === 401) {
    showSignIn();
    throw new Error("not signed in");
  }
  const text = await res.text();
  let body = null;
  try { body = text ? JSON.parse(text) : null; } catch { /* not json */ }
  if (!res.ok) {
    throw new Error((body && (body.error || body.detail)) || `${res.status} ${res.statusText}`);
  }
  return body;
}

// text() builds text nodes rather than assigning innerHTML anywhere in this
// file. Transcripts and track titles are other people's text; going through the
// DOM means a title containing markup is displayed, never executed.
const el = (tag, attrs = {}, ...children) => {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else if (v !== null && v !== undefined) node.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined || c === false) continue;
    node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return node;
};

const replace = (target, ...nodes) => { target.replaceChildren(...nodes.flat()); };

function fail(target, err) {
  replace(target, el("p", { class: "error" }, err.message || String(err)));
}

// ---- helpers ---------------------------------------------------------------

const secs = (n) => {
  if (n === null || n === undefined) return "—";
  if (n < 60) return `${n} с`;
  if (n < 3600) return `${Math.floor(n / 60)} мин`;
  return `${Math.floor(n / 3600)} ч ${Math.floor((n % 3600) / 60)} мин`;
};

const time = (iso) => (iso ? new Date(iso).toLocaleTimeString("ru-RU") : "—");

const outcomePill = (outcome) => {
  const cls = { delivered: "ok", ok: "ok", wake: "ok", nominated: "warn",
                false_alarm: "warn", empty: "warn", error: "bad" }[outcome] || "";
  return el("span", { class: `pill ${cls}` }, outcome || "—");
};

// ---- state -----------------------------------------------------------------

async function loadState() {
  const box = $("#guilds");
  try {
    const data = await api("/api/bot/state");
    const guilds = (data && data.guilds) || [];
    if (!guilds.length) {
      replace(box, el("p", { class: "muted" }, "Бот сейчас ни к чему не подключён."));
    } else {
      replace(box, guilds.map(guildCard));
    }
    $("#state-age").textContent = `обновлено в ${new Date().toLocaleTimeString("ru-RU")}`;
  } catch (err) {
    fail(box, err);
  }
}

function guildCard(g) {
  const rows = [];
  const p = g.player;

  if (g.unavailable) {
    // Reported distinctly from an idle player: "not responding" and "nothing
    // playing" look the same in a UI and mean opposite things.
    rows.push(el("dt", {}, "Плеер"), el("dd", {}, el("span", { class: "pill bad" }, "не отвечает")));
  } else if (p) {
    rows.push(
      el("dt", {}, "Играет"),
      el("dd", {}, p.now_playing ? p.now_playing.title : "—",
        p.paused ? el("span", { class: "pill warn" }, " пауза") : null),
      el("dt", {}, "Громкость"), el("dd", {}, `${p.volume} / 10`),
      el("dt", {}, "В очереди"), el("dd", {}, String(p.queue_len ?? 0)),
    );
  }

  const v = g.voice;
  if (v) {
    rows.push(
      el("dt", {}, "Тишина"), el("dd", {}, secs(v.idle_seconds)),
      el("dt", {}, "Канал пуст"),
      el("dd", {}, v.alone_seconds ? secs(v.alone_seconds) : "нет, люди на месте"),
      el("dt", {}, "Ждут команду"), el("dd", {}, String(v.armed_speakers ?? 0)),
    );
  }

  const queue = p && p.queue && p.queue.length
    ? el("ol", { class: "queue" }, p.queue.slice(0, 10).map((t) => el("li", {}, t.title || t.id)))
    : null;

  return el("div", { class: "card" },
    el("h3", {}, `Сервер ${g.guild_id}`),
    el("dl", { class: "kv" }, rows),
    queue,
  );
}

// ---- events ----------------------------------------------------------------

async function loadEvents() {
  const box = $("#events");
  const q = new URLSearchParams();
  const kind = $("#ev-kind").value;
  const since = $("#ev-since").value;
  const user = $("#ev-user").value.trim();
  if (kind) q.set("kind", kind);
  if (since) q.set("since", since);
  if (user) q.set("user_id", user);
  q.set("limit", "200");

  try {
    const data = await api(`/api/bot/events?${q}`);
    const events = (data && data.events) || [];

    const note = [];
    if (data.redacted) {
      note.push("Тексты скрыты: расшифровки речи видит только владелец.");
    }
    if (data.window && data.window.truncated) {
      note.push(`Показаны последние ${data.window.capacity} событий — более старые вытеснены.`);
    }
    $("#ev-note").textContent = note.join(" ");

    if (!events.length) {
      replace(box, el("p", { class: "muted" }, "Событий нет."));
      return;
    }
    replace(box, el("div", { class: "scroll" }, el("table", {},
      el("thead", {}, el("tr", {}, ["Время", "Тип", "Пользователь", "Длит.", "Исход", "Подробности"]
        .map((h) => el("th", {}, h)))),
      el("tbody", {}, events.map(eventRow)),
    )));
  } catch (err) {
    fail(box, err);
  }
}

function eventRow(e) {
  const details = [];
  if (e.gate) details.push(`vosk: ${e.gate}`);
  if (e.text) details.push(`stt: ${e.text}`);
  if (e.trigger) details.push(`trigger: ${e.trigger}`);
  if (e.latency_ms) details.push(`${e.latency_ms} мс`);
  if (e.action) details.push(`action: ${e.action}`);
  if (e.err) details.push(e.err);

  return el("tr", { class: e.outcome === "dropped" ? "dropped" : "" },
    el("td", { class: "nowrap" }, time(e.at)),
    el("td", { class: "mono" }, e.kind),
    el("td", { class: "mono" }, e.user_id || "—"),
    el("td", { class: "nowrap" }, e.speech_ms ? `${e.speech_ms} мс` : "—"),
    el("td", {}, outcomePill(e.outcome)),
    el("td", {}, details.join(" · ") || "—"),
  );
}

// ---- stats -----------------------------------------------------------------

async function loadStats() {
  const box = $("#stats");
  try {
    const [stt, agent] = await Promise.all([
      api("/api/bot/stats/stt"),
      api("/api/bot/stats/agent"),
    ]);

    const pct = (n) => `${Math.round(n * 100)}%`;
    replace(box,
      el("div", { class: "stats-grid" },
        stat("Вердиктов гейта", stt.gate.total,
          `отброшено ${stt.gate.dropped}, отправлено дальше ${stt.gate.nominated}`),
        stat("Команд доставлено", stt.command.delivered,
          `имя без команды ${stt.command.wake}, пусто ${stt.command.empty}`),
        stat("Ложных срабатываний", stt.command.false_alarm,
          `${pct(stt.false_alarm_rate)} от отправленных на точную модель`),
        stat("Вызовов AI", agent.total, `ошибок ${agent.errors}`),
        stat("Задержка AI", `${agent.latency.p50_ms} мс`,
          `p95 ${agent.latency.p95_ms} мс, макс ${agent.latency.max_ms} мс`),
      ),
      el("p", { class: "muted" },
        stt.window.truncated
          ? `Считано по последним ${stt.window.events} событиям — история длиннее буфера.`
          : `Считано по всем ${stt.window.events} событиям с ${time(stt.window.oldest)}.`),
    );
  } catch (err) {
    fail(box, err);
  }
}

const stat = (label, value, sub) =>
  el("div", { class: "card" },
    el("div", { class: "muted" }, label),
    el("div", { class: "big" }, String(value)),
    el("div", { class: "muted" }, sub));

// ---- AI sessions -----------------------------------------------------------

async function loadSessions() {
  const box = $("#sessions");
  try {
    const data = await api("/api/ai/sessions");
    const list = Array.isArray(data) ? data : (data.sessions || []);
    if (!list.length) {
      replace(box, el("p", { class: "muted" }, "Сессий нет."));
      return;
    }
    replace(box, list.map(sessionCard));
  } catch (err) {
    fail(box, err);
  }
}

function sessionCard(s) {
  const guild = s.guild_id || s.key || "—";
  const status = el("span", { class: "muted" }, "");

  const reset = el("button", {
    onclick: async () => {
      // The confirm text names the scope explicitly: this wipes the whole
      // server's context, not one channel, and the two are easy to confuse.
      if (!confirm(`Сбросить память AI для сервера ${guild}?\n\nЭто очистит контекст всей гильдии, а не одного канала.`)) return;
      status.textContent = "сбрасываю…";
      try {
        await api(`/api/ai/sessions/${encodeURIComponent(guild)}/memory`, { method: "DELETE" });
        status.textContent = "сброшено";
      } catch (err) {
        status.textContent = err.message;
        status.className = "error";
      }
    },
  }, "Сбросить память");

  return el("div", { class: "card" },
    el("h3", {}, `Сервер ${guild}`),
    el("dl", { class: "kv" },
      el("dt", {}, "Последняя активность"), el("dd", {}, s.last_active ? time(s.last_active) : "—"),
      el("dt", {}, "Размер памяти"), el("dd", {}, String(s.size ?? s.messages ?? "—")),
    ),
    el("div", { class: "toolbar" }, reset, status),
  );
}

// ---- views -----------------------------------------------------------------

const loaders = {
  state: loadState,
  events: loadEvents,
  stats: loadStats,
  sessions: loadSessions,
};

function show(view) {
  for (const s of document.querySelectorAll(".view")) s.classList.add("hidden");
  $(`#view-${view}`).classList.remove("hidden");
  for (const t of document.querySelectorAll(".tab")) {
    t.classList.toggle("active", t.dataset.view === view);
  }
  location.hash = view;
  loaders[view]();
  scheduleRefresh(view);
}

// Only the live state auto-refreshes. Events and stats are things you read, and
// having the table reshuffle underneath you mid-sentence is worse than stale.
function scheduleRefresh(view) {
  clearInterval(timer);
  if (view === "state" && $("#autorefresh").checked) {
    timer = setInterval(loadState, 5000);
  }
}

function showSignIn() {
  clearInterval(timer);
  $("#app").classList.add("hidden");
  $("#signin").classList.remove("hidden");
}

async function start() {
  try {
    me = await api("/api/me");
  } catch {
    showSignIn();
    return;
  }
  $("#signin").classList.add("hidden");
  $("#app").classList.remove("hidden");
  $("#whoami").textContent = `${me.username} · ${me.role}`;

  for (const t of document.querySelectorAll(".tab")) {
    t.addEventListener("click", () => show(t.dataset.view));
  }
  $("#ev-refresh").addEventListener("click", loadEvents);
  $("#ev-kind").addEventListener("change", loadEvents);
  $("#ev-since").addEventListener("change", loadEvents);
  $("#stats-refresh").addEventListener("click", loadStats);
  $("#sessions-refresh").addEventListener("click", loadSessions);
  $("#autorefresh").addEventListener("change", () => scheduleRefresh("state"));

  const initial = location.hash.replace("#", "");
  show(loaders[initial] ? initial : "state");
}

start();
