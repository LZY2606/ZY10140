'use strict';
const $ = (id) => document.getElementById(id);
const state = {
  sounding: null, versions: [], versionsDetail: {}, activeVersion: null,
  points: [], layers: [], branches: [], findings: [], raw: [], judgments: [],
  flags: {}, missing: {}, sel: null, selectedPoint: null,
};

const PHASE_CLASS = {ascent:'t-ascent', descent:'t-descent', float:'t-float', terminate:'t-terminate'};
const PHASE_COLOR = {ascent:'#79c0ff', descent:'#ffb4b0', float:'#c792ea', terminate:'#8b949e'};

function sid(){ return $('sid').value.trim() || 'demo'; }
function analyst(){ return $('analyst').value.trim() || 'analyst'; }

async function api(path, opts){
  const res = await fetch(path, opts || {});
  const ct = res.headers.get('content-type')||'';
  const body = ct.includes('json') ? await res.json() : await res.text();
  if(!res.ok){
    if(body && body.ranges) throw {status:res.status, body};
    throw new Error((body && body.error) || res.statusText);
  }
  return body;
}
function toast(msg, bad){
  const t=document.createElement('div'); t.className='toast'+(bad?' bad':'');
  t.textContent=msg; $('toasts').appendChild(t);
  setTimeout(()=>t.remove(), 5200);
}
function validDate(t){ if(!t) return null; const d=new Date(t); return isNaN(d.getTime())?null:d; }
function fmtTime(t){ const d=validDate(t); return d?d.toISOString().substring(11,19):''; }
function fmtTimeFull(t){ const d=validDate(t); return d?d.toLocaleString():''; }
function num(v,d=1){ return v==null ? '—' : Number(v).toFixed(d); }
function esc(s){ return String(s==null?'':s).replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c])); }

async function loadFlags(){
  try{ const f = await api('/api/flags'); state.flags=f.flags||{}; state.missing=f.missing||{}; }catch(e){}
}

async function loadSounding(){
  const id = sid();
  $('pubState').textContent='加载中…';
  const v = await api('/api/soundings/'+encodeURIComponent(id));
  state.sounding=v; state.versions=v.versions||[]; state.raw=v.raw_packets||[];
  state.judgments=v.judgments||[];
  if(v.published){
    $('pubState').textContent='已发布 v'+v.published.version_id+' · '+v.published.layer_count+' 层';
    $('pubState').style.borderColor='var(--ok)'; $('pubState').style.color='var(--ok)';
  }else{
    $('pubState').textContent='未发布（草稿）';
    $('pubState').style.borderColor='var(--line)'; $('pubState').style.color='';
  }
  renderVersions(); renderRaw(); renderJudgments();
  // pick: latest draft, else candidate_a, else last version
  const pick = state.versions.filter(x=>!x.published_at).sort((a,b)=>b.id-a.id)[0]
    || state.versions[state.versions.length-1];
  if(pick){ selectVersion(pick.id); }
  else { state.points=[]; state.layers=[]; state.branches=[]; state.findings=[]; render(); }
}

async function selectVersion(vid){
  state.activeVersion=vid;
  const d = await api('/api/versions/'+vid);
  state.points=d.points||[]; state.layers=d.layers||[];
  state.branches=d.branches||[]; state.findings=d.findings||[];
  renderEvidence(); renderJudgments(); renderConflictBox();
  const meta = state.versions.find(x=>x.id===vid);
  $('curVer').textContent='v'+vid;
  $('curKind').textContent = meta ? meta.kind : '';
  renderVersions(); render();
}

function renderVersions(){
  const box=$('versions'); box.innerHTML='';
  state.versions.forEach(v=>{
    const div=document.createElement('div');
    div.className='ver'+(v.id===state.activeVersion?' active':'')+(v.published_at?' pub':'');
    div.onclick=()=>selectVersion(v.id);
    const badge = v.published_at ? '🔒 已发布' : ({draft:'草稿',candidate_a:'候选 A',candidate_b:'候选 B'}[v.kind]||v.kind);
    div.innerHTML = '<input type="radio" name="ver" '+(v.id===state.activeVersion?'checked':'')+' onclick="event.stopPropagation()">'
      + '<div style="flex:1"><b>v'+v.id+'</b> <span class="mut small">'+badge+'</span>'
      + (v.published_at?'<div class="small mut">'+fmtTime(v.published_at)+' · '+esc(v.published_by||'')+'</div>':'')
      + '</div>';
    box.appendChild(div);
  });
}

