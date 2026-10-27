// THROWAWAY: three UI directions with deterministic demonstration data, no persistence.
const paths = {
  radar:'<circle cx="12" cy="12" r="8"/><circle cx="12" cy="12" r="3"/><path d="m12 12 7-7M12 4v2M4 12h2M12 18v2M18 12h2"/>',
  trend:'<path d="m3 17 6-6 4 4 8-10M15 5h6v6"/>',
  grid:'<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
  star:'<path d="m12 3 2.8 5.7 6.2.9-4.5 4.4 1.1 6.2-5.6-3-5.6 3 1.1-6.2L3 9.6l6.2-.9Z"/>',
  search:'<circle cx="10.5" cy="10.5" r="6.5"/><path d="m16 16 4.5 4.5"/>',
  down:'<path d="m7 10 5 5 5-5"/>',
  right:'<path d="m9 5 7 7-7 7"/>',
  left:'<path d="m15 5-7 7 7 7"/>',
  arrowdown:'<path d="M12 4v16m-5-5 5 5 5-5"/>',
  arrowup:'<path d="M12 20V4m-5 5 5-5 5 5"/>',
  sort:'<path d="m8 9 4-4 4 4m-8 6 4 4 4-4"/>',
  external:'<path d="M14 3h7v7m0-7L10 14M10 4H5a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2h13a2 2 0 0 0 2-2v-5"/>',
  moon:'<path d="M20.5 13.5A9 9 0 0 1 10.5 3a9 9 0 1 0 10 10.5Z"/>',
  sun:'<circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5"/>',
  info:'<circle cx="12" cy="12" r="9"/><path d="M12 11v6m0-10v.2"/>',
  clock:'<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  bolt:'<path d="m13 2-9 12h7l-1 8 10-13h-7Z"/>',
  shield:'<path d="m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6Z"/><path d="m8 12 3 3 5-6"/>',
  tag:'<path d="M3 3h8l10 10-8 8L3 11Z"/><circle cx="7" cy="7" r=".8"/>',
  refresh:'<path d="M20 7v5h-5M4 17v-5h5"/><path d="M6.1 6.1A8 8 0 0 1 20 12M4 12a8 8 0 0 0 13.9 5.9"/>',
  code:'<path d="m8 7-5 5 5 5m8-10 5 5-5 5m-3-14-2 18"/>',
};
const icon = (name, size=16) => `<svg width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${paths[name] || paths.grid}</svg>`;
const escape = value => String(value).replace(/[&<>"']/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]));
const number = value => new Intl.NumberFormat('en-US').format(Math.round(value));
const compact = value => Math.abs(value)>=1000 ? `${(value/1000).toFixed(1)}k` : number(value);
const signed = value => value === null ? '—' : `${value>=0?'+':'−'}${number(Math.abs(value))}`;
const labels = ['24 小时','3 天','7 天','30 天'];
const windows = [24,72,168,720];
const colors = { Python:'#3976ab', TypeScript:'#7e6bd3', JavaScript:'#b68c24', Rust:'#b57750', Go:'#3296aa', C:'#77849b', 'C++':'#c46088', CSS:'#9265c7', Java:'#c17e44', Shell:'#72994f' };
const palettes = [['#eaeafa','#6659bf'],['#e5f0eb','#356d59'],['#faede3','#a86a37'],['#e8edf8','#516dab'],['#f5e9ef','#a25e7c'],['#eeece6','#756e57']];
const catalog = [
  ['bytedance/deer-flow','Python','让复杂任务在多智能体协作中，一步步变得简单。','基于 LangGraph 的开源多智能体框架，将研究、代码与工具调用连接起来，帮助你完成从资料收集到结果交付的复杂任务。',['ai','agent','deep-research','langgraph'],48320,[1248,3624,7946,19280],720],
  ['anthropics/claude-code','TypeScript','在终端里与你一起工作的智能编程助手。','理解代码库、编辑代码、运行命令，把日常开发工作串联成自然的对话流程。',['ai','developer-tools','coding','cli'],89342,[986,2854,6392,16438],720],
  ['microsoft/markitdown','Python','把文档转换成适合大语言模型阅读的 Markdown。','支持多种文件格式的轻量转换工具，让文档提取与后续知识处理更简单。',['python','markdown','document-processing'],92418,[812,2231,5280,14312],720],
  ['unclecode/crawl4ai','Python','为大模型应用而设计的开源网页抓取工具。','从网页中提取结构化内容，构建可复用、灵活的知识采集流程。',['ai','web-scraping','automation'],61847,[647,1842,4291,10240],720],
  ['open-webui/open-webui','Python','一个可以自由定制的 AI 对话工作空间。','可扩展、自托管的 AI 界面，让模型、知识库和工作流聚合在同一个地方。',['llm','self-hosted','ai','rag'],125630,[524,1631,3490,9860],720],
  ['vercel/ai','TypeScript','用统一的工具集，把 AI 带进你的应用。','提供模型调用、流式响应与界面交互能力，简化 AI 应用开发。',['ai','typescript','developer-tools'],35210,[416,1182,2840,7016],720],
  ['astral-sh/uv','Rust','一个极快的 Python 包与项目管理工具。','用一套工具管理 Python 项目、依赖、环境与运行脚本。',['python','rust','package-manager'],78241,[382,1048,2460,5808],720],
  ['browser-use/browser-use','Python','让 AI 能够理解并操作网页。','连接智能体与浏览器，把网页中的实际操作纳入自动化工作流程。',['agent','browser','automation'],68291,[359,982,2208,7814],720],
  ['google-gemini/gemini-cli','TypeScript','把智能体能力带到你的命令行。','轻量的终端工具，连接代码、文件与日常任务，提供灵活的开发体验。',['cli','ai','developer-tools'],72620,[314,862,2017,5912],720],
  ['huggingface/smolagents','Python','用少量代码构建实用的智能体。','简洁、可组合的智能体工具集，让模型使用工具并执行真实任务。',['agent','python','ai'],28371,[288,790,1802,4820],720],
  ['HKUDS/nanobot','Python','轻量而完整的个人智能助手。','用更少的代码探索个人智能体的核心能力，连接工具与消息渠道。',['agent','ai','automation'],8742,[267,812,812,812],90],
  ['alibaba/page-agent','TypeScript','让网页可以通过自然语言来操作。','一个嵌入页面的轻量智能体，帮助完成表单、导航与多步骤操作。',['browser','agent','typescript'],2340,[214,214,214,214],19],
  ['oven-sh/bun','JavaScript','一个面向现代 JavaScript 的快速工具集。','集运行时、包管理、测试和打包于一体，为开发流程减少工具切换。',['javascript','runtime','developer-tools'],81263,[193,582,1296,4012],720],
  ['supabase/supabase','TypeScript','开源的应用后端基础设施。','基于 Postgres 的数据库、认证与实时能力，帮助构建完整的产品。',['database','postgres','self-hosted'],85294,[182,530,1237,3521],720],
  ['n8n-io/n8n','TypeScript','把工具连接成可以自动运行的工作流。','可视化的工作流自动化平台，连接常用应用、数据和 AI 能力。',['automation','workflow','self-hosted'],164381,[176,492,1190,3208],720],
  ['rustdesk/rustdesk','Rust','可自托管的远程桌面。','跨平台远程访问与控制，帮助你在自己的基础设施上连接设备。',['remote-desktop','rust','self-hosted'],98430,[162,478,1039,2680],720],
  ['excalidraw/excalidraw','TypeScript','用手绘风格把想法画出来。','一个简洁的虚拟白板，适合架构草图、协作讨论和快速表达。',['design','whiteboard','typescript'],103622,[148,421,972,2610],720],
  ['shadcn-ui/ui','TypeScript','构建你自己的组件库。','可访问、可组合的界面组件，让你的产品建立一致的设计语言。',['design','ui','developer-tools'],102631,[136,402,892,2406],720],
  ['ollama/ollama','Go','在自己的设备上运行语言模型。','简化模型的下载、运行与本地集成，搭建属于自己的 AI 环境。',['llm','ai','self-hosted'],159272,[124,382,861,2308],720],
  ['jqlang/jq','C','一个轻巧灵活的 JSON 命令行处理器。','用简洁的表达式查询、转换与组合 JSON 数据。',['cli','json','developer-tools'],32840,[-18,24,187,680],720],
  ['pocketbase/pocketbase','Go','一个文件就能启动的开源后端。','把数据库、认证与后台管理组合在轻量的部署体验里。',['database','go','self-hosted'],42368,[98,286,642,1789],720],
  ['zed-industries/zed','Rust','为高效协作而生的代码编辑器。','重新思考代码编辑、性能与多人协作之间的关系。',['editor','rust','developer-tools'],69423,[92,274,608,1642],720],
  ['modelcontextprotocol/servers','TypeScript','连接模型与真实工具的数据桥梁。','为工具和数据提供通用的连接方式，让智能体能够访问更多上下文。',['mcp','ai','developer-tools'],76129,[88,251,584,1530],720],
  ['chroma-core/chroma','Python','面向 AI 应用的开源检索数据库。','存储、嵌入和查询应用数据，为语义检索提供简洁的开发体验。',['database','rag','ai'],26351,[81,248,529,1483],720],
  ['firecrawl/firecrawl','TypeScript','把网站转成适合 AI 的数据。','为检索增强生成和智能应用提取可读、结构化的网页内容。',['web-scraping','ai','rag'],53501,[76,236,492,1368],720],
  ['microsoft/playwright','TypeScript','可靠地自动化现代浏览器。','跨浏览器测试与自动化工具，让网页行为能够稳定重现。',['browser','testing','developer-tools'],76400,[72,212,478,1320],720],
  ['fastapi/fastapi','Python','现代、快速的 Python Web 框架。','以类型提示构建接口，用简洁的代码表达请求与数据契约。',['python','web','developer-tools'],89260,[68,198,451,1208],720],
  ['sveltejs/svelte','JavaScript','用编译的方式构建交互界面。','通过贴近 Web 的语法构建响应式页面。',['ui','javascript','web'],84293,[62,186,418,1182],720],
  ['localsend/localsend','Java','在局域网里轻松分享文件。','跨平台、无需账户，让你的设备之间快速传输文件。',['self-hosted','productivity','network'],67510,[58,173,392,1056],720],
  ['langchain-ai/langgraph','Python','构建能够保持状态的智能体。','用明确的控制流程组织长时间运行的多步骤智能任务。',['agent','ai','workflow'],32206,[54,162,371,980],720],
  ['vitejs/vite','TypeScript','更轻快的前端开发体验。','提供快速的开发启动与现代的生产构建能力。',['typescript','web','developer-tools'],78342,[52,151,345,918],720],
  ['denoland/deno','JavaScript','现代 JavaScript 与 TypeScript 运行时。','用内置的工具链简化开发、测试与部署。',['runtime','javascript','developer-tools'],104283,[48,142,326,886],720],
  ['neovim/neovim','C','更现代、可扩展的 Vim 编辑体验。','保持高效编辑的核心，同时开放更强的扩展能力。',['editor','cli','developer-tools'],86309,[45,132,306,820],720],
  ['grafana/grafana','TypeScript','让你的数据变得可观察。','用仪表盘连接指标、日志和追踪信息。',['monitoring','typescript','self-hosted'],71400,[42,124,286,772],720],
  ['prometheus/prometheus','Go','可观测系统的时序数据基础。','采集和查询指标，理解系统随时间变化的行为。',['monitoring','go','database'],60219,[39,116,261,709],720],
  ['gethomepage/homepage','TypeScript','为你的自托管应用做一个主页。','把常用服务和状态整合在一个清晰的入口。',['self-hosted','productivity','ui'],24820,[36,108,243,648],720],
  ['immich-app/immich','TypeScript','把照片和记忆留在自己的服务器。','一个自托管的照片与视频管理方案。',['self-hosted','photos','productivity'],62439,[34,101,229,610],720],
  ['usememos/memos','Go','一个轻量的想法与知识记录空间。','用简单直接的方式捕捉片段，整理值得保留的信息。',['self-hosted','productivity','go'],42718,[32,94,211,580],720],
  ['gohugoio/hugo','Go','快速构建内容驱动的网站。','为博客、文档和内容站点提供灵活的静态生成流程。',['web','go','developer-tools'],83281,[30,88,196,532],720],
  ['tailwindlabs/tailwindcss','CSS','以工具类构建你的界面。','一套灵活的样式工具，帮助你直接表达产品设计。',['css','design','web'],92346,[29,85,188,510],720],
  ['nushell/nushell','Rust','把结构化数据带进你的 Shell。','用管道处理表格与记录，探索更现代的命令行体验。',['cli','rust','developer-tools'],37841,[27,79,179,486],720],
  ['helix-editor/helix','Rust','一个终端里的模态编辑器。','让选择、编辑与语法理解结合在轻量的终端工作流中。',['editor','rust','cli'],41520,[25,74,166,452],720],
  ['junegunn/fzf','Go','一个通用的命令行模糊搜索器。','在文件、历史和列表中快速找到想要的内容。',['cli','go','productivity'],71204,[24,68,155,421],720],
  ['sharkdp/bat','Rust','带语法高亮的文件查看工具。','为终端阅读源码和文本增加清晰的层次。',['cli','rust','developer-tools'],54817,[22,65,148,398],720],
  ['sharkdp/fd','Rust','快速且易用的文件搜索。','为常用文件查找任务提供清晰的默认体验。',['cli','rust','developer-tools'],39284,[20,59,134,362],720],
  ['BurntSushi/ripgrep','Rust','在代码里快速找到答案。','一个兼顾速度与实用默认值的文本搜索工具。',['cli','rust','developer-tools'],53291,[19,56,126,340],720],
  ['jesseduffield/lazygit','Go','更轻松地在终端使用 Git。','用直观的终端界面组织提交、分支和日常版本操作。',['cli','git','developer-tools'],74128,[18,52,118,320],720],
  ['mermaid-js/mermaid','JavaScript','用文字表达图表。','把流程、关系与结构变成可维护的可视化内容。',['design','documentation','javascript'],80327,[17,48,111,298],720],
  ['tldraw/tldraw','TypeScript','可扩展的无限画布。','为白板和可视化协作构建灵活的交互体验。',['design','ui','typescript'],40316,[16,46,104,281],720],
  ['d2l-ai/d2l-zh','Python','动手理解深度学习。','通过可运行的代码和清晰的解释学习模型背后的方法。',['ai','education','python'],78421,[15,44,98,264],720],
  ['karpathy/micrograd','Python','一个很小的自动微分引擎。','从简单实现出发，理解神经网络与反向传播。',['ai','education','python'],14328,[14,41,92,250],720],
  ['Streamlit/streamlit','Python','让数据脚本成为交互应用。','快速构建数据工具，把结果分享给需要它的人。',['python','ui','ai'],42173,[13,38,87,231],720],
  ['gradio-app/gradio','Python','为你的模型搭建交互入口。','用少量代码创建可交互的机器学习演示。',['python','ui','ai'],39168,[12,35,80,215],720],
  ['duckdb/duckdb','C++','在进程内分析数据。','轻量的分析型数据库，把 SQL 带到本地数据工作流程中。',['database','analytics','developer-tools'],31852,[11,33,73,199],720],
  ['ClickHouse/ClickHouse','C++','面向大规模数据的分析数据库。','使用列式存储与查询能力处理高吞吐分析任务。',['database','analytics','self-hosted'],43921,[10,29,67,182],720],
  ['apache/echarts','TypeScript','把数据变成丰富的图表。','提供灵活的交互式数据可视化能力。',['design','analytics','typescript'],61283,[9,26,59,158],720],
  ['TanStack/query','TypeScript','管理应用中的异步数据。','让获取、缓存与更新服务器状态变得更清晰。',['typescript','web','developer-tools'],48210,[8,23,53,141],720],
  ['motiondivision/motion','TypeScript','为界面添加自然的运动。','用简洁的表达方式构建交互和视觉过渡。',['design','ui','typescript'],29831,[7,20,47,126],720],
  ['ant-design/ant-design','TypeScript','为复杂产品建立一致的界面。','一套面向企业级界面的设计语言与组件能力。',['design','ui','typescript'],98241,[6,18,41,112],720],
  ['community-labs/next-wave','TypeScript','下一波新项目，正在积累第一段趋势。','用于预览新入库状态的虚构示例仓库。它只有一个快照，因此尚不能计算净增量。',['agent','typescript'],128,[null,null,null,null],0.3],
];
const repositories = catalog.map(([id,language,tagline,description,topics,stars,gains,age],index) => ({id,language,tagline,description,topics,stars,gains,age,index,avatar:palettes[index%palettes.length]}));
const state = { variant:['A','B','C'].includes(new URLSearchParams(location.search).get('variant'))?new URLSearchParams(location.search).get('variant'):'A', theme:'light', view:'all', search:'', language:'', topic:'', sort:'trend0', direction:-1, page:1, expanded:repositories[0].id, period:3 };
const app = document.getElementById('app');
const stamp = '2026.10.28 · 10:00 CST';

function brand() { return `<a class="brand" href="?variant=${state.variant}" aria-label="Trend Radar 首页"><span class="brand-mark">${icon('trend',22)}</span><span class="brand-name">trend<span>radar</span><span style="color:var(--accent)">.</span></span></a>`; }
function actions() { return `<div class="top-actions"><span class="demo-tag">原型 · 演示数据</span><button class="text-button" data-action="about" aria-label="数据说明">${icon('info',15)}<span class="desktop-text">数据说明</span></button><button class="icon-button" data-action="theme" aria-label="切换${state.theme==='light'?'深':'浅'}色模式" title="切换${state.theme==='light'?'深':'浅'}色模式">${icon(state.theme==='light'?'moon':'sun',16)}</button></div>`; }
function header(workspace=false) { return `<header class="topbar"><div class="container topbar-inner">${workspace?`<span>工作空间</span>${icon('right',13)}<strong style="color:var(--ink);font-weight:500">趋势探索</strong>`:`${brand()}<a class="nav-link" href="#results">${icon('radar',15)}趋势探索</a>`}${actions()}</div></header>`; }
function freshness() { return `<div class="freshness"><span class="status-dot"></span><span>演示快照 ${stamp}</span></div>`; }
function intro(type='A') { return `<section class="intro"><div><span class="eyebrow">${type==='C'?'THE OPEN SOURCE EDITION / 001':'A SIGNAL BEYOND THE STARS'}</span><h1>${type==='C'?'下一波，<em>值得关注。</em>':type==='B'?'趋势探索':'发现热度，<em>看见趋势。</em>'}</h1><p>${type==='C'?'从短暂的上榜，到持续的增长。让时间给出答案。':'持续观察 GitHub 热门项目，让每一份增长都有迹可循。'}</p></div>${type==='C'?'<div class="edition"><strong>28</strong><span>OCTOBER, 2026</span></div>':freshness()}</section>`; }
function spark(repo,period=0,big=false) {
  if (repo.gains[period]===null) return '';
  const w=100,h=32,gain=repo.gains[period];
  const points=Array.from({length:20},(_,i)=>{ const t=i/19; const p=Math.max(0,Math.min(1,t**(repo.index%3===0?1.7-period*.2:.85+period*.15)+Math.sin(i*(1.2+period*.17)+repo.index+period*.7)*.035*Math.sin(Math.PI*t))); return `${(t*w).toFixed(1)},${(gain<0?5+p*22:28-p*22).toFixed(1)}`; }).join(' ');
  return `<svg viewBox="0 0 ${w} ${h}" fill="none" aria-hidden="true" class="${big?'large-spark':''}"><polyline points="${points}" stroke="currentColor" stroke-width="${big?1.7:2.4}" stroke-linecap="round" stroke-linejoin="round" vector-effect="non-scaling-stroke"/></svg>`;
}
function metrics() { const total=repositories.reduce((s,r)=>s+(r.gains[0]||0),0); return `<section class="metrics" aria-label="演示数据概览"><div class="metric"><div class="metric-label">${icon('grid',14)}正在追踪</div><div class="metric-value">${repositories.length}<small>个项目</small></div><div class="metric-foot">来自 GitHub Trending</div><div class="metric-art">${spark(repositories[5],0,true)}</div></div><div class="metric"><div class="metric-label">${icon('trend',14)}24h Star 净增长</div><div class="metric-value">+${compact(total)}<small>stars</small></div><div class="metric-foot"><span class="positive">${repositories.filter(r=>(r.gains[0]||0)>0).length} 个项目</span> 增长中</div><div class="metric-art">${spark(repositories[0],0,true)}</div></div><div class="metric"><div class="metric-label">${icon('bolt',14)}新进入视野</div><div class="metric-value">${repositories.filter(r=>r.age<72).length}<small>个项目</small></div><div class="metric-foot">入库 24h 内不淘汰</div><div class="metric-art">${spark(repositories[3],0,true)}</div></div></section>`; }
function viewCount(view) { return repositories.filter(r=>view==='all'||(view==='hot'&&r.gains[0]>=300)||(view==='new'&&r.age<72)).length; }
function viewButtons() { return `<div class="segmented" aria-label="项目视图">${[['all','全部项目'],['hot','近期爆发'],['new','新上榜']].map(([key,label])=>`<button data-view="${key}" aria-pressed="${state.view===key}" class="${state.view===key?'active':''}">${label}<span>${viewCount(key)}</span></button>`).join('')}</div>`; }
function filterbar() { return `<div class="filterbar">${viewButtons()}<div class="filter-fields"><label class="search-box">${icon('search',15)}<span class="sr-only">搜索仓库名称或简介</span><input id="search" type="search" placeholder="搜索项目或简介…" value="${escape(state.search)}" autocomplete="off"><kbd>/</kbd></label><label class="select-wrap"><span class="sr-only">按编程语言筛选</span><select id="language"><option value="">全部语言</option>${Object.keys(colors).filter(l=>repositories.some(r=>r.language===l)).map(l=>`<option ${state.language===l?'selected':''}>${l}</option>`).join('')}</select>${icon('down',12)}</label><label class="select-wrap"><span class="sr-only">按仓库 Topics 筛选</span><select id="topic"><option value="">全部 Topics</option>${[...new Set(repositories.flatMap(r=>r.topics))].sort().map(t=>`<option ${state.topic===t?'selected':''}>${t}</option>`).join('')}</select>${icon('down',12)}</label></div></div>`; }
function filtered() { return repositories.filter(r=>(state.view==='all'||(state.view==='hot'&&r.gains[0]>=300)||(state.view==='new'&&r.age<72))&&(!state.language||r.language===state.language)&&(!state.topic||r.topics.includes(state.topic))&&(!state.search||`${r.id} ${r.tagline} ${r.description}`.toLowerCase().includes(state.search.toLowerCase()))).sort((a,b)=>{
  const field=r=>state.sort==='name'?r.id:state.sort==='stars'?r.stars:r.gains[Number(state.sort.slice(-1))];
  const av=field(a),bv=field(b);
  if(av===null||bv===null) return av===bv?a.id.localeCompare(b.id):av===null?1:-1;
  return (typeof av==='string'?av.localeCompare(bv):av-bv)*state.direction||a.id.localeCompare(b.id);
}); }
function activeFilters() { return state.search||state.language||state.topic||state.view!=='all'?`<div class="active-filters">当前筛选${state.search?`<span class="filter-chip">“${escape(state.search)}”</span>`:''}${state.language?`<span class="filter-chip">${state.language}</span>`:''}${state.topic?`<span class="filter-chip"># ${state.topic}</span>`:''}${state.view!=='all'?`<span class="filter-chip">${state.view==='hot'?'近期爆发':'新上榜'}</span>`:''}<button class="clear-filters" data-action="reset">清除全部 ×</button></div>`:''; }
function sortHeader(key,label) { const current=state.sort===key; return `<th scope="col" aria-sort="${current?(state.direction===-1?'descending':'ascending'):'none'}"><button class="sort-button ${current?'sorted':''}" data-sort="${key}">${label}${icon(current?(state.direction===-1?'arrowdown':'arrowup'):'sort',11)}</button></th>`; }
function trendCell(repo,period) { const gain=repo.gains[period]; if(gain===null)return '<span class="accumulating">积累中</span>'; return `<div class="trend ${gain<0?'negative':'positive'}"><span class="trend-number">${signed(gain)}</span>${spark(repo,period)}</div>${repo.age<windows[period]?`<span class="partial-note" title="仅使用实际观测记录，不补造历史">部分观测 · ${repo.age<24?`${Math.round(repo.age)}h`:`${(repo.age/24).toFixed(1)}天`}</span>`:''}`; }
function row(repo) { const [owner,name]=repo.id.split('/'); const expanded=state.expanded===repo.id; const age=repo.age<24?`${Math.round(repo.age)} 小时`:`${Math.floor(repo.age/24)} 天`;
  return `<tr class="repo-row ${expanded?'expanded':''}"><td><div class="repo-identity"><span class="repo-avatar" style="--avatar-bg:${repo.avatar[0]};--avatar-fg:${repo.avatar[1]}" aria-hidden="true">${name.slice(0,1).toUpperCase()}</span><div class="repo-info"><a class="repo-name" href="${repo.index===59?'#results':`https://github.com/${repo.id}`}" ${repo.index===59?'':'target="_blank" rel="noopener noreferrer"'}><span class="owner">${owner} / </span><span class="name">${name}</span></a><div class="repo-subline"><span class="language"><i style="--language-color:${colors[repo.language]}"></i>${repo.language}</span><span class="sub-divider"></span><span>追踪 ${age}</span>${repo.age<24?'<span class="new-badge">NEW</span>':''}</div></div><button class="expand-button" data-expand="${repo.id}" aria-label="${expanded?'收起':'展开'} ${repo.id} 的详情" aria-expanded="${expanded}" aria-controls="detail-${repo.index}">${icon('right',13)}</button></div></td><td><span class="star-number">${icon('star',12)}${number(repo.stars)}</span></td>${labels.map((label,i)=>`<td data-label="${label}">${trendCell(repo,i)}</td>`).join('')}</tr>${expanded?`<tr class="detail-row"><td colspan="6"><section class="details" id="detail-${repo.index}" aria-label="${repo.id} 的项目详情">${details(repo)}</section></td></tr>`:''}`;
}
function series(repo,period) { const hours=Math.min(repo.age,windows[period]); const gain=repo.gains[period]||0; const n=gain===0&&repo.age<1?1:Math.min(49,Math.ceil(hours)+1); return Array.from({length:n},(_,i)=>{ const t=n===1?1:i/(n-1);const normalized=t**(repo.index%3===0?1.65:1.15)+Math.sin(t*Math.PI*7+repo.index)*.016*Math.sin(t*Math.PI);return {value:Math.round(repo.stars-gain+gain*normalized),hours:hours*(1-t)};}); }
function chart(repo) { const period=state.period; const data=series(repo,period); const values=data.map(p=>p.value); const low=Math.min(...values); const high=Math.max(...values); const range=Math.max(high-low,10);const ymin=low-range*.12;const ymax=high+range*.12;const x=i=>12+i/Math.max(1,data.length-1)*481;const y=v=>145-(v-ymin)/(ymax-ymin)*125;const line=data.map((p,i)=>`${i?'L':'M'}${x(i).toFixed(1)},${y(p.value).toFixed(1)}`).join(' ');const heightHours=Math.min(repo.age,windows[period]); const points=data.map((p,i)=>`<circle cx="${x(i)}" cy="${y(p.value)}" r="5" fill="transparent"><title>${p.hours===0?'最新观测':`${p.hours<24?Math.round(p.hours)+' 小时':(p.hours/24).toFixed(1)+' 天'}前`}：${number(p.value)} Stars（演示数据）</title></circle>`).join('');
  return `<div class="chart-header"><div class="chart-title">Star 总数<strong>${number(repo.stars)}<small class="${(repo.gains[period]||0)<0?'negative':'positive'}">${repo.gains[period]===null?'积累中':`${signed(repo.gains[period])} / ${repo.age<windows[period]?'实际观测':labels[period]}`}</small></strong></div><div class="chart-tabs" aria-label="图表时间范围">${labels.map((label,i)=>`<button data-period="${i}" aria-pressed="${period===i}" class="${period===i?'active':''}">${label}</button>`).join('')}</div></div><svg class="chart-svg" role="img" aria-label="${repo.id} 在${heightHours.toFixed(1)}小时实际观测内，Star 从 ${number(data[0].value)} 变化至 ${number(repo.stars)}。演示数据。" viewBox="0 0 555 175"><defs><linearGradient id="area-${repo.index}" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="var(--accent)" stop-opacity=".12"/><stop offset="100%" stop-color="var(--accent)" stop-opacity="0"/></linearGradient></defs>${[0,1,2].map(i=>{const value=ymin+(ymax-ymin)*i/2;return `<line x1="12" x2="495" y1="${y(value)}" y2="${y(value)}" stroke="var(--chart-grid)" stroke-dasharray="3 4"/><text x="510" y="${y(value)+3}">${compact(value)}</text>`;}).join('')}<g style="color:var(--accent)"><path d="${line} L${x(data.length-1)},145 L12,145 Z" fill="url(#area-${repo.index})"/><path d="${line}" stroke="currentColor" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round"/><circle cx="${x(data.length-1)}" cy="${y(repo.stars)}" r="3.2" fill="var(--surface)" stroke="currentColor" stroke-width="2"/>${points}</g><text x="12" y="169">${heightHours<24?`${Math.round(heightHours)} 小时前`:`${(heightHours/24).toFixed(heightHours%24?1:0)} 天前`}</text><text x="250" y="169" text-anchor="middle">${heightHours<48?`${Math.round(heightHours/2)} 小时前`:`${(heightHours/48).toFixed(0)} 天前`}</text><text x="493" y="169" text-anchor="end">最新观测</text></svg><div class="chart-summary"><span>每小时快照 · 模拟曲线</span><span>${repo.age<windows[period]?'部分观测 · 不补造入库前历史':'完整周期观测'}</span></div>`;
}
function details(repo) { return `<div class="detail-grid"><div class="detail-description"><span class="eyebrow">ABOUT THIS REPOSITORY</span><h3>${repo.tagline}</h3><p>${repo.description}</p><a class="detail-link" href="https://github.com/${repo.id}" target="_blank" rel="noopener noreferrer">前往 GitHub 仓库 ${icon('external',12)}</a><div class="detail-meta"><div><span>主要语言</span><b class="language"><i style="--language-color:${colors[repo.language]}"></i>${repo.language}</b></div><div><span>实际追踪时长</span><b>${repo.age<24?`${repo.age} 小时`:`${(repo.age/24).toFixed(repo.age%24?1:0)} 天`}</b></div><div><span>最近快照</span><b>10:00 CST · 演示</b></div></div>${repo.age<24?`<span class="protection">${icon('shield',12)}新项目保护期内 · 不参与清理</span>`:''}</div><div class="detail-chart">${chart(repo)}</div></div><div class="topics"><span class="topics-label">${icon('tag',12)}Topics</span>${repo.topics.map(topic=>`<button class="topic" data-topic="${topic}" aria-label="筛选 ${topic} 主题">${topic}</button>`).join('')}<span class="topics-origin">仓库 Topics · 原型示意</span></div>`; }
function results() { const rows=filtered();const pages=Math.max(1,Math.ceil(rows.length/50));state.page=Math.min(state.page,pages);const visible=rows.slice((state.page-1)*50,state.page*50);return `${activeFilters()}<div class="results-card"><table class="repo-table"><caption class="sr-only">GitHub 趋势项目，所有数值均为演示数据。点击项目展开按钮查看简介、图表与 Topics。</caption><colgroup><col><col>${labels.map(()=>'<col class="trend-col">').join('')}</colgroup><thead><tr>${sortHeader('name','仓库名称')}${sortHeader('stars','Star')}${labels.map((label,i)=>sortHeader(`trend${i}`,label+'趋势')).join('')}</tr></thead><tbody>${visible.map(row).join('')}</tbody></table>${rows.length?'':`<div class="empty-state">${icon('search',30)}<h3>还没有找到匹配的项目</h3><p>试试其他关键词，或清除当前筛选条件。</p><button data-action="reset">清除筛选，查看全部项目</button></div>`}<div class="pagination"><span>${rows.length?`${(state.page-1)*50+1}–${Math.min(state.page*50,rows.length)} / ${rows.length} 个项目`:'0 个项目'}<span class="muted"> · 每页 50 个</span></span><div class="pages"><button data-page="${state.page-1}" ${state.page===1?'disabled':''} aria-label="上一页">${icon('left',12)}</button>${Array.from({length:pages},(_,i)=>`<button data-page="${i+1}" class="${state.page===i+1?'current':''}" ${state.page===i+1?'aria-current="page"':''}>${i+1}</button>`).join('')}<button data-page="${state.page+1}" ${state.page===pages?'disabled':''} aria-label="下一页">${icon('right',12)}</button></div></div></div>`; }
function resultsArea() { return `<section id="results" aria-labelledby="list-title"><div class="section-heading"><div class="section-title"><h2 id="list-title">${state.variant==='C'?'所有值得追踪的变化':'趋势项目'}</h2><span class="count-pill" id="project-count">${filtered().length}</span></div><span class="section-caption">${icon('clock',12)}小时快照，滚动观察</span></div>${filterbar()}<div id="results-content">${results()}</div></section>`; }
function footer() { return '<footer class="footer-note"><span>源于 GitHub Trending，关注增长的每一步。</span><span>演示数据 · 每 10 分钟发现 · 每小时采样 · 最多 30 天历史</span></footer>'; }
function VariantA() { return `<div class="classic">${header()}<main class="container page-main">${intro()}${metrics()}${resultsArea()}${footer()}</main></div>`; }
function sidebar() { return `<aside class="sidebar" aria-label="趋势探索筛选">${brand()}<span class="eyebrow">EXPLORE</span>${[['all','grid','全部项目'],['hot','bolt','近期爆发'],['new','radar','新上榜']].map(([key,glyph,label])=>`<button class="sidebar-view ${state.view===key?'active':''}" data-view="${key}" aria-pressed="${state.view===key}">${icon(glyph,16)}${label}<span>${viewCount(key)}</span></button>`).join('')}<span class="eyebrow">LANGUAGES</span>${['','Python','TypeScript','Rust','Go','JavaScript'].map(language=>`<button class="sidebar-language ${state.language===language?'active':''}" data-language="${language}" aria-pressed="${state.language===language}"><span class="language"><i style="--language-color:${colors[language]||'var(--muted)'}"></i>${language||'全部语言'}</span><span>${repositories.filter(r=>!language||r.language===language).length}</span></button>`).join('')}<div class="sidebar-footer"><span class="freshness"><span class="status-dot"></span>持续观察，发现新信号</span><p>10 分钟发现 · 每小时更新<br>新项目拥有 24 小时保护期</p><button class="text-button" data-action="about">数据与追踪规则 ${icon('external',11)}</button></div></aside>`; }
function VariantB() { return `<div class="workspace">${sidebar()}<div class="workspace-content">${header(true)}<main class="container page-main">${intro('B')}${metrics()}${resultsArea()}${footer()}</main></div></div>`; }
function spotlights() { return `<section class="spotlights" aria-label="编辑式热点速览">${[[0,'今日增长领跑','bolt'],[1,'持续获得关注','trend'],[11,'刚刚进入视野','radar']].map(([index,label,glyph])=>{const repo=repositories[index];return `<button class="spotlight" data-spotlight="${repo.id}" aria-label="查看 ${repo.id} 的详情"><span class="spotlight-label">${icon(glyph,12)}${label}</span><h3>${repo.id.split('/')[1]}</h3><p>${repo.tagline}</p><div class="spotlight-stat"><div><strong>${signed(repo.gains[0])}</strong><small>/ ${repo.age<24?'部分观测':'24 小时'}</small></div>${spark(repo,0,true)}</div></button>`;}).join('')}</section>`; }
function VariantC() { return `<div class="editorial">${header()}<main class="container page-main">${intro('C')}${spotlights()}${resultsArea()}${footer()}</main></div>`; }
function render() { document.documentElement.dataset.theme=state.theme;app.innerHTML=({A:VariantA,B:VariantB,C:VariantC})[state.variant]();renderSwitcher(); }
function renderSwitcher() { document.getElementById('prototype-controls').innerHTML=`<nav class="prototype-switcher" aria-label="原型设计方向切换"><button data-variant-step="-1" aria-label="上一个设计方向">${icon('left',14)}</button><div class="variant-label"><b>${state.variant} · ${{A:'精简表格',B:'探索工作台',C:'热点观察刊'}[state.variant]}</b><span>原型 ${['A','B','C'].indexOf(state.variant)+1}/3</span></div><button data-variant-step="1" aria-label="下一个设计方向">${icon('right',14)}</button><div class="variant-dots">${['A','B','C'].map(v=>`<button data-variant="${v}" class="${v===state.variant?'active':''}" aria-label="设计方向 ${v}" aria-pressed="${v===state.variant}">${v}</button>`).join('')}</div></nav>`; }
function updateResults() { document.getElementById('results-content').innerHTML=results();document.getElementById('project-count').textContent=filtered().length;document.getElementById('announcer').textContent=`找到 ${filtered().length} 个项目`; }
function switchVariant(value) { state.variant=value;const url=new URL(location.href);url.searchParams.set('variant',value);history.replaceState(null,'',url);render();window.scrollTo({top:0});document.getElementById('announcer').textContent=`设计方向 ${value}，${{A:'精简表格',B:'探索工作台',C:'热点观察刊'}[value]}。${filtered().length} 个项目，当前${state.theme==='light'?'浅':'深'}色模式。`; }
function reset() { Object.assign(state,{search:'',language:'',topic:'',view:'all',page:1});render(); }
document.addEventListener('click', event => {
  const button=event.target.closest('button');if(!button)return;
  const data=button.dataset;
  if(data.action==='about')document.getElementById('about-dialog').showModal();
  if(data.action==='theme'){state.theme=state.theme==='light'?'dark':'light';render();document.querySelector('[data-action=theme]').focus({preventScroll:true});}
  if(data.action==='reset')reset();
  if(data.view){state.view=data.view;state.page=1;render();document.querySelector(`[data-view=${state.view}]`).focus({preventScroll:true});}
  if(data.language!==undefined){state.language=data.language;state.page=1;render();}
  if(data.sort){state.direction=state.sort===data.sort?-state.direction:data.sort==='name'?1:-1;state.sort=data.sort;updateResults();}
  if(data.expand){const repo=repositories.find(r=>r.id===data.expand);state.expanded=state.expanded===repo.id?null:repo.id;updateResults();document.querySelector(`[data-expand="${CSS.escape(repo.id)}"]`)?.focus({preventScroll:true});}
  if(data.period!==undefined){state.period=Number(data.period);const id=state.expanded;const repo=repositories.find(r=>r.id===id);document.getElementById(`detail-${repo.index}`).innerHTML=details(repo);document.querySelector(`[data-period="${state.period}"]`)?.focus({preventScroll:true});}
  if(data.topic){state.topic=data.topic;state.page=1;render();document.getElementById('results').scrollIntoView();}
  if(data.page){state.page=Number(data.page);updateResults();document.getElementById('results').scrollIntoView();}
  if(data.spotlight){reset();state.expanded=data.spotlight;updateResults();document.getElementById(`detail-${repositories.find(r=>r.id===data.spotlight).index}`)?.scrollIntoView({block:'center'});}
  if(data.variant)switchVariant(data.variant);
  if(data.variantStep){const variants=['A','B','C'];switchVariant(variants[(variants.indexOf(state.variant)+Number(data.variantStep)+3)%3]);}
});
document.addEventListener('input',event=>{if(event.target.id==='search'){state.search=event.target.value;state.page=1;updateResults();}});
document.addEventListener('change',event=>{if(['language','topic'].includes(event.target.id)){const field=event.target.id;state[field]=event.target.value;state.page=1;render();document.getElementById(field).focus({preventScroll:true});}});
document.addEventListener('keydown',event=>{
  if(document.getElementById('about-dialog').open)return;
  if(event.ctrlKey||event.metaKey||event.altKey)return;
  if(event.target.closest('input,textarea,select,[contenteditable="true"],[role="slider"]'))return;
  if(event.key==='/'){event.preventDefault();document.getElementById('search').focus();}
  if(['ArrowLeft','ArrowRight'].includes(event.key)){event.preventDefault();const values=['A','B','C'];switchVariant(values[(values.indexOf(state.variant)+(event.key==='ArrowRight'?1:2))%3]);}
});
window.addEventListener('popstate',()=>{state.variant=new URLSearchParams(location.search).get('variant')||'A';if(!['A','B','C'].includes(state.variant))state.variant='A';render();});
render();
