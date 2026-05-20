package server

// graphHTML is the embedded vis.js graph visualization page.
const graphHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>RedisKG — Knowledge Graph Explorer</title>
<script src="https://unpkg.com/vis-network/standalone/umd/vis-network.min.js"></script>
<style>
  @import url('https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');

  :root {
    --bg-primary: #050510;
    --bg-secondary: #0a0a1a;
    --bg-card: rgba(15, 15, 35, 0.8);
    --bg-input: rgba(20, 20, 45, 0.9);
    --bg-hover: rgba(30, 30, 60, 0.6);
    --border: rgba(100, 120, 255, 0.12);
    --border-focus: rgba(100, 120, 255, 0.4);
    --text-primary: #e8eaff;
    --text-secondary: #8888aa;
    --text-dim: #555577;
    --accent: #6366f1;
    --accent-glow: rgba(99, 102, 241, 0.3);
    --accent-bright: #818cf8;
    --green: #34d399;
    --green-glow: rgba(52, 211, 153, 0.3);
    --red: #f87171;
    --red-glow: rgba(248, 113, 113, 0.2);
    --orange: #fb923c;
    --cyan: #22d3ee;
    --cyan-glow: rgba(34, 211, 238, 0.15);
    --radius: 10px;
    --radius-sm: 6px;
    --shadow: 0 4px 24px rgba(0, 0, 0, 0.4);
    /* The graph canvas stays on a dark surface in both themes so node labels
       remain readable without re-styling vis-network on every theme toggle. */
    --graph-bg: #050510;
  }

  :root[data-theme="light"] {
    --bg-primary: #f5f6fb;
    --bg-secondary: #ffffff;
    --bg-card: rgba(255, 255, 255, 0.95);
    --bg-input: #f1f2f7;
    --bg-hover: rgba(99, 102, 241, 0.08);
    --border: rgba(20, 20, 80, 0.10);
    --border-focus: rgba(99, 102, 241, 0.45);
    --text-primary: #131326;
    --text-secondary: #4a4a6a;
    --text-dim: #8a8aa8;
    --accent-glow: rgba(99, 102, 241, 0.18);
    --shadow: 0 4px 24px rgba(20, 20, 60, 0.08);
  }

  * { margin: 0; padding: 0; box-sizing: border-box; }

  body {
    font-family: 'Inter', -apple-system, BlinkMacSystemFont, sans-serif;
    background: var(--bg-primary);
    color: var(--text-primary);
    overflow: hidden;
  }

  /* Subtle grid background */
  body::before {
    content: '';
    position: fixed;
    inset: 0;
    background:
      radial-gradient(ellipse at 20% 50%, rgba(99, 102, 241, 0.06) 0%, transparent 60%),
      radial-gradient(ellipse at 80% 20%, rgba(34, 211, 238, 0.04) 0%, transparent 50%);
    pointer-events: none;
    z-index: 0;
  }
  :root[data-theme="light"] body::before {
    background:
      radial-gradient(ellipse at 20% 50%, rgba(99, 102, 241, 0.06) 0%, transparent 60%),
      radial-gradient(ellipse at 80% 20%, rgba(34, 211, 238, 0.05) 0%, transparent 50%);
  }

  #app {
    display: flex;
    height: 100vh;
    position: relative;
    z-index: 1;
  }

  /* ======================== SIDEBAR ======================== */
  #sidebar {
    width: 400px;
    min-width: 400px;
    background: var(--bg-secondary);
    border-right: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    backdrop-filter: blur(20px);
    z-index: 10;
  }

  .sidebar-header {
    padding: 20px 24px;
    display: flex;
    align-items: center;
    gap: 14px;
    border-bottom: 1px solid var(--border);
  }

  .theme-toggle {
    margin-left: auto;
    width: 34px; height: 34px;
    background: var(--bg-input);
    border: 1px solid var(--border);
    border-radius: 8px;
    display: flex; align-items: center; justify-content: center;
    color: var(--text-secondary);
    cursor: pointer;
    transition: all 0.15s;
  }
  .theme-toggle:hover {
    background: var(--bg-hover);
    color: var(--accent-bright);
    border-color: var(--border-focus);
  }
  .theme-toggle svg { width: 16px; height: 16px; }
  .theme-toggle .icon-sun { display: none; }
  :root[data-theme="light"] .theme-toggle .icon-moon { display: none; }
  :root[data-theme="light"] .theme-toggle .icon-sun { display: block; }

  .logo {
    width: 36px; height: 36px;
    background: linear-gradient(135deg, var(--accent), var(--cyan));
    border-radius: 10px;
    display: flex; align-items: center; justify-content: center;
    font-weight: 700; font-size: 14px; color: white;
    box-shadow: 0 0 20px var(--accent-glow);
    position: relative;
  }

  .logo::after {
    content: '';
    position: absolute; inset: -2px;
    border-radius: 12px;
    background: linear-gradient(135deg, var(--accent), var(--cyan));
    opacity: 0.3;
    filter: blur(8px);
    z-index: -1;
  }

  .header-text h1 {
    font-size: 18px;
    font-weight: 700;
    letter-spacing: -0.02em;
    background: linear-gradient(135deg, var(--text-primary), var(--accent-bright));
    -webkit-background-clip: text;
    -webkit-text-fill-color: transparent;
  }

  .header-text .subtitle {
    font-size: 11px;
    color: var(--text-dim);
    font-weight: 400;
    letter-spacing: 0.05em;
    text-transform: uppercase;
    margin-top: 2px;
  }

  /* Stats bar */
  #stats-bar {
    padding: 10px 24px;
    display: flex;
    gap: 16px;
    border-bottom: 1px solid var(--border);
    font-size: 12px;
    color: var(--text-secondary);
  }

  .stat-item {
    display: flex; align-items: center; gap: 6px;
  }

  .stat-dot {
    width: 6px; height: 6px;
    border-radius: 50%;
    background: var(--green);
    box-shadow: 0 0 6px var(--green-glow);
  }

  .stat-dot.edges { background: var(--cyan); box-shadow: 0 0 6px var(--cyan-glow); }

  /* Query Section */
  #query-section {
    padding: 16px 24px;
    border-bottom: 1px solid var(--border);
  }

  .input-group {
    position: relative;
  }

  .input-group textarea {
    width: 100%;
    padding: 12px 16px;
    padding-right: 52px;
    background: var(--bg-input);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    color: var(--text-primary);
    font-family: inherit;
    font-size: 14px;
    resize: none;
    transition: border-color 0.2s, box-shadow 0.2s;
    direction: auto;
  }

  .input-group textarea:focus {
    outline: none;
    border-color: var(--border-focus);
    box-shadow: 0 0 0 3px var(--accent-glow);
  }

  .input-group textarea::placeholder {
    color: var(--text-dim);
  }

  .send-btn {
    position: absolute;
    right: 6px; bottom: 6px;
    width: 38px; height: 38px;
    background: linear-gradient(135deg, var(--accent), #4f46e5);
    border: none;
    border-radius: 8px;
    color: white;
    cursor: pointer;
    display: flex; align-items: center; justify-content: center;
    transition: all 0.2s;
    box-shadow: 0 2px 12px var(--accent-glow);
  }

  .send-btn:hover {
    transform: translateY(-1px);
    box-shadow: 0 4px 20px var(--accent-glow);
  }

  .send-btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
    transform: none;
  }

  .send-btn svg { width: 18px; height: 18px; }

  /* ======================== TABS (vertical history list) ======================== */
  #tabs-bar {
    display: flex;
    flex-direction: column;
    overflow-y: auto;
    overflow-x: hidden;
    max-height: 30vh;
    background: rgba(5, 5, 16, 0.3);
    border-bottom: 1px solid var(--border);
    scrollbar-width: thin;
    scrollbar-color: var(--border) transparent;
  }
  #tabs-bar::-webkit-scrollbar { width: 4px; }
  #tabs-bar::-webkit-scrollbar-thumb { background: var(--border); border-radius: 2px; }

  .tab {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 16px;
    min-height: 42px;
    font-size: 12px;
    font-weight: 500;
    color: var(--text-dim);
    cursor: pointer;
    border-bottom: 1px solid var(--border);
    transition: all 0.15s;
    position: relative;
  }

  .tab:hover {
    background: var(--bg-hover);
    color: var(--text-secondary);
  }

  .tab.active {
    color: var(--accent-bright);
    background: var(--bg-card);
  }

  /* Vertical accent on the left (was a horizontal underline). */
  .tab.active::before {
    content: '';
    position: absolute;
    top: 0; bottom: 0; left: 0;
    width: 3px;
    background: linear-gradient(180deg, var(--accent), var(--accent-bright));
    border-radius: 0 2px 2px 0;
  }

  .tab-icon { font-size: 14px; flex-shrink: 0; margin-top: 1px; }
  .tab-label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    line-clamp: 2;
    line-height: 1.4;
    word-break: break-word;
    white-space: normal;
  }

  .tab-close {
    font-size: 10px;
    width: 20px; height: 20px;
    display: flex; align-items: center; justify-content: center;
    border-radius: 4px;
    color: var(--text-dim);
    cursor: pointer;
    flex-shrink: 0;
    transition: all 0.15s;
    opacity: 0;
  }
  .tab:hover .tab-close,
  .tab.active .tab-close { opacity: 1; }

  .tab-close:hover {
    background: var(--red-glow);
    color: var(--red);
  }

  .tab-graph {
    color: var(--green) !important;
    font-weight: 600;
    min-height: 38px;
    align-items: center;
  }
  .tab-graph .tab-label { -webkit-line-clamp: 1; line-clamp: 1; }
  .tab-graph .tab-icon { color: var(--green); }
  .tab-graph.active::before { background: linear-gradient(180deg, var(--green), #10b981); }

  /* ======================== ANSWER SECTION ======================== */
  #answer-section {
    flex: 1;
    overflow-y: auto;
    padding: 20px 24px;
    scrollbar-width: thin;
    scrollbar-color: var(--border) transparent;
  }

  #answer-section::-webkit-scrollbar { width: 4px; }
  #answer-section::-webkit-scrollbar-thumb { background: var(--border); border-radius: 2px; }

  .answer-card {
    background: var(--bg-card);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 16px 20px;
    animation: fadeIn 0.3s ease;
  }

  @keyframes fadeIn {
    from { opacity: 0; transform: translateY(8px); }
    to { opacity: 1; transform: translateY(0); }
  }

  .answer-question {
    font-size: 13px;
    font-weight: 600;
    color: var(--accent-bright);
    margin-bottom: 12px;
    display: flex;
    align-items: flex-start;
    gap: 8px;
    line-height: 1.5;
  }

  .answer-question .q-icon {
    background: var(--accent-glow);
    color: var(--accent-bright);
    width: 22px; height: 22px;
    border-radius: 6px;
    display: flex; align-items: center; justify-content: center;
    font-size: 11px;
    font-weight: 700;
    flex-shrink: 0;
    margin-top: 1px;
  }

  .answer-text {
    font-size: 14px;
    line-height: 1.7;
    color: var(--text-primary);
    white-space: pre-wrap;
    unicode-bidi: plaintext;
  }

  .answer-text ul, .answer-text ol {
    padding-left: 20px;
    margin: 8px 0;
  }

  .cypher-block {
    margin-top: 12px;
    padding: 10px 14px;
    background: rgba(5, 5, 16, 0.6);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    font-family: 'JetBrains Mono', monospace;
    font-size: 11px;
    color: var(--text-dim);
    word-break: break-all;
    max-height: 80px;
    overflow-y: auto;
  }

  .empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    height: 100%;
    color: var(--text-dim);
    text-align: center;
    gap: 12px;
  }

  .empty-state .empty-icon {
    font-size: 40px;
    opacity: 0.3;
  }

  .empty-state p {
    font-size: 13px;
    max-width: 240px;
    line-height: 1.5;
  }

  /* ======================== INGEST SECTION ======================== */
  #ingest-section {
    padding: 16px 24px;
    border-top: 1px solid var(--border);
    background: rgba(5, 5, 16, 0.3);
  }

  #ingest-input {
    width: 100%;
    padding: 10px 14px;
    background: var(--bg-input);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    font-family: inherit;
    font-size: 13px;
    resize: none;
    height: 52px;
    transition: border-color 0.2s, box-shadow 0.2s;
    direction: auto;
  }

  #ingest-input:focus {
    outline: none;
    border-color: var(--border-focus);
    box-shadow: 0 0 0 3px var(--accent-glow);
  }

  #ingest-input::placeholder { color: var(--text-dim); }

  .action-bar {
    display: flex;
    gap: 8px;
    margin-top: 10px;
  }

  .btn {
    padding: 8px 14px;
    border: none;
    border-radius: var(--radius-sm);
    font-family: inherit;
    font-size: 12px;
    font-weight: 600;
    cursor: pointer;
    transition: all 0.2s;
    display: flex;
    align-items: center;
    gap: 6px;
    white-space: nowrap;
  }

  .btn:disabled { opacity: 0.4; cursor: not-allowed; }

  .btn-primary {
    background: linear-gradient(135deg, var(--accent), #4f46e5);
    color: white;
    flex: 1;
    justify-content: center;
    box-shadow: 0 2px 12px var(--accent-glow);
  }

  .btn-primary:hover:not(:disabled) {
    box-shadow: 0 4px 20px var(--accent-glow);
    transform: translateY(-1px);
  }

  .btn-ghost {
    background: transparent;
    color: var(--text-secondary);
    border: 1px solid var(--border);
  }

  .btn-ghost:hover:not(:disabled) {
    background: var(--bg-hover);
    border-color: var(--border-focus);
  }

  .btn-danger {
    background: transparent;
    color: var(--red);
    border: 1px solid rgba(248, 113, 113, 0.2);
  }

  .btn-danger:hover:not(:disabled) {
    background: var(--red-glow);
    border-color: rgba(248, 113, 113, 0.4);
  }

  /* ======================== GRAPH AREA ======================== */
  #graph-wrapper {
    flex: 1;
    position: relative;
    background: var(--graph-bg);
  }

  #graph-container {
    width: 100%; height: 100%;
  }

  /* Focus bar */
  #focus-bar {
    position: absolute;
    top: 16px;
    left: 50%;
    transform: translateX(-50%);
    padding: 8px 20px;
    background: var(--bg-card);
    backdrop-filter: blur(12px);
    border: 1px solid var(--border-focus);
    border-radius: 100px;
    font-size: 13px;
    color: var(--text-primary);
    display: none;
    align-items: center;
    gap: 12px;
    box-shadow: var(--shadow), 0 0 20px var(--accent-glow);
    animation: fadeIn 0.3s ease;
  }

  #focus-bar .focus-name {
    font-weight: 600;
    color: var(--accent-bright);
  }

  #focus-bar .focus-close {
    width: 24px; height: 24px;
    display: flex; align-items: center; justify-content: center;
    border-radius: 50%;
    cursor: pointer;
    color: var(--text-dim);
    transition: all 0.15s;
    font-size: 12px;
  }

  #focus-bar .focus-close:hover {
    background: var(--red-glow);
    color: var(--red);
  }

  /* Load more */
  #load-more {
    position: absolute;
    bottom: 16px;
    left: 50%;
    transform: translateX(-50%);
    padding: 8px 24px;
    background: var(--bg-card);
    backdrop-filter: blur(12px);
    border: 1px solid var(--border);
    border-radius: 100px;
    font-family: inherit;
    font-size: 12px;
    font-weight: 500;
    color: var(--text-secondary);
    cursor: pointer;
    display: none;
    transition: all 0.2s;
    box-shadow: var(--shadow);
  }

  #load-more:hover {
    border-color: var(--border-focus);
    color: var(--text-primary);
  }

  /* Loading animation */
  .loading-dots {
    display: inline-flex;
    gap: 4px;
    align-items: center;
  }

  .loading-dots span {
    width: 6px; height: 6px;
    background: var(--accent-bright);
    border-radius: 50%;
    animation: bounce 1.2s infinite;
  }

  .loading-dots span:nth-child(2) { animation-delay: 0.15s; }
  .loading-dots span:nth-child(3) { animation-delay: 0.3s; }

  @keyframes bounce {
    0%, 80%, 100% { transform: scale(0.6); opacity: 0.4; }
    40% { transform: scale(1); opacity: 1; }
  }

  /* Toast */
  .toast {
    position: fixed;
    bottom: 24px;
    right: 24px;
    padding: 12px 20px;
    background: var(--bg-card);
    backdrop-filter: blur(12px);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    font-size: 13px;
    color: var(--text-primary);
    box-shadow: var(--shadow);
    z-index: 1000;
    animation: slideUp 0.3s ease, fadeOut 0.3s ease 2.5s forwards;
  }

  @keyframes slideUp {
    from { opacity: 0; transform: translateY(12px); }
    to { opacity: 1; transform: translateY(0); }
  }

  @keyframes fadeOut {
    from { opacity: 1; }
    to { opacity: 0; pointer-events: none; }
  }

  /* Scrollbar for sidebar */
  #sidebar ::-webkit-scrollbar { width: 4px; }
  #sidebar ::-webkit-scrollbar-thumb { background: var(--border); border-radius: 2px; }