function renderRaw(){
  const tb=$('rawTable').querySelector('tbody'); tb.innerHTML='';
  $('rawCount').textContent='（'+state.raw.length+' 条原始证据，含重试/迟到）';
  state.raw.forEach(sp=>{
    const p=sp.Packet||sp;
    const tr=document.createElement('tr');
    let tags='';
    if(sp.dup_kind==='retry') tags+='<span class="tag flag">重试</span>';
    if(sp.dup_kind==='conflict') tags+='<span class="tag flag bad">冲突</span>';
    if(sp.late) tags+='<span class="tag flag bad">迟到</span>';
    tr.innerHTML='<td class="mut">'+sp.id+'</td><td class="mono">'+p.seq+'</td>'
      +'<td>'+fmtTime(p.obs_time)+'</td><td class="mut">'+fmtTime(sp.received_at)+'</td>'
      +'<td>'+num(p.pressure_hpa,1)+'</td><td>'+num(p.temp_c,1)+'</td><td>'+num(p.rh_pct,0)+'</td>'
      +'<td>'+tags+'</td>';
    tb.appendChild(tr);
  });
}

function renderLayers(){
  const tb=$('layerTable').querySelector('tbody'); tb.innerHTML='';
  state.layers.forEach(l=>{
    const src = l.exact ? '<span class="tag flag">观测</span>'
      : l.interpolated ? '<span class="tag flag">插值</span>' : '<span class="tag">—</span>';
    let flags=(l.flags_json||l.flags||[]).map(f=>'<span class="tag flag">'+esc(f)+'</span>').join('');
    if(!flags && l.missing) flags='';
    const tr=document.createElement('tr');
    tr.innerHTML='<td><span class="tag '+PHASE_CLASS[l.phase]+'">#'+l.branch_id+' '+l.phase+'</span></td>'
      +'<td class="mono"><b>'+num(l.pressure,0)+'</b></td>'
      +'<td>'+num(l.temp_c,1)+'</td><td>'+num(l.rh_pct,0)+'</td><td>'+num(l.alt_gps_m,0)+'</td>'
      +'<td>'+src+'</td>'
      +'<td class="miss">'+(l.missing?'<span title="'+esc(state.missing[l.missing]||'')+'">'+l.missing+'</span>':'')+'</td>'
      +'<td>'+flags+'</td>';
    tb.appendChild(tr);
  });
}

function renderTransitions(){
  const box=$('transitions'); box.innerHTML='';
  if(state.branches.length===0){ box.innerHTML='<span class="mut">无分支</span>'; return; }
  let html='<div class="row" style="gap:6px">';
  state.branches.forEach((b,i)=>{
    html += '<span class="tag '+PHASE_CLASS[b.phase]+'" style="font-size:11px;padding:3px 9px">'
      + '分支#'+b.branch_id+' '+b.phase + ' ('+(b.source==='manual'?'人工':'自动')+') '
      + fmtTime(b.start_time)+'–'+fmtTime(b.end_time)+'</span>';
    if(i<state.branches.length-1) html += '<span class="mut">→</span>';
  });
  html+='</div>';
  // transition events from findings
  const evs=(state.findings||[]).filter(f=>['burst','terminate','descent','float','ascent'].includes(f.kind));
  html+='<div class="mut" style="margin-top:6px">';
  evs.forEach(f=>{ html+='<div>• '+f.kind+' @ '+fmtTime(f.start_time)+(f.detail?' — '+esc(f.detail):'')+'</div>'; });
  html+='</div>';
  box.innerHTML=html;
}

function renderJudgments(){
  const tb=$('judgeTable').querySelector('tbody'); tb.innerHTML='';
  state.judgments.forEach(j=>{
    let range;
    if(j.seq!=null) range='seq '+j.seq+(j.chosen_hash?' → '+j.chosen_hash.substring(0,8):'');
    else range=(j.start?fmtTime(j.start):'')+'–'+(j.end?fmtTime(j.end):'')+(j.phase?(' '+j.phase):'');
    const tr=document.createElement('tr');
    tr.innerHTML='<td>'+esc(j.kind)+'</td><td class="mono small">'+esc(range)+'</td>'
      +'<td>'+esc(j.signed_by)+'</td><td>'+esc(j.reason||'')+'</td>';
    tb.appendChild(tr);
  });
  renderConflictBox();
}

