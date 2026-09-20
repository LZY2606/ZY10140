let state = { soundings: [], current: null, draft: null, selectedPoint: null, branchFilter: 'primary' };
const SVGNS='http://www.w3.org/2000/svg';
const PHASE_COLOR={ascent:'#4ea1ff',float:'#f0b93b',descent:'#ff7a59',terminated:'#9a6bff'};
const FLAG_SEV={info:'info',warning:'warning',error:'error'};

const $=s=>document.querySelector(s);
function api(path,opts){
  return fetch(path,Object.assign({headers:{'Content-Type':'application/json'}},opts)).then(async r=>{
    const t=await r.text(); const j=t?JSON.parse(t):{};
    if(!r.ok) throw Object.assign(new Error(j.error||r.statusText),{status:r.status,body:j});
    return j;
  });
}
const fmtT=t=>t?new Date(t).toISOString().substring(11,19):'';
const fmt=(v,d=1)=>v==null?'—':Number(v).toFixed(d);

async function loadSoundings(){
  const j=await api('/api/soundings'); state.soundings=j.soundings;
  const sel=$('#soundingSelect'); sel.innerHTML='';
  const list=$('#soundingList'); list.innerHTML='';
  for(const s of state.soundings){
    const o=document.createElement('option'); o.value=s.id; o.textContent=`#${s.id} ${s.name} r${s.revision}`;
    sel.appendChild(o);
    const d=document.createElement('div'); d.className='sounding'+(state.current==s.id?' active':'');
    d.innerHTML=`<b>#${s.id} ${s.name}</b><div class="muted">rev ${s.revision} · 已签字 r${s.published_revision||0}${s.published_at?' · '+fmtT(s.published_at):''}</div>`;
    d.onclick=()=>selectSounding(s.id); list.appendChild(d);
  }
  if(state.current && state.soundings.some(s=>s.id===state.current)){sel.value=state.current;}
}
async function selectSounding(id){ state.current=id; await loadSoundings(); await loadDraft(); }

async function loadDraft(){
  if(!state.current) return;
  state.draft=await api(`/api/soundings/${state.current}/draft`);
  render();
}

function render(){
  const d=state.draft; if(!d) return;
  $('#revInfo').textContent=`草稿 r${d.revision} · 已签字剖面 r${d.published_revision||0}（不可变）`;
  $('#signedInfo').textContent=d.published_revision?`签字基线 r${d.published_revision}`:'尚未签字';
  drawTimeline(); drawProfile(); renderTransitions(); renderConflicts();
  renderBranchSelect(); renderLevels(); renderFlags();
}

function allPoints(){ const a=state.draft.assembly; return a.points.concat(a.alt_points||[]); }

function drawTimeline(){
  const svg=$('#timeline'); svg.innerHTML='';
  const pts=state.draft.assembly.points; const alt=state.draft.assembly.alt_points||[];
  if(!pts.length) return;
  const W=svg.clientWidth||900,H=180,L=44,R=12,T=14,B=24;
  const xs=pts.map(p=>p.seq); const x0=Math.min(...xs),x1=Math.max(...xs);
  const ps=pts.filter(p=>p.pressure_hpa).map(p=>p.pressure_hpa);
  const y0=Math.min(...ps),y1=Math.max(...ps);
  const X=v=>L+(v-x0)/(x1-x0||1)*(W-L-R);
  const Y=v=>T+(y1-v)/(y1-y0||1)*(H-T-B);
  line(svg,L,T,L,H-B,'#2a3650');line(svg,L,H-B,W-R,H-B,'#2a3650');
  for(let g=0;g<4;g++){const v=y0+(y1-y0)*g/3; text(svg,4,Y(v)+4,v.toFixed(0),'#8b96ad');}
  const draw=(arr,cls)=>{
    for(let i=1;i<arr.length;i++){
      if(arr[i-1].pressure_hpa&&arr[i].pressure_hpa)
        line(svg,X(arr[i-1].seq),Y(arr[i-1].pressure_hpa),X(arr[i].seq),Y(arr[i].pressure_hpa),
          PHASE_COLOR[arr[i].phase]||'#888',cls?1.2:2,cls);
    }
  };
  draw(pts,false); draw(alt,true);
  for(const p of pts){
    const c=document.createElementNS(SVGNS,'circle');
    c.setAttribute('cx',X(p.seq));c.setAttribute('cy',Y(p.pressure_hpa||y0));c.setAttribute('r',p===state.selectedPoint?5:3);
    c.setAttribute('fill',PHASE_COLOR[p.phase]);
    if(p.flags&&p.flags.some(f=>f.severity==='error')) c.setAttribute('stroke','#ff5d5d');
    c.style.cursor='pointer';
    c.onclick=e=>{state.selectedPoint=p;renderFlags();};
    svg.appendChild(c);
    if(p.status==='BURST') text(svg,X(p.seq)-10,T+10,'爆裂','#ff7a59');
  }
  // click background to mark descent interval (selection of two points)
  text(svg,L,H-6,'序号 →（点击点查看标志依据；右侧操作可把区间标为下降/结冰）','#8b96ad');
}