</style>
</head>
<body>
<div id="app">
  <div id="sidebar">
    <div class="sidebar-header">
      <div class="logo">KG</div>
      <div class="header-text">
        <h1>RedisKG</h1>
        <div class="subtitle">Knowledge Graph Explorer</div>
      </div>
      <button class="theme-toggle" onclick="toggleTheme()" title="Toggle light/dark" aria-label="Toggle theme">
        <svg class="icon-moon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>
        <svg class="icon-sun" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"/></svg>
      </button>
    </div>

    <div id="stats-bar">
      <div class="stat-item">
        <div class="stat-dot"></div>
        <span id="stat-nodes">0 nodes</span>
      </div>
      <div class="stat-item">
        <div class="stat-dot edges"></div>
        <span id="stat-edges">0 edges</span>
      </div>
    </div>

    <div id="query-section">
      <div class="input-group">
        <textarea id="query-input" rows="2" placeholder="Ask about the knowledge graph..."></textarea>
        <button class="send-btn" id="query-btn" onclick="askQuestion()">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="22" y1="2" x2="11" y2="13"/><polygon points="22 2 15 22 11 13 2 9 22 2"/></svg>
        </button>
      </div>
    </div>

    <div id="tabs-bar">
      <div class="tab tab-graph active" onclick="switchToGraph()">
        <span class="tab-icon">&#9673;</span>
        <span class="tab-label">Graph</span>
      </div>
    </div>

    <div id="answer-section">
      <div id="answer-content">
        <div class="empty-state">
          <div class="empty-icon">&#8942;&#8942;&#8942;</div>
          <p>Ask a question to explore relationships in your knowledge graph</p>
        </div>
      </div>
    </div>

    <div id="ingest-section">
      <textarea id="ingest-input" placeholder="Paste text to ingest into the graph..."></textarea>
      <div class="action-bar">
        <button class="btn btn-primary" id="ingest-btn" onclick="ingestText()">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>
          Ingest
        </button>
        <button class="btn btn-ghost" onclick="copyGraph()">Copy</button>
        <button class="btn btn-ghost" onclick="downloadGraph()">Export</button>
        <button class="btn btn-danger" onclick="deleteGraph()">Clear</button>
      </div>
    </div>
  </div>

  <div id="graph-wrapper">
    <div id="graph-container"></div>
    <div id="focus-bar">
      <span>Viewing subgraph:</span>
      <span class="focus-name" id="focus-name"></span>
      <span class="focus-close" onclick="exitFocus()" title="Return to full graph">&#x2715;</span>
    </div>
    <button id="load-more" onclick="loadMore()">Load more nodes</button>
  </div>