function renderConflictBox(){
  const box=$('conflictBox'); box.innerHTML='';
  // Group raw evidence by (device,seq) to find conflicting payloads.
  const groups={};
  state.raw.forEach(sp=>{
    const key=sp.device+'#'+sp.seq;
    (groups[key]=groups[key]||[]).push(sp);
  });
  let any=false;
  Object.keys(groups).sort().forEach(key=>{
    const rows=groups[key];
    const hashes=[...new Set(rows.map(r=>r.hash))];
    if(hashes.length<2) return;
    any=true;
    // one representative per distinct hash
    const reps={};
    rows.forEach(r=>{ if(!(r.hash in reps)) reps[r.hash]=r; });
    const seq=rows[0].seq, device=rows[0].device;
    const div=document.createElement('div');
    div.style.cssText='border:1px solid var(--warn);border-radius:8px;padding:6px;margin-bottom:6px';
    let btns='';
    hashes.slice().sort().forEach((h,i)=>{
      const label=i===0?'选首次版本':'选此版本';
      btns+='<button class="mini" onclick="chooseVariant(\''+device+'\','+seq+',\''+h+'\')">'+label+' '+h.substring(0,8)+'</button>';
    });
    btns+='<button class="mini" onclick="retainBoth()">保留两条轨迹</button>';
    let detail='';
    hashes.forEach(h=>{ const r=reps[h];
      detail+='<div class="small">'+h.substring(0,12)+' T='+num(r.temp_c,2)+' P='+num(r.pressure_hpa,1)+' 到达 '+fmtTime(r.received_at)+'</div>'; });
    div.innerHTML='<div class="small"><b>设备 '+esc(device)+' seq '+seq+' 载荷冲突</b></div>'
      +'<div class="row" style="margin-top:4px">'+btns+'</div>'
      +'<div style="margin-top:3px">'+detail+'</div>';
    box.appendChild(div);
  });
  if(!any) box.innerHTML='<div class="small mut">当前无重复包冲突</div>';
}

// ---------- charts ----------
function phaseVisible(ph){
  if(ph==='ascent') return $('showAscent').checked;
  if(ph==='descent') return $('showDescent').checked;
  if(ph==='float') return $('showFloat').checked;
  return true;
}

function render(){
  if(!$('chart')) return;
  renderLayers(); renderTransitions();
  drawProfile(); drawPressureTime(); updateSelInfo();
}