function drawProfile(){
  const svg=$('#profile'); svg.innerHTML='';
  const pts=state.draft.assembly.points; const alt=state.draft.assembly.alt_points||[];
  if(!pts.length) return;
  const W=svg.clientWidth||900,H=260,L=48,R=12,T=12,B=26;
  const have=pts.filter(p=>p.pressure_hpa&&p.alt_m);
  if(!have.length){text(svg,40,40,'无有效压力/高度','#8b96ad');return;}
  const amin=Math.min(...have.map(p=>p.alt_m)),amax=Math.max(...have.map(p=>p.alt_m));
  const pmin=Math.min(...have.map(p=>p.pressure_hpa)),pmax=Math.max(...have.map(p=>p.pressure_hpa));
  const Y=v=>T+(amax-v)/(amax-amin||1)*(H-T-B);
  const Xp=v=>L+(Math.log(pmax)-Math.log(v))/(Math.log(pmax)-Math.log(pmin)||1)*(W-L-R);
  line(svg,L,T,L,H-B,'#2a3650');line(svg,L,H-B,W-R,H-B,'#2a3650');
  for(let g=0;g<=4;g++){const a=amin+(amax-amin)*g/4; line(svg,L-3,Y(a),L,Y(a),'#2a3650');text(svg,4,Y(a)+4,(a/1000).toFixed(1)+'k','#8b96ad');}
  for(const Lv of [1000,850,700,500,300,200,100,50,20,10]){
    if(Lv>=pmin&&Lv<=pmax){ line(svg,Xp(Lv),T,Xp(Lv),H-B,'#1b2538'); text(svg,Xp(Lv)-10,H-8,Lv,'#5c6b8a'); }
  }
  const draw=(arr,cls)=>{
    for(let i=1;i<arr.length;i++){
      const a=arr[i-1],b=arr[i];
      if(a.pressure_hpa&&b.pressure_hpa&&a.alt_m&&b.alt_m&&!a.gps_gap&&!b.gps_gap)
        line(svg,Xp(a.pressure_hpa),Y(a.alt_m),Xp(b.pressure_hpa),Y(b.alt_m),PHASE_COLOR[b.phase]||'#888',cls?1.2:2,cls);
    }
  };
  draw(pts,false); draw(alt,true);
  text(svg,L,H-8+14,'对数气压 → / 高度 ↑（上升与下降分离，不做跨分支平均）','#8b96ad');
}

function line(svg,x1,y1,x2,y2,stroke,w=1,cls=false){
  const l=document.createElementNS(SVGNS,'line');
  l.setAttribute('x1',x1);l.setAttribute('y1',y1);l.setAttribute('x2',x2);l.setAttribute('y2',y2);
  l.setAttribute('stroke',stroke);l.setAttribute('stroke-width',w);
  if(cls) l.setAttribute('class','alt');
  svg.appendChild(l);
}
function text(svg,x,y,s,fill){
  const t=document.createElementNS(SVGNS,'text');
  t.setAttribute('x',x);t.setAttribute('y',y);t.setAttribute('fill',fill||'#8b96ad');
  t.setAttribute('font-size',10);t.textContent=s;svg.appendChild(t);
}

function renderTransitions(){
  const tb=$('#transTable').querySelector('tbody'); tb.innerHTML='';
  for(const [i,tr] of (state.draft.assembly.transitions||[]).entries()){
    const r=document.createElement('tr');
    r.innerHTML=`<td>${i+1}</td><td class="ph-${tr.from}">${tr.from}</td>
      <td class="ph-${tr.to}">${tr.to}</td><td>${tr.reason}</td>
      <td>${tr.source==='manual'?'<b style="color:#3ecf8e">人工</b>':'自动'}</td>
      <td>${tr.from_seq}→${tr.to_seq}</td>`;
    tb.appendChild(r);
  }
}