</div>

<script>
// Theme: persisted in localStorage. Applied before any rendering so the
// flash-of-wrong-theme is avoided.
(function initTheme() {
  let t = 'dark';
  try { t = localStorage.getItem('rediskg-theme') || 'dark'; } catch (e) {}
  document.documentElement.dataset.theme = t;
})();

function toggleTheme() {
  const cur = document.documentElement.dataset.theme || 'dark';
  const next = cur === 'dark' ? 'light' : 'dark';
  document.documentElement.dataset.theme = next;
  try { localStorage.setItem('rediskg-theme', next); } catch (e) {}
}

let network = null;
let nodesDS = null;
let edgesDS = null;
let currentOffset = 0;
let pageLimit = 500;
let focusNode = null;

// Q&A tab history
let qaHistory = []; // [{id, question, answer, entity, html, graphData}]
let activeTab = 'graph';
let tabIdCounter = 0;

const palette = [
  '#6366f1','#34d399','#fb923c','#f87171','#a78bfa',
  '#22d3ee','#fbbf24','#4ade80','#c084fc','#f472b6'
];
let groupList = [];

function nodeColor(group) {
  if (group === 'focus') return { background: '#818cf8', border: '#6366f1', highlight: { background: '#a5b4fc', border: '#6366f1' } };
  let idx = groupList.indexOf(group);
  if (idx === -1 && group) { groupList.push(group); idx = groupList.length - 1; }
  const c = palette[idx % palette.length] || '#6366f1';
  return { background: c, border: 'rgba(0,0,0,0.3)', highlight: { background: c, border: '#fff' } };
}