function drawProfile(){
  const svg=$('chart'); svg.innerHTML='';
  const W=760,H=420,ml=52,mr=14,mt=12,mb=30;
  const pts=state.points.filter(p=>p.pressure_hpa!=null && phaseVisible(p.phase));
  if(pts.length<2){ svg.innerHTML='<text class="lbl" x="20" y="30">无足够点</text>'; return; }
  const pressures=pts.map(p=>p.pressure_hpa);
  const alts=pts.map(p=>p.alt_gps_m).filter(x=>x!=null);
  const pMin=Math.min(...pressures), pMax=Math.max(...pressures);
  const aMin=alts.length?Math.min(...alts):0, aMax=alts.length?Math.max(...alts):1;
  const x=(a)=>(a-aMin)/((aMax-aMin)||1)*(W-ml-mr)+ml;
  const y=(p)=>(Math.log(p)-Math.log(pMin))/(Math.log(pMax)-Math.log(pMin)||1)*(H-mt-mb)+mt;
  // grid pressure labels (log)
  const ticks=state.layers.length?[...new Set(state.layers.map(l=>l.pressure))].sort((a,b)=>b-a):[];
  let g='';
  [1000,850,700,500,300,200,100,50,20,10].forEach(p=>{
    if(p<pMin||p>pMax) return;
    const yy=y(p);
    g+='<line class="grid" x1="'+ml+'" y1="'+yy+'" x2="'+W-mr+'" y2="'+yy+'"/>'
      +'<text class="lbl" x="'+(ml-6)+'" y="'+(yy+3)+'" text-anchor="end">'+p+'</text>';
  });
  svg.innerHTML=g;
  // branch bands
  const t0=Math.min(...state.points.map(p=>+new Date(p.obs_time)));
  const t1=Math.max(...state.points.map(p=>+new Date(p.obs_time)));
  // Draw per branch polylines by time-normalized x? Use altitude x to show p/alt profile.
  const byBranch={};
  state.points.filter(p=>p.alt_gps_m!=null && p.pressure_hpa!=null && phaseVisible(p.phase))
    .forEach(p=>{(byBranch[p.branch_id]=byBranch[p.branch_id]||[]).push(p)});
  Object.keys(byBranch).sort((a,b)=>a-b).forEach(bid=>{
    const arr=byBranch[bid];
    const d=arr.map((p,i)=> (i?'L':'M')+x(p.alt_gps_m).toFixed(1)+','+y(p.pressure_hpa).toFixed(1)).join(' ');
    svg.insertAdjacentHTML('beforeend','<path class="curve" d="'+d+'" stroke="'+PHASE_COLOR[arr[0].phase]+'"/>');
  });
  // points
  state.points.filter(p=>p.alt_gps_m!=null && p.pressure_hpa!=null && phaseVisible(p.phase)).forEach((p)=>{
    const flags=p.flags||[];
    let fill=PHASE_COLOR[p.phase]||'#999', r=2.2;
    if(flags.includes('PRESSURE_REVERSAL')){ fill='#d29922'; r=3.4; }
    if(flags.includes('ICING')||flags.includes('ICING_MANUAL')){ fill='#7ad1ff'; r=3.0; }
    const sel=state.selectedPoint && state.selectedPoint.seq===p.seq && state.selectedPoint.branch_id===p.branch_id;
    svg.insertAdjacentHTML('beforeend','<circle class="point'+(sel?' pt-sel':'')+'" data-seq="'+p.seq+'" data-branch="'+p.branch_id+'" cx="'+x(p.alt_gps_m)+'" cy="'+y(p.pressure_hpa)+'" r="'+r+'" fill="'+fill+'"><title>seq '+p.seq+' '+p.phase+' '+p.pressure_hpa.toFixed(1)+'hPa</title></circle>');
  });
  // missing GPS points at interpolated pressure: faint markers
  svg.insertAdjacentHTML('beforeend','<text class="lbl" x="'+ml+'" y="'+(H-8)+'">GPS高度 (m) → '+aMin.toFixed(0)+' … '+aMax.toFixed(0)+'</text>');
  svg.insertAdjacentHTML('beforeend','<text class="lbl" x="14" y="14">P(hPa)</text>');
  // axes
  svg.insertAdjacentHTML('beforeend','<line class="axis" x1="'+ml+'" y1="'+mt+'" x2="'+ml+'" y2="'+(H-mb)+'"/>');
  svg.insertAdjacentHTML('beforeend','<line class="axis" x1="'+ml+'" y1="'+(H-mb)+'" x2="'+(W-mr)+'" y2="'+(H-mb)+'"/>');
  svg.querySelectorAll('circle.point').forEach(c=>{
    c.onclick=()=>{
      const seq=+c.dataset.seq, br=+c.dataset.branch;
      state.selectedPoint=state.points.find(p=>p.seq===seq&&p.branch_id===br);
      renderPointDetail(); drawProfile(); drawPressureTime();
    };
  });
}

function drawPressureTime(){
  const svg=$('ptime'); svg.innerHTML='';
  const W=760,H=150,ml=52,mr=14,mt=10,mb=22;
  const pts=state.points.filter(p=>p.pressure_hpa!=null);
  if(pts.length<2){ return; }
  const t0=+new Date(pts[0].obs_time), t1=+new Date(pts[pts.length-1].obs_time);
  const ps=pts.map(p=>p.pressure_hpa), pMin=Math.min(...ps), pMax=Math.max(...ps);
  const X=(t)=>(+new Date(t)-t0)/((t1-t0)||1)*(W-ml-mr)+ml;
  const Y=(p)=>H-mb-(p-pMin)/((pMax-pMin)||1)*(H-mt-mb);
  [0,.25,.5,.75,1].forEach(f=>{
    const p=pMin+(pMax-pMin)*f, yy=Y(p);
    svg.insertAdjacentHTML('beforeend','<line class="grid" x1="'+ml+'" y1="'+yy+'" x2="'+(W-mr)+'" y2="'+yy+'"/><text class="lbl" x="'+(ml-6)+'" y="'+(yy+3)+'" text-anchor="end">'+p.toFixed(0)+'</text>');
  });
  // branch background bands
  state.branches.forEach(b=>{
    if(b.phase==='terminate') return;
    const x1=X(b.start_time),x2=X(b.end_time);
    svg.insertAdjacentHTML('beforeend','<rect class="branchband" x="'+x1+'" y="'+mt+'" width="'+Math.max(2,x2-x1)+'" height="'+(H-mt-mb)+'" fill="'+PHASE_COLOR[b.phase]+'"/>');
  });
  const d=pts.map((p,i)=>(i?'L':'M')+X(p.obs_time).toFixed(1)+','+Y(p.pressure_hpa).toFixed(1)).join(' ');
  svg.insertAdjacentHTML('beforeend','<path class="curve" d="'+d+'" stroke="#9fb4c8"/>');
  pts.forEach(p=>{
    const flags=p.flags||[];
    let fill=PHASE_COLOR[p.phase]||'#999';
    if(flags.includes('PRESSURE_REVERSAL')) fill='#d29922';
    if(flags.includes('LATE_PACKET')) fill='#f85149';
    svg.insertAdjacentHTML('beforeend','<circle data-seq="'+p.seq+'" data-branch="'+p.branch_id+'" class="point" cx="'+X(p.obs_time)+'" cy="'+Y(p.pressure_hpa)+'" r="2.4" fill="'+fill+'"><title>'+fmtTime(p.obs_time)+' seq '+p.seq+'</title></circle>');
  });
  svg.insertAdjacentHTML('beforeend','<text class="lbl" x="'+ml+'" y="'+(H-6)+'">观测时间 →</text>');
  svg.querySelectorAll('circle.point').forEach(c=>{
    c.onclick=()=>{
      const seq=+c.dataset.seq,br=+c.dataset.branch;
      state.selectedPoint=state.points.find(p=>p.seq===seq&&p.branch_id===br);
      renderPointDetail(); drawProfile(); drawPressureTime();
    };
  });
}