function renderConflicts(){
  const box=$('#conflicts'); box.innerHTML='';
  const confs=(state.draft.assembly.conflicts||[]).filter(c=>c.candidates.length>1);
  if(!confs.length){ box.innerHTML='<div class="muted">无重复序号冲突</div>'; return; }
  for(const c of confs){
    const d=document.createElement('div'); d.style.cssText='border:1px solid #5a4622;border-radius:8px;padding:6px;margin-bottom:6px';
    let h=`<div><b>序号 ${c.seq}</b> · ${c.candidates.length} 个载荷 · ${fmtT(c.time)}</div>`;
    c.candidates.forEach((cd,i)=>{
      const chosen=c.chosen_candidate_id===cd.candidate_id;
      h+=`<div style="margin:4px 0"><label>
        <input type="radio" name="cf${c.seq}" value="${cd.candidate_id}" ${chosen?'checked':''}>
        候选 ${cd.candidate_id}：P=${fmt(cd.packet.pressure_hpa)} T=${fmt(cd.packet.temp_c)} 高度=${fmt(cd.packet.alt_m,0)}
        ${cd.packet.late?'<span class="tag warning">迟到</span>':''} ${chosen&&c.keep_both?'':chosen?'<span class="tag info">已选</span>':''}
      </label></div>`;
    });
    h+=`<div><label><input type="checkbox" class="keepboth" ${c.keep_both?'checked':''}> 保留两个候选轨迹（不签字插值）</label></div>
      <div class="row"><button data-seq="${c.seq}" class="resolveBtn">保存可信版本判定</button></div>`;
    d.innerHTML=h; box.appendChild(d);
    d.querySelector('.resolveBtn').onclick=async ()=>{
      const keep=d.querySelector('.keepboth').checked;
      const cand=keep?-1:Number(d.querySelector(`input[name=cf${c.seq}]:checked`).value);
      await submitOverride({kind:'resolve',seq:c.seq,candidate_id:cand,note:`重复包判定 seq=${c.seq}`});
    };
  }
}

function renderBranchSelect(){
  const sel=$('#branchSelect'); const cur=state.branchFilter; sel.innerHTML='';
  const opt0=document.createElement('option'); opt0.value='primary'; opt0.textContent='主上升分支（全部标准层）'; sel.appendChild(opt0);
  for(const b of state.draft.assembly.branches||[]){
    const o=document.createElement('option'); o.value=b.id;
    o.textContent=`分支#${b.id} ${b.kind} [${b.start_seq}..${b.end_seq}] ${b.source==='manual'?'(人工)':''}`;
    sel.appendChild(o);
  }
  sel.value=cur;
  sel.onchange=()=>{state.branchFilter=sel.value;renderLevels();};
}

function renderLevels(){
  const tb=$('#levelTable').querySelector('tbody'); tb.innerHTML='';
  const ls=(state.draft.assembly.levels||[]).filter(l=>
    state.branchFilter==='primary'?l.primary:l.branch_id===Number(state.branchFilter));
  for(const l of ls){
    const miss=(l.missing||[])[0];
    const st=l.exact?'观测值':'插值';
    const statusHtml=[];
    if(l.ambiguous) statusHtml.push('<span class="tag warning">冲突待定</span>');
    if(miss) statusHtml.push(`<span class="miss" title="${(l.missing||[]).map(m=>m.field+':'+m.reason).join('&#10;')}">缺失:${miss.reason}</span>`);
    statusHtml.push(`<span class="tag info">${st}</span>`);
    const r=document.createElement('tr');
    r.innerHTML=`<td>${l.pressure}</td><td>${fmt(l.alt_m,0)}</td><td>${fmt(l.temp_c)}</td>
      <td>${fmt(l.rh_pct)}</td><td>${statusHtml.join(' ')}</td>`;
    tb.appendChild(r);
  }
}

function renderFlags(){
  const box=$('#flagList');
  if(!state.selectedPoint){
    // summarized flag counts over the flight
    const counts={};
    for(const p of state.draft.assembly.points) for(const f of (p.flags||[])){
      counts[f.code]=(counts[f.code]||{n:0,meta:f}); counts[f.code].n++;
    }
    box.innerHTML=Object.values(counts).map(x=>
      `<div><span class="tag ${FLAG_SEV[x.meta.severity]}">${x.meta.label}</span> ×${x.n}<div class="muted">${x.meta.reason}</div></div>`).join('')||'<span class="muted">无标志</span>';
    return;
  }
  const p=state.selectedPoint;
  box.innerHTML=`<div>序号 ${p.seq} · ${fmtT(p.time)} · <span class="ph-${p.phase}">${p.phase}</span>(${p.phase_source})</div>`+
    (p.flags||[]).map(f=>`<div style="margin-top:6px"><span class="tag ${FLAG_SEV[f.severity]}">${f.label}</span>
      <span class="muted">[${f.source}]</span><div>${f.reason}</div></div>`).join('')||
    '<span class="muted">该点无质量标志</span>';
}