function toVisNode(n) {
  return {
    id: n.id, label: n.label,
    color: nodeColor(n.group),
    font: { color: '#e8eaff', size: 12, face: 'Inter, sans-serif' },
    shape: 'dot',
    size: n.group === 'focus' ? 28 : 16,
    shadow: n.group === 'focus' ? { enabled: true, color: 'rgba(99,102,241,0.5)', size: 16 } : false
  };
}

function toVisEdge(e, i) {
  return {
    id: 'e' + i + '_' + e.from + '_' + e.to,
    from: e.from, to: e.to, label: e.label,
    font: { color: '#555577', size: 9, strokeWidth: 0, face: 'Inter, sans-serif' },
    color: { color: 'rgba(99,102,241,0.25)', highlight: '#818cf8', hover: '#818cf8' },
    arrows: { to: { enabled: true, scaleFactor: 0.6 } },
    width: Math.min(Math.max(e.weight || 1, 1), 4),
    smooth: { type: 'continuous', roundness: 0.2 }
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
      hover: true,
      tooltipDelay: 80,
      zoomView: true,
      dragView: true,
      navigationButtons: false,
      keyboard: false
    },
    nodes: {
      borderWidth: 2,
      borderWidthSelected: 3,
      chosen: true
    },
    edges: {
      selectionWidth: 2,
      hoverWidth: 1.5
    }
  });

  network.on('click', function(params) {
    if (params.nodes.length > 0) {
      document.getElementById('query-input').value = 'Tell me about ' + params.nodes[0];
    }
  });

  network.on('doubleClick', function(params) {
    if (params.nodes.length > 0) {
      focusOnNode(params.nodes[0]);
    }
  });
}