function renderPointDetail(){
  const p=state.selectedPoint; const box=$('ptDetail');
  if(!p){ box.textContent='点击剖面或时线上的点'; return; }
  let html='<div class="kv">'
    +'<b>seq</b><span class="mono">'+p.seq+'</span>'
    +'<b>时间</b><span>'+fmtTimeFull(p.obs_time)+'</span>'
    +'<b>分支</b><span>#'+p.branch_id+' <span class="tag '+PHASE_CLASS[p.phase]+'">'+p.phase+'</span></span>'
    +'<b>P/T/RH</b><span class="mono">'+num(p.pressure_hpa,2)+' / '+num(p.temp_c,2)+' / '+num(p.rh_pct,1)+'</span>'
    +'<b>GPS</b><span class="mono">'+num(p.alt_gps_m,0)+' m</span>'
    +'<b>载荷</b><span class="mono small">'+esc((p.hash||'').substring(0,12))+'</span></div>';
  html+='<div style="margin-top:6px">'+(p.flags||[]).map(f=>flagTag(f)).join(' ')+'</div>';
  // set selection interval to this point ±1 sample for quick marking
  state.sel={start:new Date(p.obs_time),end:new Date(p.obs_time), seq:p.seq};
  box.innerHTML=html; updateSelInfo();
}
function flagTag(f){
  const doc=state.flags[f]||'';
  const bad=['DUP_CONFLICT','LATE_PACKET','PRESSURE_REVERSAL','GPS_GAP_MISSING','TERMINATED'].includes(f);
  return '<span class="tag flag'+(bad?' bad':'')+'" title="'+esc(doc)+'">'+esc(f)+'</span>';
}
function updateSelInfo(){
  const el=$('selInfo');
  if(!state.sel){ el.textContent='未选择'; return; }
  if(state.sel.seq!=null) el.textContent='seq '+state.sel.seq;
  else el.textContent=fmtTime(state.sel.start)+' – '+fmtTime(state.sel.end);
}

// ---------- evidence panel ----------
function renderEvidence(){
  const box=$('evidence'); let html='';
  const used={};
  // layer flags in current version
  state.layers.forEach(l=>(l.flags||[]).forEach(f=>used[f]=true));
  (state.points||[]).forEach(p=>(p.flags||[]).forEach(f=>used[f]=true));
  const keys=Object.keys(used).filter(f=>state.flags[f]);
  if(!keys.length){ html='<span class="mut">当前版本无质量标志</span>'; }
  keys.sort().forEach(f=>{
    html+='<details><summary>'+flagTag(f)+'</summary><div class="mut" style="padding:4px 0 6px">'+esc(state.flags[f])+'</div></details>';
  });
  html+='<h2 style="margin:10px 0 4px">缺失原因口径</h2>';
  const miss=[...new Set((state.layers||[]).map(l=>l.missing).filter(Boolean))];
  if(!miss.length) html+='<div class="mut">无缺失层</div>';
  miss.forEach(m=>{ html+='<details><summary><span class="miss">'+m+'</span></summary><div class="mut" style="padding:4px 0 6px">'+esc(state.missing[m]||'')+'</div></details>'; });
  box.innerHTML=html;
}

