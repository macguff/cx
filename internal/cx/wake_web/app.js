'use strict';
const $ = s => document.querySelector(s);
const escapeHTML = v => String(v ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const e = escapeHTML;
const icon = name => `<svg class="icon" aria-hidden="true"><use href="#i-${name}"/></svg>`;
const zero = value => !value || String(value).startsWith('0001-');
const stamp = value => zero(value) ? 0 : Date.parse(value);
const compact = n => new Intl.NumberFormat('en', {notation:'compact', maximumFractionDigits:1}).format(n || 0);
const stateNames = {success:'请求完成',failed:'失败',running:'执行中',missed:'已错过',interrupted:'已中断'};
const stateColors = {success:'green',failed:'red',running:'blue',missed:'amber',interrupted:'neutral'};
let token = new URLSearchParams(location.hash.slice(1)).get('token');
if (token) { sessionStorage.setItem('cx-token', token); history.replaceState(null, '', location.pathname); }
token ||= sessionStorage.getItem('cx-token') || '';
let data = {profiles:[],tasks:[],history:[]}, offset = 0, selectedProfile = '', view = 'overview', kind = 'daily', editing = null;
let previewTimer, previewSequence = 0, toastTimer, refreshPending = false;
const busy = new Set();
const activityOpen = new Set();
document.addEventListener('toggle', event => {
  const details = event.target;
  if (!details.isConnected || !details.matches('.account-activity')) return;
  if (details.open) activityOpen.add(details.dataset.profile);
  else activityOpen.delete(details.dataset.profile);
}, true);
const now = () => Date.now() + offset;
const time = (value, options = {}) => !stamp(value) ? '—' : new Intl.DateTimeFormat('zh-CN', {hour:'2-digit',minute:'2-digit',hour12:false,...options}).format(new Date(value));
const fullTime = (value, zone) => time(value, {month:'2-digit',day:'2-digit', ...(zone && zone !== 'Local' ? {timeZone:zone} : {})});
const remaining = value => {
  const ms = stamp(value) - now();
  if (ms <= 0) return '等待刷新';
  const minutes = Math.ceil(ms / 60000);
  return minutes >= 60 ? `${Math.floor(minutes / 60)}h ${minutes % 60}m` : `${minutes}m`;
};
const ago = value => {
  if (!stamp(value)) return '尚未查询';
  const minutes = Math.max(0, Math.floor((now() - stamp(value)) / 60000));
  return minutes < 1 ? '刚刚' : minutes < 60 ? `${minutes} 分钟前` : `${Math.floor(minutes/60)} 小时前`;
};
const countdown = value => `<span data-deadline="${e(value)}">${remaining(value)}</span>`;
const status = value => `<span class="badge ${stateColors[value] || 'neutral'}">${e(stateNames[value] || '尚未执行')}</span>`;
const profiles = () => data.profiles.filter(p => !selectedProfile || p.name === selectedProfile).sort((a,b)=>Number(b.current)-Number(a.current)||a.name.localeCompare(b.name));
const tasks = () => data.tasks.filter(t => !selectedProfile || t.profile === selectedProfile);
function toast(message, error = false) {
  const element = $('#toast');
  element.textContent = message; element.className = `toast${error ? ' error' : ''}`;
  clearTimeout(toastTimer); toastTimer = setTimeout(() => element.classList.add('hidden'), error ? 6500 : 3500);
}
async function api(path, method = 'GET', body) {
  const headers = {'Authorization':`Bearer ${token}`};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const response = await fetch(path, {method, headers, ...(body !== undefined ? {body:JSON.stringify(body)} : {})});
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || `请求失败 (${response.status})`);
  return result;
}
async function refresh() {
  if (refreshPending || !token) return;
  refreshPending = true;
  try {
    data = await api('/api/state'); offset = stamp(data.now) - Date.now();
    $('#connection').className = 'connection'; $('#connection').innerHTML = '<i></i>后台运行中';
    $('#connection-error').classList.add('hidden'); $('#version').textContent = `cx ${data.version || ''}`;
    const filter = $('#profile-filter');
    const options = '<option value="">全部账号</option>' + data.profiles.map(p => `<option value="${e(p.name)}">${e(p.name)}</option>`).join('');
    if (filter.innerHTML !== options) { filter.innerHTML = options; filter.value = selectedProfile; }
    if (selectedProfile && !data.profiles.some(p => p.name === selectedProfile)) { selectedProfile = ''; filter.value = ''; }
    render(); $('#updated-at').textContent = `${time(data.now, {second:'2-digit'})} 更新`;
  } catch (error) {
    $('#connection').className = 'connection offline'; $('#connection').innerHTML = '<i></i>连接中断';
    $('#connection-error').classList.remove('hidden');
    $('#connection-error').textContent = `${error.message}。请确认后台运行中，或重新运行 cx wake web 打开页面。`;
  } finally { refreshPending = false; }
}
function quotaWindows(profile) {
  const entries = Object.entries(profile.quota?.buckets || {}).sort(([a],[b]) => a === 'codex' ? -1 : b === 'codex' ? 1 : a.localeCompare(b));
  return entries.flatMap(([id,bucket]) => [bucket.primary,bucket.secondary].filter(Boolean).map(window => ({...window,bucket:bucket.limitName || id,plan:bucket.planType})));
}
const windowLabel = w => !w.windowDurationMins ? '时长未知' : w.windowDurationMins === 10080 ? '7 天' : w.windowDurationMins % 60 === 0 ? `${w.windowDurationMins/60}h` : `${w.windowDurationMins} 分钟`;
function accountCard(profile) {
  const windows = quotaWindows(profile), primary = windows.find(w => w.windowDurationMins === 300) || windows[0];
  const weekly = windows.find(w => w.windowDurationMins === 10080);
  const fresh = primary && now() - stamp(profile.quota.updated_at) < 300000 && primary.resetsAt * 1000 > now();
  const left = primary ? Math.max(0, Math.min(100, 100 - primary.usedPercent)) : 0;
  const reset = primary?.resetsAt ? new Date(primary.resetsAt * 1000).toISOString() : '';
  const peak = Math.max(1,...profile.activity);
  const wake = data.history.filter(r => r.profile === profile.name && r.status === 'success').sort((a,b) => stamp(b.started_at)-stamp(a.started_at))[0];
  const active = data.history.some(r => r.profile === profile.name && r.status === 'running');
  return `<article class="account panel">
    <div class="account-header"><div><div class="account-name">${e(profile.name)} ${profile.current ? '<span class="badge neutral">当前</span>' : ''}</div><div class="account-sub" title="登录文件的本地状态，实际可用性以额度查询为准"><i></i>${profile.signed_in ? '已配置登录' : '尚未登录'}</div></div><button class="icon-button" data-action="quota" data-profile="${e(profile.name)}" aria-label="刷新 ${e(profile.name)} 额度" title="刷新实际额度" ${busy.has('quota:'+profile.name) ? 'disabled' : ''}>${icon('refresh')}</button></div>
    <div class="account-metrics">
    <div class="quota-heading"><span>${primary ? windowLabel(primary)+' 窗口 · '+(fresh ? '当前剩余' : '上次剩余') : '实际额度'}</span><span class="badge ${fresh ? 'blue' : 'amber'}">${primary ? fresh ? '服务端数据' : '快照待刷新' : '待查询'}</span></div>
    <div class="quota-value ${!primary ? 'unknown' : ''}"><strong>${primary ? Math.round(left)+'<span>%</span>' : '—'}</strong><small>${primary ? ago(profile.quota.updated_at)+'查询' : '刷新额度后显示'}</small></div>
    <div class="meter ${fresh ? '' : 'stale'}"><i style="width:${left}%"></i></div>
    <div class="quota-reset"><span>${reset ? `重置 <b title="${e(fullTime(reset))}">${time(reset)}</b>` : '未查询到服务端重置时间'}</span>${reset ? `<span>${countdown(reset)} · ${fresh ? '当前窗口' : '请刷新确认'}</span>` : ''}</div>
    ${weekly && weekly!==primary ? `<div class="weekly"><span>周额度剩余</span><div class="meter"><i style="width:${Math.max(0,Math.min(100,100-weekly.usedPercent))}%"></i></div><span>${Math.round(Math.max(0,100-weekly.usedPercent))}%</span></div>` : ''}
    ${windows.length > (weekly && weekly!==primary ? 2 : 1) ? `<details class="quota-details"><summary>查看全部额度窗口</summary>${windows.map(w => `<p>${e(w.bucket)} · ${windowLabel(w)} · 已用 ${Math.round(w.usedPercent)}% · ${w.resetsAt ? fullTime(new Date(w.resetsAt*1000).toISOString())+' 重置' : '重置时间未知'}</p>`).join('')}</details>` : ''}
    ${profile.quota?.error ? `<p class="quota-error">${e(profile.quota.error)}</p>` : ''}${profile.usage_error ? `<p class="quota-error">本地记录暂不可用：${e(profile.usage_error)}</p>` : ''}
    </div><details class="account-activity" data-profile="${e(profile.name)}" ${activityOpen.has(profile.name) ? 'open' : ''}><summary class="activity-label"><span>近 5 小时本地活动</span><b>${compact(profile.requests)} 次 / ${compact(profile.tokens)} tokens</b></summary><div class="spark" aria-label="近五小时每十分钟的 token 活动">${profile.activity.map((n,i) => `<i class="${n ? 'active' : ''}" style="height:${n ? Math.max(9,n/peak*100) : 5}%" title="${time(new Date(now()-5*3600000+i*600000).toISOString())} · ${compact(n)} tokens"></i>`).join('')}</div></details>
    <div class="account-footer"><button class="button" data-action="wake" data-profile="${e(profile.name)}" ${!profile.signed_in || active || busy.has('wake:'+profile.name) ? 'disabled' : ''}>${active ? '执行中' : icon('arrow')+'唤醒一次'}</button><button class="button" data-action="new" data-profile="${e(profile.name)}">${icon('plus')}添加计划</button><small>${wake ? '最近唤醒 '+ago(wake.started_at) : '尚无唤醒记录'}</small></div>
  </article>`;
}
function render() {
  const filtered = profiles(), planned = tasks(), enabled = planned.filter(t => t.enabled);
  const next = enabled.map(t => stamp(t.next_run)).filter(n => n > 0).sort((a,b) => a-b)[0];
  $('#nav-count').textContent = data.tasks.length;
  $('#account-count').textContent = filtered.length;
  $('#stats').innerHTML = [
    ['账号',`${filtered.length}<small>个 profile</small>`],
    ['启用的计划',`${enabled.length}<small>/ ${planned.length} 项</small>`],
    ['近 5h 本地请求',`${compact(filtered.reduce((n,p)=>n+p.requests,0))}<small>次</small>`],
    ['下次唤醒',next ? `${time(new Date(next).toISOString())}<small>${countdown(new Date(next).toISOString())}</small>` : '—']
  ].map(([label,value]) => `<div class="stat"><span>${label}</span><strong>${value}</strong></div>`).join('');
  $('#accounts').innerHTML = filtered.length ? filtered.map(accountCard).join('') : `<div class="empty panel"><span class="empty-icon">${icon('overview')}</span><h3>先添加一个 Codex 账号</h3><p>在终端运行 <code>cx login &lt;name&gt;</code>，登录后刷新此页。</p></div>`;
  $('#refresh-quotas').disabled = !filtered.length || busy.has('quota-all');
  renderTimeline(filtered,planned);
  $('#plan-summary').textContent = `${planned.length} 项计划 · ${enabled.length} 项启用`;
  $('#plans').innerHTML = planned.length ? planned.map(planCard).join('') : `<div class="empty panel"><span class="empty-icon">${icon('clock')}</span><h3>还没有唤醒计划</h3><p>选好账号和时间，提前安排下一次请求。</p><button class="button primary" data-action="new">${icon('plus')}创建第一个计划</button></div>`;
  renderHistory();
}
function renderTimeline(filtered, planned) {
  const start = Math.floor(now()/3600000)*3600000-2*3600000, span = 24*3600000;
  const position = t => (t-start)/span*100;
  const band = (from,to,projected,label='') => {
    const left = Math.max(0,position(from)), right = Math.min(100,position(to));
    return right <= left ? '' : `<div class="time-band ${projected ? 'projected' : ''}" style="left:${left}%;width:${right-left}%" title="${e(label)}">${!projected && right-left > 13 ? e(label) : ''}</div>`;
  };
  $('#timeline').innerHTML = `<div class="time-axis">${Array.from({length:7},(_,i) => `<span style="left:${i/6*100}%">${time(new Date(start+i*4*3600000).toISOString())}</span>`).join('')}</div>` + filtered.map(profile => {
    const freshWindows = quotaWindows(profile).filter(w => w.windowDurationMins > 0 && w.resetsAt*1000 > now() && now()-stamp(profile.quota.updated_at) < 300000);
    const official = freshWindows.find(w => w.windowDurationMins===300) || freshWindows.find(w => w.windowDurationMins<=1440);
    const slots = planned.filter(t => t.enabled && t.profile === profile.name).flatMap(task => task.slots.map(at => ({task,at:stamp(at)}))).sort((a,b)=>a.at-b.at);
    return `<div class="timeline-row"><div class="timeline-name" title="${e(profile.name)}">${e(profile.name)}</div><div class="timeline-track"><div class="now-line" style="left:${position(now())}%" title="现在"></div>${official ? band((official.resetsAt-official.windowDurationMins*60)*1000,official.resetsAt*1000,false,'服务端重置 '+fullTime(new Date(official.resetsAt*1000).toISOString())) : ''}${slots.map(({at}) => band(at,at+5*3600000,true,'参考窗口结束 '+fullTime(new Date(at+5*3600000).toISOString()))).join('')}${slots.filter(x => position(x.at)>=0 && position(x.at)<=100).map(({task,at}) => `<button class="time-event" style="left:${position(at)}%" data-action="edit" data-id="${e(task.id)}" title="${e(profile.name)} · ${fullTime(new Date(at).toISOString())} 计划请求" aria-label="编辑 ${e(profile.name)} 在 ${time(new Date(at).toISOString())} 的计划"></button>`).join('')}${official && position(official.resetsAt*1000) <= 100 ? `<div class="reset-line" style="left:${position(official.resetsAt*1000)}%" title="服务端重置 ${time(new Date(official.resetsAt*1000).toISOString())}"></div>` : ''}</div></div>`;
  }).join('') + (!filtered.length ? '<p class="fineprint">添加账号后，可在这里对照各个窗口与计划。</p>' : '');
}
function scheduleLabel(task) {
  const s=task.schedule;
  if (s.kind==='interval') return `每隔 ${e(s.every)}`;
  if (s.kind==='once') return `单次 · ${e(fullTime(s.at,s.timezone))}`;
  return `${s.kind==='daily' ? '每天' : '工作日'} · ${e(s.times.join(' / '))}`;
}
function planCard(task) {
  const finished = task.schedule.kind==='once' && zero(task.next_run);
  return `<article class="plan panel"><div class="plan-icon">${icon('clock')}</div><div class="plan-body"><div class="plan-title">${scheduleLabel(task)}<span class="badge ${task.enabled ? 'green':'neutral'}">${task.enabled ? '已启用':finished ? '已结束':'已暂停'}</span></div><div class="plan-times"><span>${e(task.profile)}</span><span>${e(task.schedule.timezone)}</span><span>${e(task.model || '默认模型')}${task.reasoning_effort ? ' · '+e(task.reasoning_effort) : ''}</span>${task.last_status ? status(task.last_status) : ''}</div></div><div class="plan-next"><small>下次请求</small>${task.enabled ? fullTime(task.next_run)+'<br><b>'+countdown(task.next_run)+'</b>' : '<span class="muted">—</span>'}</div><div class="plan-actions"><input class="toggle" type="checkbox" data-toggle="${e(task.id)}" aria-label="启用 ${e(task.profile)} 的计划" ${task.enabled ? 'checked':''} ${finished ? 'disabled':''}><button class="button" data-action="edit" data-id="${e(task.id)}" ${task.last_status==='running' ? 'disabled':''}>编辑</button><button class="button danger" data-action="delete" data-id="${e(task.id)}">删除</button></div></article>`;
}
function renderHistory() {
  const filter = $('#history-filter').value;
  const records = [...data.history].reverse().filter(r => (!selectedProfile || r.profile===selectedProfile) && (!filter || r.status===filter));
  $('#history').innerHTML = records.length ? `<table><thead><tr><th>时间</th><th>账号</th><th>结果</th><th>触发方式</th><th>耗时 / 说明</th></tr></thead><tbody>${records.map(r => `<tr><td>${time(r.started_at,{second:'2-digit'})}<small>${fullTime(r.started_at).split(' ')[0]}</small></td><td>${e(r.profile)}</td><td>${status(r.status)}</td><td>${r.trigger==='manual' ? '手动唤醒':'定时计划'}</td><td>${r.status==='running' ? '执行中…' : stamp(r.finished_at) ? Math.max(0,Math.round((stamp(r.finished_at)-stamp(r.started_at))/1000))+'s' : '—'}<small class="record-message">${r.status==='success' ? '请求已完成；可刷新额度确认窗口状态。' : r.status==='missed' ? '超过 5 分钟未执行，本次已跳过。' : e(r.message || '')}</small></td></tr>`).join('')}</tbody></table>` : `<div class="empty"><span class="empty-icon">${icon('history')}</span><h3>这里会记录每次唤醒</h3><p>执行结果自动保留，方便确认计划是否按时运行。</p></div>`;
}
function setView(name) {
  view = ['overview','plans','history'].includes(name) ? name : 'overview';
  for (const name of ['overview','plans','history']) $(`#${name}-view`).classList.toggle('hidden',name!==view);
  document.querySelectorAll('[data-view]').forEach(button => button.classList.toggle('active',button.dataset.view===view));
  $('#view-name').textContent = {overview:'窗口总览',plans:'唤醒计划',history:'执行历史'}[view];
  window.scrollTo({top:0,behavior:'instant'});
}
function ask(title, description, label='确认') {
  const dialog = $('#confirmation');
  if (dialog.open) return Promise.resolve(false);
  $('#confirm-title').textContent=title; $('#confirm-description').textContent=description; $('#confirm-ok').textContent=label;
  dialog.returnValue=''; dialog.showModal();
  return new Promise(resolve => dialog.addEventListener('close',()=>resolve(dialog.returnValue==='yes'),{once:true}));
}
$('#confirm-ok').onclick=()=>$('#confirmation').close('yes');
$('#confirm-cancel').onclick=()=>$('#confirmation').close('no');
function setKind(value) {
  kind=value;
  document.querySelectorAll('[data-kind]').forEach(button=>{button.classList.toggle('selected',button.dataset.kind===kind);button.setAttribute('aria-pressed',String(button.dataset.kind===kind));});
  $('#times-field').classList.toggle('hidden',!['daily','weekdays'].includes(kind));
  $('#at-field').classList.toggle('hidden',kind!=='once'); $('#interval-field').classList.toggle('hidden',kind!=='interval');
  schedulePreview();
}
function openEditor(id, profileName) {
  if (!data.profiles.length) { toast('先运行 cx login <name> 添加一个账号。',true); return; }
  editing=id || null;
  const task=data.tasks.find(t=>t.id===id), form=$('#task-form');
  form.reset(); form.elements.profile.innerHTML=data.profiles.map(p=>`<option value="${e(p.name)}">${e(p.name)}</option>`).join('');
  form.elements.profile.value=task?.profile || profileName || selectedProfile || data.profiles.find(p=>p.current)?.name || data.profiles[0].name;
  form.elements.timezone.value=task?.schedule.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Shanghai';
  form.elements.model.value=task ? (task.model || '') : 'gpt-6.1-terra'; form.elements.reasoning_effort.value=task ? (task.reasoning_effort || '') : 'low'; form.elements.times.value=task?.schedule.times?.join(',') || '05:00,11:01,17:10'; form.elements.enabled.checked=task ? task.enabled : true;
  if (task?.schedule.kind==='interval') {
    const parts=[...task.schedule.every.matchAll(/([\d.]+)(h|m|s)/g)];
    const minutes=parts.reduce((n,m)=>n+Number(m[1])*({h:60,m:1,s:1/60}[m[2]]),0);
    form.elements.hours.value=Math.floor(minutes/60); form.elements.minutes.value=Math.ceil(minutes%60);
  }
  if (task?.schedule.kind==='once') {
    const zone=task.schedule.timezone==='Local' ? undefined : task.schedule.timezone;
    const parts=new Intl.DateTimeFormat('sv-SE',{timeZone:zone,year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false}).format(new Date(task.schedule.at));
    form.elements.at.value=parts.replace(' ','T');
  } else {
    const future=new Date(now()+3600000); form.elements.at.value=new Date(future.getTime()-future.getTimezoneOffset()*60000).toISOString().slice(0,16);
  }
  $('#editor-title').textContent=task ? '编辑唤醒计划':'新建唤醒计划'; $('#form-error').classList.add('hidden');
  $('#editor').showModal(); setKind(task?.schedule.kind || 'daily');
}
function formInput() {
  const f=$('#task-form').elements;
  return {profile:f.profile.value,model:f.model.value.trim(),reasoning_effort:f.reasoning_effort.value,kind,times:f.times.value.trim(),at:f.at.value,every:`${Number(f.hours.value || 0)}h${Number(f.minutes.value || 0)}m`,timezone:f.timezone.value.trim(),enabled:f.enabled.checked};
}
function renderSchedulePreview(task, input) {
  if (input.kind === 'daily' || input.kind === 'weekdays') {
    const times = task.schedule.times.map(value => {
      const [hour, minute] = value.split(':').map(Number);
      return {value, minutes:hour * 60 + minute};
    });
    const span = Math.max(1440, Math.ceil((times[times.length-1].minutes + 300) / 60) * 60);
    const label = minutes => minutes === 1440 ? '24:00' : `${minutes > 1440 ? '次日 ' : ''}${String(Math.floor(minutes / 60) % 24).padStart(2,'0')}:${String(minutes % 60).padStart(2,'0')}`;
    const axis = `<div class="preview-axis">${[0,span/2,span].map(n => `<span>${e(label(Math.round(n)))}</span>`).join('')}</div>`;
    const rows = times.map(({value,minutes}) => `<div class="preview-window"><div class="preview-slot"><span>${e(value)} 唤醒</span><span>+5h → ${e(label(minutes+300))}</span></div><div class="timeline-track preview-track"><div class="time-band projected" style="left:${minutes/span*100}%;width:${300/span*100}%" title="参考窗口结束 ${e(label(minutes+300))}"></div><span class="time-event" style="left:${minutes/span*100}%" title="${e(value)} 计划请求"></span></div></div>`).join('');
    const actual = task.slots.map(at => `<li>${e(fullTime(at,input.timezone))}</li>`).join('');
    return axis + rows + `<details class="preview-upcoming"><summary>查看接下来实际执行的三次时间</summary><ol>${actual}</ol></details>`;
  }
  const profile = data.profiles.find(p => p.name === input.profile);
  const windows = profile ? quotaWindows(profile).filter(w => w.windowDurationMins > 0 && w.resetsAt * 1000 > now() && now() - stamp(profile.quota.updated_at) < 300000) : [];
  const official = windows.find(w => w.windowDurationMins === 300) || windows.find(w => w.windowDurationMins <= 1440);
  const slots = task.slots.map(stamp), start = Math.floor(slots[0] / 3600000) * 3600000 - 3600000;
  const span = Math.max(24 * 3600000, Math.ceil((slots[slots.length-1] + 5 * 3600000 - start) / 3600000) * 3600000);
  const position = ms => (ms - start) / span * 100;
  const axis = `<div class="preview-axis">${[0,0.5,1].map(f => `<span>${e(fullTime(new Date(start+span*f).toISOString(), input.timezone))}</span>`).join('')}</div>`;
  return axis + task.slots.map((value, index) => {
    const at = slots[index];
    const left = official ? Math.max(0, position((official.resetsAt - official.windowDurationMins * 60) * 1000)) : 0;
    const right = official ? Math.min(100, position(official.resetsAt * 1000)) : 0;
    const server = right > left ? `<div class="time-band" style="left:${left}%;width:${right-left}%" title="服务端重置 ${e(fullTime(new Date(official.resetsAt*1000).toISOString(),input.timezone))}"></div>` : '';
    return `<div class="preview-window"><div class="preview-slot"><span>第 ${index+1} 次 · ${e(fullTime(value, input.timezone))}</span><span>+5h → ${e(fullTime(new Date(at+5*3600000).toISOString(), input.timezone))}</span></div><div class="timeline-track preview-track">${server}<div class="time-band projected" style="left:${position(at)}%;width:${5*3600000/span*100}%" title="参考窗口结束 ${e(fullTime(new Date(at+5*3600000).toISOString(),input.timezone))}"></div><span class="time-event" style="left:${position(at)}%" title="第 ${index+1} 次计划请求"></span></div></div>`;
  }).join('');
}
function schedulePreview() {
  clearTimeout(previewTimer); $('#save-task').disabled=true;
  $('#preview').textContent='正在更新预览…';
  const sequence=++previewSequence;
  previewTimer=setTimeout(async()=>{
    if (!$('#editor').open) return;
    try {
      const input=formInput(), task=await api('/api/preview','POST',input);
      if (sequence!==previewSequence || !$('#editor').open) return;
      const daily = input.kind === 'daily' || input.kind === 'weekdays';
      $('#preview-title').textContent=daily ? (input.kind === 'daily' ? '每天的计划时间' : '每个工作日的计划时间') : '接下来三次请求与窗口';
      $('#preview-server-legend').classList.toggle('hidden',daily);
      $('#preview-note').textContent=daily ? '时间轴按所选时区展示同一天的安排；只有 +5h 跨过午夜时才显示次日。夏令时切换可能影响实际间隔。+5h 是规划参考，不代表已确认的额度重置。' : '各行使用同一时间刻度，按未来执行顺序排列。实线仅显示已刷新且仍有效的服务端窗口；+5h 是规划参考，不代表已确认的额度重置。';
      $('#preview').innerHTML=renderSchedulePreview(task,input);
      $('#save-task').disabled=false; $('#form-error').classList.add('hidden');
    } catch (error) {
      if (sequence!==previewSequence) return;
      $('#preview').textContent=error.message; $('#save-task').disabled=true;
    }
  },250);
}
$('#task-form').addEventListener('input',schedulePreview);
$('#task-form').addEventListener('change',schedulePreview);
$('#task-form').addEventListener('submit',async event=>{
  event.preventDefault(); const button=$('#save-task'); button.disabled=true;
  try { await api(editing ? `/api/tasks/${encodeURIComponent(editing)}`:'/api/tasks',editing ? 'PUT':'POST',formInput()); $('#editor').close(); toast('计划已保存'); await refresh(); }
  catch(error) { $('#form-error').textContent=error.message; $('#form-error').classList.remove('hidden'); button.disabled=false; }
});
document.addEventListener('click',async event=>{
  const nav=event.target.closest('[data-view]'); if(nav){setView(nav.dataset.view);return;}
  const kindButton=event.target.closest('[data-kind]'); if(kindButton){setKind(kindButton.dataset.kind);return;}
  const preset=event.target.closest('[data-preset]');
  if(preset){ const f=$('#task-form').elements; if(preset.dataset.preset==='three'){f.times.value='05:00,11:01,17:10';setKind('daily');}else if(preset.dataset.preset==='interval'){f.hours.value=5;f.minutes.value=1;setKind('interval');}else{f.times.value='07:00';setKind('weekdays');} return; }
  const button=event.target.closest('[data-action]'); if(!button || button.disabled)return;
  const {action,id,profile}=button.dataset;
  let busyKey;
  try {
    switch(action){
      case 'new':openEditor(null,profile);break;
      case 'edit':openEditor(id);break;
      case 'close-editor':$('#editor').close();break;
      case 'refresh':await refresh();break;
      case 'quota':
        busyKey='quota:'+profile; if(busy.has(busyKey))return; busy.add(busyKey); render();
        await api(`/api/profiles/${encodeURIComponent(profile)}/quota`,'POST',{});toast(`${profile} 额度已更新`);break;
      case 'quota-all':
        busyKey='quota-all'; if(busy.has(busyKey))return; busy.add(busyKey); render();
        for(const p of profiles()){try{await api(`/api/profiles/${encodeURIComponent(p.name)}/quota`,'POST',{});}catch(error){toast(`${p.name}: ${error.message}`,true);}}
        break;
      case 'wake':
        if(!(await ask(`唤醒 ${profile}？`,'这会发送一次简短的 Codex 请求并消耗额度。已有计划的时间不会改变。','发送请求')))return;
        busyKey='wake:'+profile;if(busy.has(busyKey))return;busy.add(busyKey);render();toast('正在发送唤醒请求，可在执行历史查看进度。');
        await api(`/api/profiles/${encodeURIComponent(profile)}/wake`,'POST',{});toast('请求已完成，可以刷新实际额度确认状态。');break;
      case 'delete':
        if(!(await ask('删除这项计划？','后续定时请求将停止，已有执行历史会保留。','删除计划')))return;
        await api(`/api/tasks/${encodeURIComponent(id)}`,'DELETE');toast('计划已删除');break;
      case 'pause-all':case 'resume-all':
        for(const task of tasks().filter(t=>action==='pause-all' ? t.enabled : !t.enabled && !(t.schedule.kind==='once' && zero(t.next_run)))){await api(`/api/tasks/${encodeURIComponent(task.id)}`,'POST',{action:action==='pause-all'?'pause':'resume'});}
        toast(action==='pause-all'?'计划已暂停':'计划已恢复');break;
    }
  }catch(error){toast(error.message,true);}
  finally{if(busyKey)busy.delete(busyKey);await refresh();}
});
document.addEventListener('change',async event=>{
  const toggle=event.target.closest('[data-toggle]');if(!toggle)return;
  toggle.disabled=true;
  try{await api(`/api/tasks/${encodeURIComponent(toggle.dataset.toggle)}`,'POST',{action:toggle.checked?'resume':'pause'});toast(toggle.checked?'计划已恢复':'计划已暂停');}
  catch(error){toggle.checked=!toggle.checked;toast(error.message,true);}
  finally{toggle.disabled=false;await refresh();}
});
$('#profile-filter').onchange=event=>{selectedProfile=event.target.value;render();};
$('#history-filter').onchange=renderHistory;
function tick(){
  $('#clock').textContent=time(new Date(now()).toISOString(),{month:'2-digit',day:'2-digit',weekday:'short',second:'2-digit'});
  document.querySelectorAll('[data-deadline]').forEach(element=>element.textContent=remaining(element.dataset.deadline));
}
if(!token){$('#connection-error').textContent='请运行 cx wake web，使用输出的完整链接打开此页面。';$('#connection-error').classList.remove('hidden');$('#connection').className='connection offline';$('#connection').innerHTML='<i></i>等待访问链接';}
refresh();tick();setInterval(refresh,5000);setInterval(tick,1000);
