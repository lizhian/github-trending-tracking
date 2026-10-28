import catalog from './data.js';

const repos = catalog.filter(row => !row[0].startsWith('community-labs/')).map((row, index) => ({ id: row[0], language: row[1], tagline: row[2], description: row[3], topics: row[4], stars: row[5], gains: row[6], age: row[7], index }));
const $ = selector => document.querySelector(selector);
const number = value => new Intl.NumberFormat('en-US').format(value);
const signed = value => value === null ? '积累中' : `${value < 0 ? '−' : '+'}${number(Math.abs(value))}`;
const esc = value => String(value).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const windows = [24, 72, 168, 720];
const windowLabels = ['24 小时', '3 天', '7 天', '30 天'];
const params = new URLSearchParams(location.search);
const state = { view: ['all', 'hot', 'new'].includes(params.get('view')) ? params.get('view') : 'all', q: params.get('q') || '', language: params.get('language') || '', topic: params.get('topic') || '', sort: ['name','stars','0','2'].includes(params.get('sort')) ? params.get('sort') : '0', direction: params.get('direction') === 'asc' ? 1 : -1, page: Math.max(1, Math.floor(Number(params.get('page'))) || 1), selected: params.get('project') || repos[0].id, period: [0,1,2,3].includes(Number(params.get('period'))) && params.has('period') ? Number(params.get('period')) : 3 };
const pageSize = 10;
const inView = repo => state.view === 'all' || (state.view === 'hot' && repo.gains[0] >= 300) || (state.view === 'new' && repo.age < 72);
function filtered() {
  const search = state.q.toLowerCase();
  return repos.filter(repo => inView(repo) && (!state.language || repo.language === state.language) && (!state.topic || repo.topics.includes(state.topic)) && (!search || `${repo.id} ${repo.tagline} ${repo.description}`.toLowerCase().includes(search))).sort((a, b) => {
    if (state.sort === 'name') return a.id.localeCompare(b.id) * state.direction;
    const av = state.sort === 'stars' ? a.stars : a.gains[Number(state.sort)];
    const bv = state.sort === 'stars' ? b.stars : b.gains[Number(state.sort)];
    if (av === null || bv === null) return av === bv ? 0 : av === null ? 1 : -1;
    return (av - bv) * state.direction || a.id.localeCompare(b.id);
  });
}
function save() {
  const p = new URLSearchParams();
  for (const key of ['view','q','language','topic','sort','page','period']) if (state[key] !== '' && state[key] !== 'all') p.set(key, state[key]);
  p.set('direction', state.direction === 1 ? 'asc' : 'desc');
  p.set('project', state.selected);
  history.replaceState(null, '', `?${p}`);
}
function gainCell(repo, period) {
  const gain = repo.gains[period];
  return gain === null ? '<span class="accumulating">积累中</span>' : `<span class="gain ${gain < 0 ? 'negative' : ''}">${signed(gain)}</span>${repo.age < windows[period] ? '<span class="partial">部分观测</span>' : ''}`;
}
function render() {
  const list = filtered();
  const pages = Math.max(1, Math.ceil(list.length / pageSize));
  state.page = Math.min(state.page, pages);
  const shown = list.slice((state.page - 1) * pageSize, state.page * pageSize);
  if (!list.some(repo => repo.id === state.selected)) state.selected = shown[0]?.id || '';
  $('#rows').innerHTML = shown.length ? shown.map((repo, i) => {
    const [owner, name] = repo.id.split('/');
    return `<tr class="${state.selected === repo.id ? 'selected' : ''}"><td class="rank ${i < 3 && state.page === 1 ? 'top' : ''}">${String((state.page - 1) * pageSize + i + 1).padStart(2,'0')}</td><td class="repo-cell"><button type="button" class="repo-button" data-project="${esc(repo.id)}" aria-pressed="${state.selected === repo.id}" aria-label="查看 ${esc(repo.id)} 趋势"><span class="owner">${esc(owner)}</span><span class="slash">/</span>${esc(name)}</button><p class="repo-description" title="${esc(repo.tagline)}">${esc(repo.tagline)}</p></td><td class="lang-col"><span class="lang">${esc(repo.language)}</span></td><td class="numeric">${number(repo.stars)}</td><td class="numeric">${gainCell(repo,0)}</td><td class="numeric week-col">${gainCell(repo,2)}</td></tr>`;
  }).join('') : '<tr><td class="empty-cell" colspan="6"><strong>没有匹配的项目</strong><p>试试其他关键词，或清除筛选重新探索。</p><button type="button" data-reset>清除全部筛选</button></td></tr>';
  $('#range-label').textContent = list.length ? `展示 ${(state.page - 1) * pageSize + 1}–${Math.min(state.page * pageSize, list.length)} 项，共 ${list.length} 项` : '0 个匹配项目';
  $('#page-label').textContent = `${state.page} / ${pages}`;
  $('#previous').disabled = state.page <= 1;
  $('#next').disabled = state.page >= pages;
  document.querySelectorAll('[data-view]').forEach(button => button.setAttribute('aria-pressed', String(state.view === button.dataset.view)));
  document.querySelectorAll('[data-sort]').forEach(button => {
    const selected = state.sort === button.dataset.sort;
    button.parentElement.setAttribute('aria-sort', selected ? state.direction === -1 ? 'descending' : 'ascending' : 'none');
    button.querySelector('span')?.remove();
    if (selected) button.insertAdjacentHTML('beforeend', `<span aria-hidden="true"> ${state.direction === -1 ? '↓' : '↑'}</span>`);
  });
  renderDetail();
  save();
}
function chart(repo) {
  const window = windows[state.period];
  const age = Math.min(repo.age, window);
  const delta = repo.gains[state.period];
  if (delta === null) return '<p class="detail-empty">正在积累快照，暂不能计算趋势。</p>';
  const candidates = windows.map((hours, index) => ({ hours: Math.min(repo.age, hours), value: repo.stars - repo.gains[index] })).filter((point, index) => point.hours <= age && repo.gains[index] !== null);
  const points = [...new Map(candidates.map(point => [point.hours, point])).values()].sort((a,b) => b.hours - a.hours);
  points.push({ hours: 0, value: repo.stars });
  const min = Math.min(...points.map(p => p.value)), max = Math.max(...points.map(p => p.value));
  const range = max - min || 1;
  const x = point => 4 + ((age - point.hours) / (age || 1)) * 228;
  const y = point => 106 - ((point.value - min) / range) * 77;
  return `<div class="chart-heading"><span>Star 总数 / 演示快照</span><span>纵轴局部范围</span></div><svg class="chart" viewBox="0 0 270 145" role="img" aria-label="${esc(repo.id)} 演示趋势，${signed(delta)} Star，连接各窗口边界快照"><g>${[29,67.5,106].map((v,i) => `<line class="chart-grid" x1="4" x2="232" y1="${v}" y2="${v}"/><text x="240" y="${v+3}">${((max - range*i/2)/1000).toFixed(1)}k</text>`).join('')}</g><polyline class="chart-line" points="${points.map(point => `${x(point)},${y(point)}`).join(' ')}"/>${points.map(point => `<circle class="chart-point" cx="${x(point)}" cy="${y(point)}" r="3"><title>${point.hours ? `${point.hours} 小时前` : '最新快照'}：${number(point.value)} Star（演示）</title></circle>`).join('')}<text x="4" y="132">${age < 24 ? `${age} 小时前` : `${Math.round(age/24*10)/10} 天前`}</text><text x="232" y="132" text-anchor="end">最新快照</text></svg>`;
}
function renderDetail() {
  const repo = repos.find(repo => repo.id === state.selected);
  if (!repo) { $('#detail').innerHTML = '<h2 class="detail-title">项目详情</h2><p class="detail-empty">从榜单中选择一个项目，查看它的 Star 变化。</p>'; return; }
  const [owner, name] = repo.id.split('/');
  const delta = repo.gains[state.period];
  $('#detail').innerHTML = `<p class="detail-owner">${esc(owner)} /</p><h2 class="detail-title">${esc(name)}</h2><p class="detail-description">${esc(repo.description)}</p><div class="topics" aria-label="仓库 Topics">${repo.topics.map(topic => `<button type="button" class="topic-button" data-topic="${esc(topic)}" aria-label="筛选 Topic ${esc(topic)}">${esc(topic)}</button>`).join('')}</div><div class="detail-stat"><span>Star 总数</span><div class="detail-star-line"><strong>${number(repo.stars)}</strong><span class="delta-badge ${delta < 0 ? 'negative' : ''}">${signed(delta)}</span></div></div>${chart(repo)}<div class="periods" aria-label="趋势窗口">${windowLabels.map((label,index) => `<button type="button" data-period="${index}" aria-pressed="${state.period === index}">${label}</button>`).join('')}</div><dl class="observation"><dt>编程语言</dt><dd>${esc(repo.language)}</dd><dt>观测跨度</dt><dd>${Math.min(repo.age, windows[state.period]) < 24 ? `${Math.min(repo.age, windows[state.period])} 小时` : `${Math.round(Math.min(repo.age, windows[state.period])/24*10)/10} 天`}</dd><dt>${windowLabels[state.period]} Star 净增量</dt><dd>${signed(delta)}</dd><dt>保护期</dt><dd>${repo.age < 24 ? '保护中' : '已结束'}</dd></dl><a class="repo-link" href="https://github.com/${esc(repo.id)}" target="_blank" rel="noreferrer"><span>在 GitHub 查看项目</span><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><path d="M14 3h7v7M21 3 10 14M10 3H3v18h18v-7"/></svg></a>`;
}
function reset() { Object.assign(state, { q: '', language: '', topic: '', view: 'all', page: 1 }); $('#search').value = ''; $('#language').value = ''; $('#topic').value = ''; render(); }
$('#total').textContent = number(repos.length);
$('#net').textContent = signed(repos.reduce((sum, repo) => sum + (repo.gains[0] || 0), 0));
$('#languages-total').textContent = `${new Set(repos.map(repo => repo.language)).size}`;
$('#all-count').textContent = repos.length;
$('#hot-count').textContent = repos.filter(repo => repo.gains[0] >= 300).length;
$('#new-count').textContent = repos.filter(repo => repo.age < 72).length;
$('#language').insertAdjacentHTML('beforeend', [...new Set(repos.map(repo => repo.language))].sort().map(value => `<option value="${esc(value)}">${esc(value)}</option>`).join(''));
$('#topic').insertAdjacentHTML('beforeend', [...new Set(repos.flatMap(repo => repo.topics))].sort().map(value => `<option value="${esc(value)}">${esc(value)}</option>`).join(''));
$('#search').value = state.q; $('#language').value = state.language; $('#topic').value = state.topic;
$('#search').addEventListener('input', event => { state.q = event.target.value; state.page = 1; render(); });
for (const key of ['language', 'topic']) $(`#${key}`).addEventListener('change', event => { state[key] = event.target.value; state.page = 1; render(); });
$('#reset').addEventListener('click', reset);
$('#previous').addEventListener('click', () => { state.page--; render(); });
$('#next').addEventListener('click', () => { state.page++; render(); });
document.addEventListener('click', event => {
  const project = event.target.closest('[data-project]');
  const view = event.target.closest('[data-view]');
  const sort = event.target.closest('[data-sort]');
  const period = event.target.closest('[data-period]');
  const topic = event.target.closest('[data-topic]');
  if (project) { state.selected = project.dataset.project; render(); document.querySelectorAll('[data-project]').forEach(button => { if (button.dataset.project === state.selected) button.focus({preventScroll:true}); }); if (matchMedia('(max-width:760px)').matches) $('#inspector').scrollIntoView({ block:'start' }); }
  if (view) { state.view = view.dataset.view; state.page = 1; render(); }
  if (sort) { if (state.sort === sort.dataset.sort) state.direction *= -1; else { state.sort = sort.dataset.sort; state.direction = state.sort === 'name' ? 1 : -1; } state.page = 1; render(); }
  if (period) { state.period = Number(period.dataset.period); renderDetail(); save(); $(`[data-period="${state.period}"]`).focus({preventScroll:true}); }
  if (topic) { state.topic = topic.dataset.topic; state.page = 1; $('#topic').value = state.topic; render(); }
  if (event.target.closest('[data-reset]')) reset();
});
document.addEventListener('keydown', event => { if (event.key === '/' && !/input|select|textarea/i.test(document.activeElement.tagName)) { event.preventDefault(); $('#search').focus(); } });
render();

const controls = $('.list-controls');
const syncControlsHeight = () => $('.listing').style.setProperty('--list-controls-height', `${controls.getBoundingClientRect().height}px`);
new ResizeObserver(syncControlsHeight).observe(controls);
syncControlsHeight();