async function loadGraph(offset) {
  offset = offset || 0;
  try {
    const res = await fetch('/api/graph?limit=' + pageLimit + '&offset=' + offset);
    const data = await res.json();

    initNetwork();

    if (offset === 0) {
      nodesDS.clear();
      edgesDS.clear();
      groupList = [];
    }

    if (!data.nodes || data.nodes.length === 0) {
      if (offset === 0) {
        updateStats(0, 0);
      }
      document.getElementById('load-more').style.display = 'none';
      return;
    }

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
  document.getElementById('stat-nodes').textContent = nodes + ' nodes';
  document.getElementById('stat-edges').textContent = edges + ' edges';
}

function loadMore() {
  loadGraph(currentOffset);
}

// Focus on a node by fetching its subgraph from API
async function focusOnNode(name) {
  try {
    const res = await fetch('/api/graph?node=' + encodeURIComponent(name));
    const data = await res.json();
    renderSubgraph(data, name);
  } catch (err) {
    console.error('Focus error:', err);
  }
}

// Render a subgraph directly (used by both focusOnNode and query results)
function renderSubgraph(data, focusName) {
  if (!data.nodes || data.nodes.length === 0) return;

  initNetwork();
  nodesDS.clear();
  edgesDS.clear();
  groupList = [];

  data.nodes.forEach(n => { if (n.group && !groupList.includes(n.group)) groupList.push(n.group); });
  nodesDS.add(data.nodes.map(toVisNode));
  if (data.edges && data.edges.length > 0) {
    edgesDS.add(data.edges.map((e, i) => toVisEdge(e, i)));
  }

  focusNode = focusName;
  document.getElementById('focus-name').textContent = focusName;
  document.getElementById('focus-bar').style.display = 'flex';
  document.getElementById('load-more').style.display = 'none';
  updateStats(data.nodes.length, (data.edges || []).length);
}

function exitFocus() {
  focusNode = null;
  document.getElementById('focus-bar').style.display = 'none';
  switchToGraph();
}

// ======================== TAB MANAGEMENT ========================

function renderTabs() {
  const bar = document.getElementById('tabs-bar');
  let html = '<div class="tab tab-graph ' + (activeTab === 'graph' ? 'active' : '') + '" onclick="switchToGraph()">';
  html += '<span class="tab-icon">&#9673;</span><span class="tab-label">Graph</span></div>';

  for (const qa of qaHistory) {
    const isActive = activeTab === qa.id;
    // Full question text; CSS line-clamp wraps it to 2 lines with ellipsis.
    html += '<div class="tab ' + (isActive ? 'active' : '') + '" onclick="switchToTab(\'' + qa.id + '\')">';
    html += '<span class="tab-icon">&#9671;</span>';
    html += '<span class="tab-label" title="' + qa.question.replace(/"/g, '&quot;') + '">' + escapeHtml(qa.question) + '</span>';
    html += '<span class="tab-close" onclick="event.stopPropagation(); closeTab(\'' + qa.id + '\')">&#x2715;</span>';
    html += '</div>';
  }
  bar.innerHTML = html;
}

function switchToGraph() {
  activeTab = 'graph';
  renderTabs();
  document.getElementById('answer-content').innerHTML = '<div class="empty-state"><div class="empty-icon">&#8942;&#8942;&#8942;</div><p>Ask a question to explore relationships in your knowledge graph</p></div>';
  document.getElementById('focus-bar').style.display = 'none';
  focusNode = null;
  loadGraph();
}

function switchToTab(tabId) {
  const qa = qaHistory.find(q => q.id === tabId);
  if (!qa) return;

  activeTab = tabId;
  renderTabs();
  document.getElementById('answer-content').innerHTML = qa.html;

  // Render the stored subgraph directly (no extra API call!)
  if (qa.graphData && qa.graphData.nodes && qa.graphData.nodes.length > 0) {
    renderSubgraph(qa.graphData, qa.entity || qa.question);
  } else if (qa.entity) {
    // Fallback: fetch from API
    focusOnNode(qa.entity);
  }
}

function closeTab(tabId) {
  qaHistory = qaHistory.filter(q => q.id !== tabId);
  if (activeTab === tabId) {
    if (qaHistory.length > 0) {
      switchToTab(qaHistory[qaHistory.length - 1].id);
    } else {
      switchToGraph();
    }
  } else {
    renderTabs();
  }
}

// ======================== ASK QUESTION ========================

async function askQuestion() {
  const input = document.getElementById('query-input');
  const btn = document.getElementById('query-btn');
  const answerDiv = document.getElementById('answer-content');
  const question = input.value.trim();
  if (!question) return;

  // Create a new tab
  const tabId = 'q' + (++tabIdCounter);
  const qa = { id: tabId, question, answer: '', entity: null, html: '', graphData: null };
  qaHistory.push(qa);
  activeTab = tabId;
  renderTabs();

  btn.disabled = true;
  answerDiv.innerHTML = buildLoadingHtml(question);
  input.value = '';

  try {
    // UI always asks for the human-readable answer. Agent callers hitting
    // /api/query directly can omit the human flag (or set it to false) to
    // skip the extra LLM round-trip and just get graph + facts.
    const res = await fetch('/api/query', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ question, human: true })
    });
    const data = await res.json();

    let answerText = '';
    let entity = null;
    let graphData = null;

    if (data.error) {
      answerText = 'Error: ' + data.error;
    } else {
      answerText = data.answer || 'No answer';

      // Get entity name for focus label
      const entities = data.entities || [];
      if (entities.length > 0 && entities[0].name) {
        entity = entities[0].name;
      }

      // Get subgraph data returned directly from the query endpoint
      if (data.graph && data.graph.nodes && data.graph.nodes.length > 0) {
        graphData = data.graph;
      }
    }

    // Build answer HTML
    const html = buildAnswerHtml(question, answerText, data.cypher);

    // Store in tab
    qa.answer = answerText;
    qa.entity = entity;
    qa.html = html;
    qa.graphData = graphData;

    // Render if this tab is still active
    if (activeTab === tabId) {
      answerDiv.innerHTML = html;

      // Render the subgraph directly from query response
      if (graphData) {
        renderSubgraph(graphData, entity || question);
      } else if (entity) {
        // Fallback: fetch from API
        await focusOnNode(entity);
      }
    }
  } catch (err) {
    const errHtml = buildAnswerHtml(question, 'Error: ' + err.message);
    qa.html = errHtml;
    qa.answer = 'Error: ' + err.message;
    if (activeTab === tabId) {
      answerDiv.innerHTML = errHtml;
    }
  }
  btn.disabled = false;
}

