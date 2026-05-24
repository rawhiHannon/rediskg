package server

// graphHTML is the embedded web UI — Neo4j Browser-inspired layout with
// a top command bar, collapsible sidebar, stacking result frames, and
// a full-screen graph canvas.
const graphHTML = `<!DOCTYPE html>
<html lang="en" data-theme="dark" style="background:#1a1b26">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>RedisKG</title>
<script src="https://unpkg.com/vis-network/standalone/umd/vis-network.min.js"></script>
<style>
@import url('https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700&family=Fira+Code:wght@400;500&display=swap');

:root {
  --bg: #1a1b26;
  --bg-surface: #1f2133;
  --bg-elevated: #252839;
  --bg-input: #2a2d3e;
  --bg-hover: #303348;
  --border: rgba(255,255,255,0.06);
  --border-active: rgba(99,102,241,0.5);
  --text: #c0caf5;
  --text-secondary: #737aa2;
  --text-dim: #545c7e;
  --accent: #7aa2f7;
  --accent-bright: #89b4fa;
  --green: #9ece6a;
  --green-dim: rgba(158,206,106,0.15);
  --red: #f7768e;
  --red-dim: rgba(247,118,142,0.12);
  --orange: #ff9e64;
  --cyan: #7dcfff;
  --purple: #bb9af7;
  --radius: 4px;
  --radius-lg: 8px;
  --font-mono: 'Fira Code', 'JetBrains Mono', monospace;
  --graph-bg: #1a1b26;
}

:root[data-theme="light"] {
  --bg: #f0f0f5;
  --bg-surface: #ffffff;
  --bg-elevated: #f8f8fc;
  --bg-input: #eeeef5;
  --bg-hover: #e4e4ed;
  --border: rgba(0,0,0,0.08);
  --border-active: rgba(99,102,241,0.5);
  --text: #1a1b2e;
  --text-secondary: #6b6f8a;
  --text-dim: #9498b5;
  --green-dim: rgba(74,167,93,0.12);
  --red-dim: rgba(220,60,80,0.08);
  --graph-bg: #f0f0f5;
}

* { margin: 0; padding: 0; box-sizing: border-box; }
body {
  font-family: 'Inter', -apple-system, BlinkMacSystemFont, sans-serif;
  background: var(--bg);
  color: var(--text);
  overflow: hidden;
  height: 100vh;
}

/* ==================== LAYOUT ==================== */
#app {
  display: flex;
  flex-direction: column;
  height: 100vh;
}

/* ==================== TOP BAR ==================== */
#topbar {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 0 16px;
  height: 48px;
  min-height: 48px;
  background: var(--bg-surface);
  border-bottom: 1px solid var(--border);
  z-index: 100;
}

.brand {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
  cursor: pointer;
}

.brand-icon {
  width: 28px; height: 28px;
  background: linear-gradient(135deg, var(--accent), var(--purple));
  border-radius: 6px;
  display: flex; align-items: center; justify-content: center;
  font-weight: 700; font-size: 11px; color: #fff;
}

.brand-name {
  font-weight: 700;
  font-size: 15px;
  letter-spacing: -0.02em;
}

/* Command editor */
#cmd-editor {
  flex: 1;
  display: flex;
  align-items: center;
  background: var(--bg-input);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 0 12px;
  height: 34px;
  transition: border-color 0.15s;
}
#cmd-editor:focus-within {
  border-color: var(--border-active);
}
#cmd-editor .prefix {
  color: var(--text-dim);
  font-family: var(--font-mono);
  font-size: 12px;
  margin-right: 8px;
  user-select: none;
}
#cmd-input {
  flex: 1;
  background: none;
  border: none;
  outline: none;
  color: var(--text);
  font-family: var(--font-mono);
  font-size: 13px;
  line-height: 34px;
}
#cmd-input::placeholder { color: var(--text-dim); }

#run-btn {
  flex-shrink: 0;
  width: 34px; height: 34px;
  background: var(--accent);
  border: none;
  border-radius: var(--radius);
  color: #fff;
  cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  transition: opacity 0.15s;
}
#run-btn:hover { opacity: 0.85; }
#run-btn:disabled { opacity: 0.4; cursor: not-allowed; }
#run-btn svg { width: 16px; height: 16px; }

.topbar-actions {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
}

.icon-btn {
  width: 32px; height: 32px;
  background: none;
  border: 1px solid transparent;
  border-radius: var(--radius);
  color: var(--text-secondary);
  cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  transition: all 0.1s;
}
.icon-btn:hover {
  background: var(--bg-hover);
  color: var(--text);
  border-color: var(--border);
}
.icon-btn svg { width: 16px; height: 16px; }
.icon-btn.active {
  background: var(--accent);
  color: #fff;
  border-color: var(--accent);
}
.icon-btn.active:hover { opacity: 0.85; }

/* ==================== MAIN AREA ==================== */
#main {
  flex: 1;
  display: flex;
  overflow: hidden;
}

/* ==================== SIDEBAR ==================== */
#sidebar {
  width: 280px;
  min-width: 280px;
  background: var(--bg-surface);
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  transition: width 0.2s, min-width 0.2s;
}
#sidebar.collapsed {
  width: 0;
  min-width: 0;
  border-right: none;
}

.sb-section {
  border-bottom: 1px solid var(--border);
}
.sb-header {
  display: flex;
  align-items: center;
  padding: 10px 14px;
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text-dim);
  cursor: pointer;
  user-select: none;
}
.sb-header:hover { color: var(--text-secondary); }
.sb-header .arrow {
  margin-right: 6px;
  font-size: 9px;
  transition: transform 0.15s;
}
.sb-header .arrow.open { transform: rotate(90deg); }

.sb-body {
  padding: 4px 14px 12px;
}
.sb-body.hidden { display: none; }

/* DB info */
.db-stats {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 6px;
}
.db-stat {
  background: var(--bg-elevated);
  border-radius: var(--radius);
  padding: 8px 10px;
}
.db-stat-val {
  font-size: 20px;
  font-weight: 700;
  color: var(--text);
  font-family: var(--font-mono);
}
.db-stat-label {
  font-size: 10px;
  color: var(--text-dim);
  text-transform: uppercase;
  letter-spacing: 0.04em;
  margin-top: 2px;
}
.db-stat-val.nodes { color: var(--green); }
.db-stat-val.edges { color: var(--cyan); }

/* Ingest panel */
.ingest-area textarea {
  width: 100%;
  height: 80px;
  background: var(--bg-input);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  color: var(--text);
  font-family: inherit;
  font-size: 12px;
  padding: 8px 10px;
  resize: vertical;
  direction: auto;
}
.ingest-area textarea:focus {
  outline: none;
  border-color: var(--border-active);
}
.ingest-area textarea::placeholder { color: var(--text-dim); }

.ingest-opts {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 6px;
  flex-wrap: wrap;
}
.ingest-opts label {
  font-size: 11px;
  color: var(--text-dim);
  display: flex;
  align-items: center;
  gap: 4px;
}
.ingest-opts select, .ingest-opts input[type="text"] {
  background: var(--bg-input);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 2px 6px;
  font-size: 11px;
  font-family: inherit;
}
.ingest-opts input[type="text"] {
  font-family: var(--font-mono);
  width: 160px;
}

.ingest-actions {
  display: flex;
  gap: 6px;
  margin-top: 8px;
}

.btn-sm {
  padding: 5px 10px;
  font-size: 11px;
  font-weight: 600;
  font-family: inherit;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  cursor: pointer;
  transition: all 0.1s;
  display: flex;
  align-items: center;
  gap: 4px;
}
.btn-sm:disabled { opacity: 0.4; cursor: not-allowed; }
.btn-sm.primary {
  background: var(--accent);
  color: #fff;
  border-color: var(--accent);
  flex: 1;
  justify-content: center;
}
.btn-sm.primary:hover:not(:disabled) { opacity: 0.85; }
.btn-sm.ghost {
  background: transparent;
  color: var(--text-secondary);
}
.btn-sm.ghost:hover:not(:disabled) {
  background: var(--bg-hover);
}
.btn-sm.danger {
  background: transparent;
  color: var(--red);
  border-color: var(--red-dim);
}
.btn-sm.danger:hover:not(:disabled) { background: var(--red-dim); }

/* History list */
.history-list {
  max-height: 200px;
  overflow-y: auto;
  scrollbar-width: thin;
  scrollbar-color: var(--border) transparent;
}
.history-list::-webkit-scrollbar { width: 3px; }
.history-list::-webkit-scrollbar-thumb { background: var(--border); border-radius: 2px; }

.history-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 0;
  font-size: 12px;
  color: var(--text-secondary);
  cursor: pointer;
  transition: color 0.1s;
}
.history-item:hover { color: var(--text); }
.history-item.active { color: var(--accent); font-weight: 500; }
.history-item .hi-icon {
  width: 6px; height: 6px;
  border-radius: 50%;
  background: var(--text-dim);
  flex-shrink: 0;
}
.history-item.active .hi-icon { background: var(--accent); }
.history-item .hi-text {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.history-item .hi-close {
  opacity: 0;
  font-size: 10px;
  color: var(--text-dim);
  cursor: pointer;
  padding: 2px;
}
.history-item:hover .hi-close { opacity: 1; }
.history-item .hi-close:hover { color: var(--red); }

/* ==================== CONTENT AREA ==================== */
#content {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  position: relative;
}

/* Graph canvas */
#graph-container {
  flex: 1;
  width: 100%;
  height: 100%;
  background: var(--graph-bg);
}

/* Focus bar */
#focus-bar {
  position: absolute;
  top: 10px;
  left: 50%;
  transform: translateX(-50%);
  background: var(--bg-elevated);
  border: 1px solid var(--border-active);
  border-radius: 20px;
  padding: 6px 16px;
  display: none;
  align-items: center;
  gap: 10px;
  font-size: 12px;
  z-index: 10;
  animation: fadeSlideDown 0.2s ease;
}
#focus-bar .focus-label { color: var(--text-dim); }
#focus-bar .focus-name { font-weight: 600; color: var(--accent); }
#focus-bar .focus-close {
  cursor: pointer;
  color: var(--text-dim);
  font-size: 11px;
  padding: 2px 4px;
  border-radius: var(--radius);
}
#focus-bar .focus-close:hover { color: var(--red); background: var(--red-dim); }

/* Load more pill */
#load-more {
  position: absolute;
  bottom: 10px;
  left: 50%;
  transform: translateX(-50%);
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: 20px;
  padding: 6px 18px;
  font-family: inherit;
  font-size: 11px;
  font-weight: 500;
  color: var(--text-secondary);
  cursor: pointer;
  display: none;
  z-index: 10;
}
#load-more:hover { border-color: var(--border-active); color: var(--text); }

/* ==================== RESULT FRAME ==================== */
#result-frame {
  position: absolute;
  bottom: 0;
  left: 0;
  right: 0;
  max-height: 50%;
  background: var(--bg-surface);
  border-top: 1px solid var(--border);
  display: none;
  flex-direction: column;
  z-index: 20;
  animation: slideUp 0.2s ease;
}

#result-frame.visible { display: flex; }

.frame-header {
  display: flex;
  align-items: center;
  padding: 8px 14px;
  border-bottom: 1px solid var(--border);
  gap: 10px;
  flex-shrink: 0;
}
.frame-type {
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  padding: 2px 8px;
  border-radius: 3px;
}
.frame-type.answer { background: var(--green-dim); color: var(--green); }
.frame-type.error { background: var(--red-dim); color: var(--red); }
.frame-type.ingested { background: rgba(125,207,255,0.12); color: var(--cyan); }

.frame-question {
  flex: 1;
  font-size: 12px;
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.frame-close {
  cursor: pointer;
  color: var(--text-dim);
  font-size: 14px;
  padding: 2px 6px;
  border-radius: var(--radius);
}
.frame-close:hover { color: var(--red); background: var(--red-dim); }

.frame-body {
  padding: 14px 18px;
  overflow-y: auto;
  font-size: 14px;
  line-height: 1.7;
  scrollbar-width: thin;
  scrollbar-color: var(--border) transparent;
}
.frame-body::-webkit-scrollbar { width: 4px; }
.frame-body::-webkit-scrollbar-thumb { background: var(--border); border-radius: 2px; }

.frame-body .answer-text {
  white-space: pre-wrap;
  unicode-bidi: plaintext;
}

.frame-body .cypher-block {
  margin-top: 10px;
  padding: 8px 12px;
  background: var(--bg-input);
  border-radius: var(--radius);
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--text-dim);
  word-break: break-all;
  max-height: 60px;
  overflow-y: auto;
}

/* ==================== ANIMATIONS ==================== */
@keyframes fadeSlideDown {
  from { opacity: 0; transform: translateX(-50%) translateY(-8px); }
  to { opacity: 1; transform: translateX(-50%) translateY(0); }
}
@keyframes slideUp {
  from { opacity: 0; transform: translateY(20px); }
  to { opacity: 1; transform: translateY(0); }
}

/* ==================== LOADING ==================== */
.spinner {
  display: inline-flex;
  gap: 3px;
  align-items: center;
}
.spinner span {
  width: 5px; height: 5px;
  background: var(--accent);
  border-radius: 50%;
  animation: pulse 1.2s infinite;
}
.spinner span:nth-child(2) { animation-delay: 0.15s; }
.spinner span:nth-child(3) { animation-delay: 0.3s; }
@keyframes pulse {
  0%, 80%, 100% { transform: scale(0.6); opacity: 0.4; }
  40% { transform: scale(1); opacity: 1; }
}

/* ==================== TOAST ==================== */
.toast {
  position: fixed;
  bottom: 16px;
  right: 16px;
  padding: 10px 16px;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  font-size: 12px;
  color: var(--text);
  z-index: 1000;
  animation: slideUp 0.2s ease, fadeOut 0.3s ease 2.7s forwards;
}
@keyframes fadeOut {
  from { opacity: 1; }
  to { opacity: 0; pointer-events: none; }
}

/* ==================== EMPTY STATE ==================== */
.empty-graph {
  position: absolute;
  top: 50%; left: 50%;
  transform: translate(-50%, -50%);
  text-align: center;
  color: var(--text-dim);
  z-index: 5;
  pointer-events: none;
}
.empty-graph .eg-icon { font-size: 48px; opacity: 0.2; margin-bottom: 12px; }
.empty-graph p { font-size: 13px; }

/* Scrollbar for sidebar */
#sidebar ::-webkit-scrollbar { width: 3px; }
#sidebar ::-webkit-scrollbar-thumb { background: var(--border); border-radius: 2px; }
</style>
</head>
<body>
<div id="app">
  <!-- TOP BAR -->
  <div id="topbar">
    <div class="brand" onclick="switchToGraph()" title="RedisKG Home">
      <div class="brand-icon">KG</div>
      <span class="brand-name">RedisKG</span>
    </div>

    <div id="cmd-editor">
      <span class="prefix">$</span>
      <input id="cmd-input" type="text" placeholder="Ask a question or type a Cypher query..." autocomplete="off" spellcheck="false" />
    </div>

    <button id="run-btn" onclick="runCommand()" title="Run (Enter)">
      <svg viewBox="0 0 24 24" fill="currentColor"><polygon points="5 3 19 12 5 21 5 3"/></svg>
    </button>

    <div class="topbar-actions">
      <button class="icon-btn" id="chat-toggle" onclick="toggleChatMode()" title="Toggle continuous chat mode">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>
      </button>
      <button class="icon-btn" onclick="toggleSidebar()" title="Toggle sidebar">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><rect x="3" y="3" width="18" height="18" rx="2"/><line x1="9" y1="3" x2="9" y2="21"/></svg>
      </button>
      <button class="icon-btn" onclick="toggleTheme()" title="Toggle theme">
        <svg class="icon-moon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>
        <svg class="icon-sun" style="display:none" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"/></svg>
      </button>
    </div>
  </div>

  <!-- MAIN -->
  <div id="main">
    <!-- SIDEBAR -->
    <div id="sidebar">
      <!-- DB Info -->
      <div class="sb-section">
        <div class="sb-header" onclick="toggleSection(this)">
          <span class="arrow open">&#9654;</span> Database
        </div>
        <div class="sb-body">
          <div class="db-stats">
            <div class="db-stat">
              <div class="db-stat-val nodes" id="stat-nodes">0</div>
              <div class="db-stat-label">Nodes</div>
            </div>
            <div class="db-stat">
              <div class="db-stat-val edges" id="stat-edges">0</div>
              <div class="db-stat-label">Edges</div>
            </div>
          </div>
        </div>
      </div>

      <!-- Ingest -->
      <div class="sb-section">
        <div class="sb-header" onclick="toggleSection(this)">
          <span class="arrow open">&#9654;</span> Ingest
        </div>
        <div class="sb-body">
          <div class="ingest-area">
            <textarea id="ingest-input" placeholder="Paste text or drop a file to ingest..."></textarea>
            <div class="ingest-opts">
              <label>
                Strategy
                <select id="extraction-strategy">
                  <option value="llm">LLM 2-pass</option>
                  <option value="hybrid">Hybrid NER+LLM</option>
                </select>
              </label>
              <label id="ner-url-label" style="display:none">
                NER URL
                <input id="ner-url" type="text" value="" placeholder="Built-in" />
              </label>
            </div>
            <div class="ingest-actions">
              <button class="btn-sm primary" id="ingest-btn" onclick="ingestText()">
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>
                Ingest
              </button>
              <button class="btn-sm ghost" onclick="copyGraph()" title="Copy graph JSON">Copy</button>
              <button class="btn-sm ghost" onclick="downloadGraph()" title="Export JSON">Export</button>
              <button class="btn-sm danger" onclick="deleteGraph()">Clear</button>
            </div>
          </div>
        </div>
      </div>

      <!-- History -->
      <div class="sb-section" style="flex:1;overflow:hidden;display:flex;flex-direction:column;">
        <div class="sb-header" onclick="toggleSection(this)">
          <span class="arrow open">&#9654;</span> History
        </div>
        <div class="sb-body" style="flex:1;overflow:hidden;">
          <div class="history-list" id="history-list"></div>
        </div>
      </div>
    </div>

    <!-- CONTENT -->
    <div id="content">
      <div id="graph-container"></div>

      <div id="empty-state" class="empty-graph">
        <div class="eg-icon">&#9678;</div>
        <p>Query or ingest data to explore your knowledge graph</p>
      </div>

      <div id="focus-bar">
        <span class="focus-label">Subgraph:</span>
        <span class="focus-name" id="focus-name"></span>
        <span class="focus-close" onclick="exitFocus()" title="Back to full graph">&#x2715;</span>
      </div>

      <button id="load-more" onclick="loadMore()">Load more</button>

      <!-- Result frame (slides up from bottom) -->
      <div id="result-frame">
        <div class="frame-header">
          <span class="frame-type answer" id="frame-type">ANSWER</span>
          <span class="frame-question" id="frame-question"></span>
          <span class="frame-close" onclick="closeFrame()">&#x2715;</span>
        </div>
        <div class="frame-body" id="frame-body"></div>
      </div>
    </div>
  </div>
</div>

<script>
// ==================== THEME ====================
(function() {
  let t = 'dark';
  try { t = localStorage.getItem('rediskg-theme') || 'dark'; } catch(e) {}
  document.documentElement.dataset.theme = t;
  document.documentElement.style.background = t === 'light' ? '#f0f0f5' : '#1a1b26';
  applyThemeIcons(t);
})();

function toggleTheme() {
  const cur = document.documentElement.dataset.theme || 'dark';
  const next = cur === 'dark' ? 'light' : 'dark';
  document.documentElement.dataset.theme = next;
  document.documentElement.style.background = next === 'light' ? '#f0f0f5' : '#1a1b26';
  try { localStorage.setItem('rediskg-theme', next); } catch(e) {}
  applyThemeIcons(next);
}

function applyThemeIcons(theme) {
  document.querySelectorAll('.icon-moon').forEach(el => el.style.display = theme === 'dark' ? 'block' : 'none');
  document.querySelectorAll('.icon-sun').forEach(el => el.style.display = theme === 'light' ? 'block' : 'none');
}

// ==================== SIDEBAR ====================
function toggleSidebar() {
  document.getElementById('sidebar').classList.toggle('collapsed');
}

function toggleSection(header) {
  const arrow = header.querySelector('.arrow');
  const body = header.nextElementSibling;
  arrow.classList.toggle('open');
  body.classList.toggle('hidden');
}

// ==================== GRAPH ====================
let network = null, nodesDS = null, edgesDS = null;
let currentOffset = 0, pageLimit = 500, focusNode = null;

const palette = [
  '#7aa2f7','#9ece6a','#ff9e64','#f7768e','#bb9af7',
  '#7dcfff','#e0af68','#73daca','#c0caf5','#f7768e'
];
let groupList = [];

function nodeColor(group) {
  if (group === 'focus') return { background: '#7aa2f7', border: '#5d7dd0', highlight: { background: '#89b4fa', border: '#7aa2f7' } };
  let idx = groupList.indexOf(group);
  if (idx === -1 && group) { groupList.push(group); idx = groupList.length - 1; }
  const c = palette[idx % palette.length] || '#7aa2f7';
  return { background: c, border: 'rgba(0,0,0,0.25)', highlight: { background: c, border: '#fff' } };
}

function toVisNode(n) {
  return {
    id: n.id, label: n.label,
    color: nodeColor(n.group),
    font: { color: '#c0caf5', size: 12, face: 'Inter, sans-serif' },
    shape: 'dot',
    size: n.group === 'focus' ? 26 : 14,
    shadow: n.group === 'focus' ? { enabled: true, color: 'rgba(122,162,247,0.4)', size: 12 } : false
  };
}

function toVisEdge(e, i) {
  return {
    id: 'e' + i + '_' + e.from + '_' + e.to,
    from: e.from, to: e.to, label: e.label,
    font: { color: '#545c7e', size: 9, strokeWidth: 0, face: 'Inter, sans-serif' },
    color: { color: 'rgba(122,162,247,0.2)', highlight: '#7aa2f7', hover: '#7aa2f7' },
    arrows: { to: { enabled: true, scaleFactor: 0.5 } },
    width: Math.min(Math.max(e.weight || 1, 1), 4),
    smooth: { type: 'continuous', roundness: 0.15 }
  };
}

function initNetwork() {
  if (network) return;
  nodesDS = new vis.DataSet();
  edgesDS = new vis.DataSet();
  const container = document.getElementById('graph-container');
  network = new vis.Network(container, { nodes: nodesDS, edges: edgesDS }, {
    physics: {
      forceAtlas2Based: {
        gravitationalConstant: -50,
        centralGravity: 0.005,
        springLength: 160,
        springConstant: 0.06,
        damping: 0.4
      },
      solver: 'forceAtlas2Based',
      stabilization: { iterations: 200, updateInterval: 25 }
    },
    interaction: {
      hover: true, tooltipDelay: 80,
      zoomView: true, dragView: true,
      navigationButtons: false, keyboard: false
    },
    nodes: { borderWidth: 2, borderWidthSelected: 3 },
    edges: { selectionWidth: 2, hoverWidth: 1.5 }
  });

  network.on('click', function(params) {
    if (params.nodes.length > 0) {
      document.getElementById('cmd-input').value = 'Tell me about ' + params.nodes[0];
    }
  });
  network.on('doubleClick', function(params) {
    if (params.nodes.length > 0) focusOnNode(params.nodes[0]);
  });
}

async function loadGraph(offset) {
  offset = offset || 0;
  try {
    const res = await fetch('/api/graph?limit=' + pageLimit + '&offset=' + offset);
    const data = await res.json();
    initNetwork();
    if (offset === 0) { nodesDS.clear(); edgesDS.clear(); groupList = []; }

    if (!data.nodes || data.nodes.length === 0) {
      if (offset === 0) { updateStats(0, 0); showEmptyState(true); }
      document.getElementById('load-more').style.display = 'none';
      return;
    }
    showEmptyState(false);
    data.nodes.forEach(n => { if (n.group && !groupList.includes(n.group)) groupList.push(n.group); });
    nodesDS.add(data.nodes.map(toVisNode));
    if (data.edges && data.edges.length > 0) {
      edgesDS.add(data.edges.map((e, i) => toVisEdge(e, offset + i)));
    }
    currentOffset = offset + data.nodes.length;
    updateStats(data.total || nodesDS.length, edgesDS.length);
    document.getElementById('load-more').style.display = data.hasMore ? 'block' : 'none';
    document.getElementById('focus-bar').style.display = 'none';
    focusNode = null;
  } catch (err) {
    showToast('Error loading graph: ' + err.message);
  }
}

function updateStats(nodes, edges) {
  document.getElementById('stat-nodes').textContent = nodes;
  document.getElementById('stat-edges').textContent = edges;
}

function showEmptyState(show) {
  document.getElementById('empty-state').style.display = show ? 'block' : 'none';
}

function loadMore() { loadGraph(currentOffset); }

async function focusOnNode(name) {
  try {
    const res = await fetch('/api/graph?node=' + encodeURIComponent(name));
    const data = await res.json();
    renderSubgraph(data, name);
  } catch (err) { console.error('Focus error:', err); }
}

function renderSubgraph(data, focusName) {
  if (!data.nodes || data.nodes.length === 0) return;
  initNetwork();
  nodesDS.clear(); edgesDS.clear(); groupList = [];
  showEmptyState(false);
  data.nodes.forEach(n => { if (n.group && !groupList.includes(n.group)) groupList.push(n.group); });
  nodesDS.add(data.nodes.map(toVisNode));
  if (data.edges && data.edges.length > 0) edgesDS.add(data.edges.map((e, i) => toVisEdge(e, i)));
  focusNode = focusName;
  document.getElementById('focus-name').textContent = focusName;
  document.getElementById('focus-bar').style.display = 'flex';
  document.getElementById('load-more').style.display = 'none';
  updateStats(data.nodes.length, (data.edges || []).length);
}

function exitFocus() {
  focusNode = null;
  document.getElementById('focus-bar').style.display = 'none';
  loadGraph();
}

function switchToGraph() {
  closeFrame();
  setActiveHistory(null);
  loadGraph();
}

// ==================== HISTORY ====================
let queryHistory = []; // [{id, question, answer, entity, graphData, cypher}]
let activeHistoryId = null;
let histIdCounter = 0;

function renderHistory() {
  const el = document.getElementById('history-list');
  if (queryHistory.length === 0) {
    el.innerHTML = '<div style="font-size:11px;color:var(--text-dim);padding:4px 0;">No queries yet</div>';
    return;
  }
  let html = '';
  for (let i = queryHistory.length - 1; i >= 0; i--) {
    const q = queryHistory[i];
    const active = activeHistoryId === q.id;
    html += '<div class="history-item' + (active ? ' active' : '') + '" onclick="showHistoryItem(\'' + q.id + '\')">';
    html += '<div class="hi-icon"></div>';
    html += '<div class="hi-text" title="' + escapeAttr(q.question) + '">' + escapeHtml(q.question) + '</div>';
    html += '<span class="hi-close" onclick="event.stopPropagation();removeHistory(\'' + q.id + '\')">&#x2715;</span>';
    html += '</div>';
  }
  el.innerHTML = html;
}

function setActiveHistory(id) {
  activeHistoryId = id;
  renderHistory();
}

function showHistoryItem(id) {
  const q = queryHistory.find(h => h.id === id);
  if (!q) return;
  setActiveHistory(id);
  showResultFrame(q.question, q.answer, q.cypher, q.error);
  if (q.graphData && q.graphData.nodes && q.graphData.nodes.length > 0) {
    renderSubgraph(q.graphData, q.entity || q.question);
  } else if (q.entity) {
    focusOnNode(q.entity);
  }
}

function removeHistory(id) {
  queryHistory = queryHistory.filter(h => h.id !== id);
  if (activeHistoryId === id) {
    activeHistoryId = null;
    closeFrame();
    loadGraph();
  }
  renderHistory();
}

// ==================== RESULT FRAME ====================
function showResultFrame(question, answer, cypher, isError) {
  const frame = document.getElementById('result-frame');
  const typeEl = document.getElementById('frame-type');
  const qEl = document.getElementById('frame-question');
  const bodyEl = document.getElementById('frame-body');

  typeEl.textContent = isError ? 'ERROR' : 'ANSWER';
  typeEl.className = 'frame-type ' + (isError ? 'error' : 'answer');
  qEl.textContent = question;

  let html = '<div class="answer-text">' + formatAnswer(answer) + '</div>';
  if (cypher) {
    html += '<div class="cypher-block">' + escapeHtml(cypher) + '</div>';
  }
  bodyEl.innerHTML = html;
  frame.classList.add('visible');
}

function showIngestFrame(msg) {
  const frame = document.getElementById('result-frame');
  document.getElementById('frame-type').textContent = 'INGESTED';
  document.getElementById('frame-type').className = 'frame-type ingested';
  document.getElementById('frame-question').textContent = '';
  document.getElementById('frame-body').innerHTML = '<div class="answer-text">' + escapeHtml(msg) + '</div>';
  frame.classList.add('visible');
}

function closeFrame() {
  document.getElementById('result-frame').classList.remove('visible');
}

// ==================== COMMAND BAR ====================
async function runCommand() {
  const input = document.getElementById('cmd-input');
  const btn = document.getElementById('run-btn');
  const question = input.value.trim();
  if (!question) return;

  if (chatMode) { return runChat(question); }

  const histId = 'h' + (++histIdCounter);
  const entry = { id: histId, question, answer: '', entity: null, graphData: null, cypher: '', error: false };
  queryHistory.push(entry);
  setActiveHistory(histId);

  btn.disabled = true;
  showResultFrame(question, '', null, false);
  document.getElementById('frame-body').innerHTML = '<div class="spinner"><span></span><span></span><span></span></div> Querying graph...';
  input.value = '';

  try {
    const res = await fetch('/api/query', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ question, human: true })
    });
    const data = await res.json();

    if (data.error) {
      entry.answer = data.error;
      entry.error = true;
    } else {
      entry.answer = data.answer || 'No answer';
      entry.cypher = data.cypher || '';
      const entities = data.entities || [];
      if (entities.length > 0 && entities[0].name) entry.entity = entities[0].name;
      if (data.graph && data.graph.nodes && data.graph.nodes.length > 0) entry.graphData = data.graph;
    }

    if (activeHistoryId === histId) {
      showResultFrame(entry.question, entry.answer, entry.cypher, entry.error);
      if (entry.graphData) {
        renderSubgraph(entry.graphData, entry.entity || entry.question);
      } else if (entry.entity) {
        await focusOnNode(entry.entity);
      }
    }
    renderHistory();
  } catch (err) {
    entry.answer = err.message;
    entry.error = true;
    if (activeHistoryId === histId) {
      showResultFrame(entry.question, entry.answer, null, true);
    }
    renderHistory();
  }
  btn.disabled = false;
}

// ==================== INGEST ====================
document.getElementById('extraction-strategy').addEventListener('change', function() {
  document.getElementById('ner-url-label').style.display = this.value === 'hybrid' ? 'flex' : 'none';
});

fetch('/api/settings').then(r => r.json()).then(s => {
  if (s.extraction_strategy) {
    const sel = document.getElementById('extraction-strategy');
    sel.value = s.extraction_strategy;
    sel.dispatchEvent(new Event('change'));
  }
  if (s.ner_service_url) document.getElementById('ner-url').value = s.ner_service_url;
}).catch(() => {});

async function ingestText() {
  const input = document.getElementById('ingest-input');
  const btn = document.getElementById('ingest-btn');
  const text = input.value.trim();
  if (!text) return;

  const strategy = document.getElementById('extraction-strategy').value;
  const nerUrl = document.getElementById('ner-url').value.trim();

  btn.disabled = true;
  btn.innerHTML = '<div class="spinner"><span></span><span></span><span></span></div> Ingesting...';

  try {
    const body = { text, source: 'web-ui' };
    if (strategy !== 'llm') {
      body.extraction_strategy = strategy;
      if (nerUrl) body.ner_service_url = nerUrl;
    }
    const res = await fetch('/api/ingest', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    });
    const data = await res.json();
    if (data.error) {
      showToast('Error: ' + data.error);
    } else {
      input.value = '';
      const msg = (data.nodes || 0) + ' nodes, ' + (data.edges || 0) + ' edges created';
      showToast('Ingested: ' + msg);
      showIngestFrame(msg);
      loadGraph();
    }
  } catch (err) {
    showToast('Error: ' + err.message);
  }
  btn.disabled = false;
  btn.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg> Ingest';
}

function downloadGraph() {
  const a = document.createElement('a');
  a.href = '/api/export?download=1';
  a.download = 'knowledge_graph.json';
  a.click();
}

async function deleteGraph() {
  if (!confirm('Delete the entire graph? This cannot be undone.')) return;
  try {
    const res = await fetch('/api/graph', { method: 'DELETE' });
    const data = await res.json();
    if (data.error) {
      showToast('Error: ' + data.error);
    } else {
      if (nodesDS) nodesDS.clear();
      if (edgesDS) edgesDS.clear();
      updateStats(0, 0);
      showEmptyState(true);
      document.getElementById('load-more').style.display = 'none';
      document.getElementById('focus-bar').style.display = 'none';
      queryHistory = [];
      activeHistoryId = null;
      renderHistory();
      closeFrame();
      showToast('Graph deleted');
    }
  } catch (err) {
    showToast('Error: ' + err.message);
  }
}

// ==================== UTILS ====================
function formatAnswer(text) {
  return escapeHtml(text).replace(/\n- /g, '\n&bull; ').replace(/\n/g, '<br>');
}
function escapeHtml(s) {
  if (!s) return '';
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}
function escapeAttr(s) {
  return escapeHtml(s).replace(/'/g, '&#39;');
}
function showToast(msg) {
  const existing = document.querySelector('.toast');
  if (existing) existing.remove();
  const t = document.createElement('div');
  t.className = 'toast';
  t.textContent = msg;
  document.body.appendChild(t);
  setTimeout(() => t.remove(), 3200);
}

// ==================== COPY GRAPH ====================
async function copyGraph() {
  try {
    const res = await fetch('/api/export');
    const data = await res.json();
    const text = JSON.stringify(data, null, 2);
    await navigator.clipboard.writeText(text);
    showToast('Graph JSON copied to clipboard');
  } catch (err) {
    showToast('Copy failed: ' + err.message);
  }
}

// ==================== CHAT MODE ====================
let chatMode = false;
let chatHistory = [];

function toggleChatMode() {
  chatMode = !chatMode;
  const btn = document.getElementById('chat-toggle');
  btn.classList.toggle('active', chatMode);
  if (chatMode) {
    chatHistory = [];
    showToast('Chat mode ON — conversation history maintained');
    document.getElementById('cmd-input').placeholder = 'Chat mode: ask follow-up questions...';
  } else {
    chatHistory = [];
    showToast('Chat mode OFF — single query mode');
    document.getElementById('cmd-input').placeholder = 'Ask a question or type a Cypher query...';
  }
}

async function runChat(question) {
  const histId = 'h' + (++histIdCounter);
  const entry = { id: histId, question, answer: '', entity: null, graphData: null, cypher: '', error: false };
  queryHistory.push(entry);
  setActiveHistory(histId);

  const btn = document.getElementById('run-btn');
  btn.disabled = true;
  showResultFrame(question, '', null, false);
  document.getElementById('frame-body').innerHTML = '<div class="spinner"><span></span><span></span><span></span></div> Chatting...';
  document.getElementById('cmd-input').value = '';

  try {
    const body = { question, history: chatHistory };
    const res = await fetch('/api/chat', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    });
    const data = await res.json();

    if (data.error) {
      entry.answer = data.error;
      entry.error = true;
    } else {
      entry.answer = data.answer || 'No answer';
      const entities = data.entities || [];
      if (entities.length > 0 && entities[0].name) entry.entity = entities[0].name;
      if (data.graph && data.graph.nodes && data.graph.nodes.length > 0) entry.graphData = data.graph;
      // Accumulate history
      chatHistory.push({ role: 'user', content: question });
      chatHistory.push({ role: 'assistant', content: entry.answer });
    }

    if (activeHistoryId === histId) {
      showResultFrame(entry.question, entry.answer, entry.cypher, entry.error);
      if (entry.graphData) {
        renderSubgraph(entry.graphData, entry.entity || entry.question);
      } else if (entry.entity) {
        await focusOnNode(entry.entity);
      }
    }
    renderHistory();
  } catch (err) {
    entry.answer = err.message;
    entry.error = true;
    if (activeHistoryId === histId) {
      showResultFrame(entry.question, entry.answer, null, true);
    }
    renderHistory();
  }
  btn.disabled = false;
}

// Keyboard
document.getElementById('cmd-input').addEventListener('keydown', function(e) {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); runCommand(); }
});

// Boot
renderHistory();
loadGraph();
</script>
</body>
</html>`