// ---------- judgments ----------
function currentInterval(){
  // derive an interval from selected point ± one sample on the active branch
  const p=state.selectedPoint;
  if(!p){ toast('请先在图上点击一个点来确定区间起点',true); return null; }
  const arr=state.points;
  const i=arr.findIndex(x=>x.seq===p.seq&&x.branch_id===p.branch_id);
  const start = new Date(arr[Math.max(0,i-1)].obs_time);
  const end = new Date(arr[Math.min(arr.length-1,i+1)].obs_time);
  return {start,end};
}

async function postJudgment(body){
  body.signed_by=analyst(); body.reason=$('reason').value||body.reason||'';
  try{
    await api('/api/soundings/'+encodeURIComponent(sid())+'/judgments',
      {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
    toast('判定已保存，草稿已重建（已发布剖面不变）');
    await loadSounding();
  }catch(e){
    if(e.status===409 && e.body && e.body.ranges){
      const rs=e.body.ranges.map(r=>'受影响时间段 '+fmtTimeFull(r.start)+' – '+fmtTimeFull(r.end)+'（'+r.other_signed_by+' rev'+r.other_rev+'）').join('\n');
      toast('并发判定冲突：\n'+rs, true);
    } else toast('保存失败：'+(e.message||e), true);
  }
}

function markPhase(phase){
  const iv=currentInterval(); if(!iv) return;
  postJudgment({kind:'phase_override',start:iv.start.toISOString(),end:iv.end.toISOString(),phase});
}
function markIcing(){
  const iv=currentInterval(); if(!iv) return;
  postJudgment({kind:'icing',start:iv.start.toISOString(),end:iv.end.toISOString()});
}
async function chooseVariant(device, seq, hash){
  if(!hash){ toast('该候选哈希缺失，无法直接选择；请在原始数据中确认',true); return; }
  await postJudgment({kind:'variant_choice',device:device,seq:seq,chosen_hash:hash});
}
async function retainBoth(){
  await postJudgment({kind:'retain_both',reason:'保留两个候选轨迹'});
}

// ---------- versions / publish ----------
async function publishSelected(){
  if(!state.activeVersion){ toast('请先选择版本',true); return; }
  const note=$('reason').value||'';
  try{
    const rec=await api('/api/soundings/'+encodeURIComponent(sid())+'/publish',{
      method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({version_id:state.activeVersion,published_by:analyst(),note})});
    toast('已原子发布 v'+rec.version_id+'，共 '+rec.layer_count+' 层');
    await loadSounding();
  }catch(e){ toast('发布失败（未标记任何层为已发布）：'+e.message,true); }
}
async function rebuild(){
  await api('/api/soundings/'+encodeURIComponent(sid())+'/rebuild',{method:'POST'});
  toast('已用新算法重跑草稿'); await loadSounding();
}

// ---------- simulator ----------
async function sim(action){
  try{
    let body=null, method='POST';
    if(action==='start') body=JSON.stringify({sounding_id:sid()});
    if(action==='batch') body=JSON.stringify({count:8});
    const r=await fetch('/api/simulator/'+action,{method,headers:body?{'Content-Type':'application/json'}:{},body});
    const data=await r.json();
    if(!r.ok) throw new Error(data.error||r.statusText);
    await refreshSimStatus();
    await loadSounding();
    if(data.result) toast('批次接收：首包 '+data.result.first+' 重试 '+data.result.retries+' 冲突 '+data.result.conflicts+' 迟到 '+data.result.late+(data.published?' · 已发布剖面保持不变':''));
    if(data.done) toast('流已送完');
  }catch(e){ toast('模拟器错误：'+e.message,true); }
}
async function refreshSimStatus(){
  try{
    const s=await api('/api/simulator/status',{method:'GET'});
    $('simStatus').textContent='已投递 '+s.delivered+' / '+s.total+'（剩余 '+s.remaining+'）';
  }catch(e){}
}

// ---------- init ----------
(async function init(){
  await loadFlags();
  // allow query string ?id=
  const q=new URLSearchParams(location.search);
  if(q.get('id')) $('sid').value=q.get('id');
  try{ await loadSounding(); }catch(e){ /* empty db */ render(); }
  refreshSimStatus();
})();