function buildLoadingHtml(question) {
  return '<div class="answer-card">' +
    '<div class="answer-question"><span class="q-icon">Q</span>' + escapeHtml(question) + '</div>' +
    '<div class="answer-text"><div class="loading-dots"><span></span><span></span><span></span></div> Analyzing graph...</div>' +
    '</div>';
}

function buildAnswerHtml(question, answer, cypher) {
  let html = '<div class="answer-card">';
  html += '<div class="answer-question"><span class="q-icon">Q</span>' + escapeHtml(question) + '</div>';
  html += '<div class="answer-text">' + formatAnswer(answer) + '</div>';
  if (cypher) {
    html += '<div class="cypher-block">' + escapeHtml(cypher) + '</div>';
  }
  html += '</div>';
  return html;
}

function formatAnswer(text) {
  // Convert markdown-like bullets to HTML
  return escapeHtml(text)
    .replace(/\n- /g, '\n&bull; ')
    .replace(/\n/g, '<br>');
}

function escapeHtml(str) {
  if (!str) return '';
  return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

// ======================== OTHER ACTIONS ========================

async function ingestText() {
  const input = document.getElementById('ingest-input');
  const btn = document.getElementById('ingest-btn');
  const text = input.value.trim();
  if (!text) return;

  btn.disabled = true;
  btn.innerHTML = '<div class="loading-dots"><span></span><span></span><span></span></div> Ingesting...';

  try {
    const res = await fetch('/api/ingest', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text, source: 'web-ui' })
    });
    const data = await res.json();
    if (data.error) {
      showToast('Error: ' + data.error);
    } else {
      input.value = '';
      showToast('Ingested successfully! ' + (data.nodes || 0) + ' nodes, ' + (data.edges || 0) + ' edges');
      switchToGraph();
    }
  } catch (err) {
    showToast('Error: ' + err.message);
  }
  btn.disabled = false;
  btn.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg> Ingest';
}