function openDialog(title,bodyHtml,onOk){
  $('#dlgTitle').textContent=title; $('#dlgBody').innerHTML=bodyHtml;
  const btn=$('#dlgOk');
  const h=()=>{btn.onclick=async()=>{try{await onOk();dlg.close();}catch(e){alert((e.body&&e.body.error)||e.message);}};};
  dlg.showModal(); h();
}

async function submitOverride(o){
  o.author=$('#author').value||'analyst';
  o.expected_revision=state.draft.revision;
  if(!o.start_time){ const p=state.selectedPoint; if(p)o.start_time=p.time; }
  try{
    const j=await api(`/api/soundings/${state.current}/overrides`,{method:'POST',body:JSON.stringify(o)});
    state.draft=j.draft; render();
    if(j.affected_overrides&&j.affected_overrides.length){
      const r=j.affected_time_range;
      alert(`并发判定提示：已有 ${j.affected_overrides.length} 条已签字判定覆盖该时间段（${r?fmtT(r.start)+' ~ '+fmtT(r.end):''}），请确认。已基于最新版本保存。`);
    }
  }catch(e){
    if(e.status===409){ alert('版本冲突：另一位分析员已修改该探空。\n受影响时间段：'+JSON.stringify(e.body.affected_time_range)); await loadDraft(); return; }
    throw e;
  }
}

$('#newBtn').onclick=async()=>{
  const dev=prompt('设备序号','RSN-'+Math.floor(Math.random()*9000+1000)); if(!dev)return;
  const j=await api('/api/soundings',{method:'POST',body:JSON.stringify({device_id:dev,name:dev+' 探空'})});
  await loadSoundings(); await selectSounding(j.id);
};
$('#simBtn').onclick=async()=>{
  if(!state.current){ alert('请先选择或新建探空'); return; }
  const btn=$('#simBtn'); btn.disabled=true; btn.textContent='灌入中…';
  try{ await api(`/api/soundings/${state.current}/simulate`,{method:'POST',body:JSON.stringify({batch_size:25})}); await loadDraft(); }
  finally{ btn.disabled=false; btn.textContent='灌入模拟乱序包'; }
};
$('#soundingSelect').onchange=e=>selectSounding(Number(e.target.value));
$('#publishBtn').onclick=async()=>{
  if(!state.current)return;
  const author=$('#author').value;
  if(!author){alert('请填写签字人工号');return;}
  const note=prompt('发布说明（可空）','')||'';
  try{
    await api(`/api/soundings/${state.current}/publish`,{method:'POST',body:JSON.stringify({author,note})});
    alert('剖面已发布并冻结（标准层全部原子写入）'); await loadSoundings(); await loadDraft();
  }catch(e){ alert('发布失败：'+((e.body&&e.body.error)||e.message)); }
};

// right-click-like action bar for range decisions
document.addEventListener('keydown',e=>{
  if(!state.current||!state.selectedPoint)return;
  const p=state.selectedPoint;
  if(e.key==='d') openDialog('把区间标为下降',
    `<div class="muted">起点：${p.time}</div>
     <label>结束序号 <input id="endSeq" type="number" value="${p.seq+5}"></label>
     <div class="row"><label><input type="checkbox" id="endAll"> 直到记录结束</label></div>`,
    async()=>{
      const endSeq=Number($('#endSeq').value);
      const all=state.draft.assembly.points; const end=all.find(q=>q.seq===endSeq)||all[all.length-1];
      await submitOverride({kind:'phase',phase:'descent',start_time:p.time,end_time:$('#endAll').checked?null:end.time,note:'人工标注下降段'});
    });
  if(e.key==='i') openDialog('圈定传感器结冰区间',
    `<div class="muted">起点：${p.time}</div>
     <label>结束序号 <input id="endSeq" type="number" value="${p.seq+3}"></label>`,
    async()=>{
      const endSeq=Number($('#endSeq').value);
      const end=state.draft.assembly.points.find(q=>q.seq===endSeq)||p;
      await submitOverride({kind:'icing',icing:true,start_time:p.time,end_time:end.time,note:'人工圈定结冰区间'});
    });
});
window.addEventListener('resize',()=>{ if(state.draft){drawTimeline();drawProfile();} });
loadSoundings();