async function copyGraph() {
  try {
    const res = await fetch('/api/export');
    const data = await res.json();
    const text = JSON.stringify(data, null, 2);
    await navigator.clipboard.writeText(text);
    showToast('Copied ' + (data.nodes ? data.nodes.length : 0) + ' nodes, ' + (data.edges ? data.edges.length : 0) + ' edges');
  } catch (err) {
    showToast('Error: ' + err.message);
  }
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
      document.getElementById('load-more').style.display = 'none';
      document.getElementById('focus-bar').style.display = 'none';
      qaHistory = [];
      activeTab = 'graph';
      renderTabs();
      document.getElementById('answer-content').innerHTML = '<div class="empty-state"><div class="empty-icon">&#8942;&#8942;&#8942;</div><p>Graph cleared. Ingest some documents to get started.</p></div>';
      showToast('Graph deleted');
    }
  } catch (err) {
    showToast('Error: ' + err.message);
  }
}

function showToast(msg) {
  const existing = document.querySelector('.toast');
  if (existing) existing.remove();
  const toast = document.createElement('div');
  toast.className = 'toast';
  toast.textContent = msg;
  document.body.appendChild(toast);
  setTimeout(() => toast.remove(), 3000);
}

// Keyboard shortcut
document.getElementById('query-input').addEventListener('keydown', function(e) {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); askQuestion(); }
});

// Boot
loadGraph();
</script>
</body>
</html>`
