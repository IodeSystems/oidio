package verify

// page is the workbench, inline so the binary is the whole tool — no asset
// directory to install beside it and no CDN to be offline from.
//
// One persistent mode. Every action is a single key from the list — including
// join, which needs no target beyond "the previous turn" and so needs no editor
// to host it. Only SPLIT has a transient state, because a cut is the one action
// that needs an argument: which boundary. `s` raises a caret, the arrows move
// it, enter commits.
//
// An earlier version put join and split behind an editor mode. That was one
// keystroke of ceremony on the commonest repair there is — a boundary landing
// mid-sentence, leaving one speaker's last word in the next speaker's turn —
// and ceremony on the common case is what makes a labelling pass unfinishable.
//
// The shortcuts are on screen permanently, and they change with the mode. A
// keyboard-driven tool whose keys must be memorised from a terminal message is
// one nobody uses twice. Speakers show their names, never bare uuids: "is this
// 3f2a or d4df" is not a question a person can answer.
const page = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>oidio verify</title>
<style>
:root{--bg:#0f1115;--fg:#e6e6e6;--dim:#8a8f98;--line:#242832;--accent:#4c8dff;--ok:#3fb950;--warn:#d29922;--panel:#161a22}
@media(prefers-color-scheme:light){:root{--bg:#fff;--fg:#1a1a1a;--dim:#5c6370;--line:#e3e3e3;--accent:#0b62d6;--ok:#1a7f37;--warn:#9a6700;--panel:#f6f7f9}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.55 ui-sans-serif,system-ui,sans-serif}
header{position:sticky;top:0;background:var(--bg);border-bottom:1px solid var(--line);padding:10px 16px;z-index:5}
.row{display:flex;gap:14px;align-items:center;flex-wrap:wrap}
.pill{border:1px solid var(--line);border-radius:999px;padding:2px 10px;font-size:12px;color:var(--dim)}
.bar{height:6px;background:var(--line);border-radius:3px;overflow:hidden;flex:1;min-width:140px}
.bar i{display:block;height:100%;background:var(--ok);width:0;transition:width .2s}
#roster{display:flex;gap:6px;flex-wrap:wrap;margin-top:8px}
.sp{border:1px solid var(--line);border-radius:6px;padding:3px 9px;font-size:12px;cursor:pointer;white-space:nowrap}
.sp.on{border-color:var(--accent);color:var(--accent)}
.sp b{color:var(--accent);margin-right:6px;font-variant-numeric:tabular-nums}
.sp .t{color:var(--dim);margin-left:6px}
main{padding:10px 16px 180px}
.seg{display:grid;grid-template-columns:58px 130px 1fr;gap:10px;padding:7px 10px;border-bottom:1px solid var(--line);cursor:pointer;border-left:3px solid transparent}
.seg:hover{background:rgba(127,127,127,.07)}
.seg.cur{background:rgba(76,141,255,.13);border-left-color:var(--accent)}
.seg.done{border-left-color:var(--ok)}
.seg.unclear{border-left-color:var(--warn)}
.seg.cont .spk{color:var(--dim)}
.t{color:var(--dim);font-variant-numeric:tabular-nums;font-size:12px}
.spk{font-size:12px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.moved{color:var(--warn)}
.txt{white-space:pre-wrap}
.txt w{cursor:pointer;border-radius:3px}
.txt w:hover{background:rgba(76,141,255,.25)}
.seg.playing .txt{font-weight:650}
.txt w.now{text-decoration:underline;text-decoration-thickness:2px;text-underline-offset:3px;text-decoration-color:var(--accent)}
#edit{padding:12px;margin:8px 0;border:1px solid var(--accent);border-radius:8px;background:var(--panel)}
#edit h4{margin:0 0 8px;font-size:12px;color:var(--dim);font-weight:600;letter-spacing:.04em;text-transform:uppercase}
#words{font-size:15px;line-height:2.2}
#words span.w{padding:1px 2px;border-radius:3px}
#words span.w:hover{background:rgba(76,141,255,.25)}
#words span.gap{display:inline-block;width:3px;height:1.2em;vertical-align:-.2em;margin:0 1px}
#words span.gap.on{background:var(--accent);box-shadow:0 0 0 1px var(--accent)}
#help{position:fixed;bottom:0;left:0;right:0;background:var(--panel);border-top:1px solid var(--line);padding:10px 16px;font-size:12.5px;z-index:6}
#help h5{margin:0 0 6px;font-size:11px;letter-spacing:.06em;text-transform:uppercase;color:var(--dim)}
#help .grid{display:flex;gap:10px 18px;flex-wrap:wrap}
#help span{color:var(--dim);white-space:nowrap}
kbd{border:1px solid var(--line);border-radius:4px;padding:1px 6px;font:12px ui-monospace,monospace;color:var(--fg);background:var(--bg)}
.mode{color:var(--accent);font-weight:600}
</style></head><body>
<header>
  <div class="row">
    <b>oidio verify</b>
    <span class="pill" id="file"></span>
    <div class="bar"><i id="prog"></i></div>
    <span class="pill" id="count">0 / 0</span>
    <span class="pill" id="saved">saved</span>
    <span class="pill" id="undos">0 undo</span>
    <label class="pill" style="display:flex;gap:8px;align-items:center">
      <span id="vollbl">vol 100%</span>
      <input id="vol" type="range" min="0" max="300" step="5" value="100" style="width:110px">
    </label>
  </div>
  <div id="roster"></div>
</header>
<main id="list"></main>
<div id="help"></div>
<audio id="au" preload="metadata"></audio>
<script>
const A=document.getElementById('au'), L=document.getElementById('list'), H=document.getElementById('help');

// Volume goes past 100%.
//
// An <audio> element caps at 1.0, which is the source level — and the source is
// a room recording where someone across the table is genuinely quiet. Levelling
// closes most of that gap, but not all of it, and "turn it up" should remain
// available without re-encoding. Routing through a GainNode allows 3x.
//
// The context is created on first play because browsers refuse to start one
// without a user gesture, and an AudioContext built at load would sit suspended
// and silently mute everything.
let actx=null,gain=null;
function audioGraph(){
  if(actx) return;
  try{
    actx=new (window.AudioContext||window.webkitAudioContext)();
    gain=actx.createGain();
    actx.createMediaElementSource(A).connect(gain).connect(actx.destination);
    gain.gain.value=vol()/100;
  }catch(e){ actx=null; gain=null; }
}
function vol(){ return +(localStorage.getItem('oidio-vol')||100) }
function setVol(v){
  v=Math.max(0,Math.min(300,Math.round(v/5)*5));
  localStorage.setItem('oidio-vol',v);
  const el=document.getElementById('vol'); if(el) el.value=v;
  const lb=document.getElementById('vollbl'); if(lb) lb.textContent='vol '+v+'%';
  if(gain) gain.gain.value=v/100;
  // Below 100 the element's own volume is enough and works before the graph
  // exists; above it, only the gain node can help.
  A.volume=Math.min(1,v/100);
}
let segs=[], speakers={}, order=[], cur=0, stopAt=null, mode='list', caret=1, hist=[];

fetch('api/data').then(r=>r.json()).then(d=>{
  document.getElementById('file').textContent=d.audio;
  A.src='audio'; speakers=d.speakers||{}; segs=d.segments||[];
  segs.sort((a,b)=>a.start-b.start);
  // Fixes pairs left by an earlier session, before any new edit is made.
  if(coalesce()) save();
  roster(); render(); focus(0); help();
  setVol(vol());
  document.getElementById('vol').oninput=e=>{ audioGraph(); setVol(+e.target.value); e.target.blur() };
});

// A uuid is not a name a person can hold in their head, so an unnamed speaker
// gets a positional one instead. The roster order is frozen, so "speaker 3"
// stays speaker 3 for the whole pass.
function labelOf(u){
  if(!u) return '(none)';
  if(speakers[u]) return speakers[u];
  const i=order.indexOf(u);
  return 'speaker '+(i<0?'?':i+1);
}

// The roster order is FROZEN after the first build. It was previously re-sorted
// by speaking time on every render, so assigning a turn could renumber every
// speaker mid-pass — the digit that meant "Clerk" a second ago now means someone
// else, and the muscle memory the number keys exist for is destroyed. A stable
// wrong-ish order beats an optimal moving one.
//
// New speakers append rather than insert, for the same reason.
function buildOrder(){
  const secs=secsAll();
  const seen=new Set(order);
  const fresh=Object.keys(secs).filter(u=>!seen.has(u)).sort((a,b)=>secs[b]-secs[a]);
  order=order.filter(u=>secs[u]!==undefined).concat(fresh);
}
function secsAll(){
  const secs={};
  segs.forEach(s=>{ if(s.speaker) secs[s.speaker]=(secs[s.speaker]||0)+(s.end-s.start) });
  return secs;
}

// keyHint gives each speaker the SHORTEST substring that identifies it uniquely
// among the others — matched anywhere in the name, not just the front, because
// "Alice" and "Anne" share their first letter and the distinguishing character
// is what a person actually reaches for.
function keyHints(){
  const labels=order.map(u=>(speakers[u]||u.slice(0,8)).toLowerCase());
  return labels.map((lab,i)=>{
    for(let len=1;len<=lab.length;len++){
      for(let st=0;st+len<=lab.length;st++){
        const sub=lab.slice(st,st+len);
        if(labels.filter((o,j)=>j!==i&&o.includes(sub)).length===0) return sub;
      }
    }
    return lab;
  });
}

function roster(){
  buildOrder();
  const secs=secsAll(), hints=keyHints();
  const R=document.getElementById('roster'); R.innerHTML='';
  order.forEach((u,i)=>{
    const el=document.createElement('span');
    el.className='sp'+(segs[cur]&&segs[cur].speaker===u?' on':'');
    const lab=labelOf(u), h=hints[i];
    const at=lab.toLowerCase().indexOf(h);
    const shown = at<0 ? esc(lab)
      : esc(lab.slice(0,at))+'<u>'+esc(lab.slice(at,at+h.length))+'</u>'+esc(lab.slice(at+h.length));
    el.innerHTML=(i<9?'<b>'+(i+1)+'</b>':'')+shown+'<span class=t>'+Math.round(secs[u]||0)+'s</span>';
    el.onclick=()=>assign(u);
    R.appendChild(el);
  });
}
function esc(s){const d=document.createElement('div');d.textContent=s;return d.innerHTML}

function render(){
  L.innerHTML='';
  segs.forEach((s,i)=>{
    const prev=segs[i-1];
    const sameAsPrev=prev&&prev.speaker&&prev.speaker===s.speaker;
    const d=document.createElement('div');
    d.className='seg'+(s.confirmed?' done':'')+(s.unclear?' unclear':'')+(sameAsPrev?' cont':'');
    d.innerHTML='<div class=t>'+fmt(s.start)+'</div><div class="spk'+(s.moved?' moved':'')+'"></div><div class=txt></div>';
    d.children[1].textContent=sameAsPrev?'⤷ '+labelOf(s.speaker):labelOf(s.speaker);
    d.children[2].innerHTML=(s.corrected?'✎ ':'')+wordSpans(s);
    d.onclick=ev=>{
      const w=ev.target.closest('w');
      focus(i);
      if(w){ A.currentTime=parseFloat(w.dataset.t); stopAt=s.end; A.play(); }
      else play();
    };
    L.appendChild(d);
    if(i===cur&&mode==='split') L.appendChild(editor(s));
    if(i===cur&&mode==='text') L.appendChild(textEditor(s));
    if(i===cur&&mode==='pick') L.appendChild(pickerBox());
  });
  stats(); markCur();
}
function markCur(){
  [...L.querySelectorAll('.seg')].forEach((c,i)=>c.classList.toggle('cur',i===cur));
  roster();
}
function fmt(x){const m=Math.floor(x/60),s=Math.floor(x%60);return m+':'+String(s).padStart(2,'0')}
function stats(){
  const n=segs.filter(s=>s.confirmed||s.unclear).length;
  document.getElementById('count').textContent=n+' / '+segs.length;
  document.getElementById('prog').style.width=(segs.length?100*n/segs.length:0)+'%';
  document.getElementById('undos').textContent=hist.length+' undo';
}

// The editor shows the turn as words with a caret BETWEEN them, because a split
// is a claim about a boundary, not about a word.
function editor(s){
  const box=document.createElement('div'); box.id='edit';
  const w=words(s.text);
  let html='<h4>split — arrows move the cut, enter commits, esc cancels</h4><div id=words>';
  w.forEach((x,i)=>{
    if(i>0) html+='<span class="gap'+(caret===i?' on':'')+'" data-g="'+i+'"></span>';
    html+='<span class=w data-t="'+wordTime(s,i,w.length).toFixed(3)+'">'+esc(x)+'</span>';
  });
  html+='</div>';
  box.innerHTML=html;
  box.querySelectorAll('.gap').forEach(g=>g.onclick=e=>{e.stopPropagation();caret=+g.dataset.g;render()});
  // Clicking a word in the split view moves the cut to just BEFORE it and
  // auditions from there. Clicking is a claim about where the next speaker
  // starts, and the word you click is the first word they say — so the boundary
  // belongs in front of it, and hearing it from that point is how you check.
  box.querySelectorAll('.w').forEach((el,wi)=>el.onclick=e=>{
    e.stopPropagation();
    const n=words(s.text).length;
    caret=Math.max(1,Math.min(n-1,wi));
    render();
    if(el.dataset.t){ A.currentTime=parseFloat(el.dataset.t); stopAt=s.end; A.play(); }
  });
  box.querySelectorAll('.w').forEach(el=>el.style.cursor='pointer');
  return box;
}
function words(t){ return (t||'').split(/\s+/).filter(Boolean) }

// Word times are ESTIMATED by position within the turn, because the transcript
// carries no per-word timestamps — Parakeet produces them, but the API does not
// expose them yet. Good enough to click into a long turn and to follow along;
// not good enough to trust to the syllable. Exposing words[] would make this
// exact and is the same gap that blocks OpenAI timestamp_granularities.
function wordTime(s,i,n){ return s.start+(s.end-s.start)*(n?i/n:0) }
function wordSpans(s){
  const w=words(s.text);
  return w.map((x,i)=>'<w data-t="'+wordTime(s,i,w.length).toFixed(3)+'">'+esc(x)+'</w>').join(' ');
}

// The speaker picker: type until one name is left, and it commits itself.
//
// Number keys are fast but only work while you can see which number is which,
// and they run out at nine. Typing the distinguishing letters needs no lookup
// and no memory of position — "l" is Alice because nothing else has an l — and
// it degrades gracefully as speakers are added.
//
// Opening it with the current speaker preselected means enter-enter is still
// "confirm as-is", so the commonest action stays two keystrokes and never
// requires reading anything.
let pick='', pickSel=0;
function pickerMatches(){
  if(!pick) return order.slice();
  return order.filter(u=>labelOf(u).toLowerCase().includes(pick));
}
function pickerBox(){
  const box=document.createElement('div'); box.id='edit';
  const hints=keyHints(), ms=pickerMatches();
  const naming = pick && ms.length===0;
  let html='<h4>speaker — '+(naming
      ? 'no match: enter NAMES this speaker &ldquo;'+esc(pick)+'&rdquo;'
      : 'type to narrow, enter accepts, esc cancels')+'</h4>';
  html+='<div style="font-size:15px;line-height:2">';
  html+='<div style="margin-bottom:6px">typed: <b style="color:var(--accent)">'+
        (pick?esc(pick):'<span style="opacity:.4">…</span>')+'</b></div>';
  if(naming){
    html+='<div style="color:var(--warn)">↵ rename '+esc(labelOf(segs[cur].speaker))+
          ' → <b>'+esc(pick)+'</b></div>';
  } else {
    ms.forEach((u,mi)=>{
      const i=order.indexOf(u), lab=labelOf(u);
      const h=hints[i], at=lab.toLowerCase().indexOf(h);
      const shown = at<0?esc(lab):esc(lab.slice(0,at))+'<u>'+esc(lab.slice(at,at+h.length))+'</u>'+esc(lab.slice(at+h.length));
      html+='<div style="'+(mi===pickSel?'background:rgba(76,141,255,.18);border-radius:4px;padding:0 4px':'')+'">'+
        (mi===pickSel?'▸ ':'&nbsp;&nbsp;')+(i<9?'<kbd>'+(i+1)+'</kbd> ':'')+shown+
        (u===segs[cur].speaker?' <span style="color:var(--ok)">← current</span>':'')+'</div>';
    });
  }
  box.innerHTML=html+'</div>';
  return box;
}

// Correcting the text is what makes this ground truth for the WORDS as well as
// the speakers. Without it the truth file can score attribution but not
// transcription, and the recogniser would only ever be measured against its own
// output.
//
// The edit is marked as corrected rather than silently replacing the text,
// because a turn nobody retyped is not evidence about wording — it is just the
// recogniser's guess that went unchallenged.
function textEditor(s){
  const box=document.createElement('div'); box.id='edit';
  box.innerHTML='<h4>correct the transcript — enter saves, esc cancels</h4>';
  const inp=document.createElement('textarea');
  inp.style.cssText='width:100%;min-height:4.5em;background:var(--bg);color:var(--fg);border:1px solid var(--line);border-radius:6px;padding:8px;font:inherit';
  inp.value=s.text||'';
  inp.onkeydown=e=>{
    e.stopPropagation();
    if(e.key==='Enter'&&!e.shiftKey){
      e.preventDefault();
      const v=inp.value.trim();
      if(v!==(s.text||'')){ push(); s.text=v; s.corrected=true; save(); }
      mode='list'; render(); help();
    }
    if(e.key==='Escape'){ mode='list'; render(); help(); }
  };
  box.appendChild(inp);
  setTimeout(()=>{inp.focus();inp.setSelectionRange(inp.value.length,inp.value.length)},0);
  return box;
}

// contigGap is how close two turns must be to count as one. The engine emits no
// adjacent same-speaker turns at all — segmentsFromWords groups by speaker run —
// so any that appear were made HERE, by reassigning a turn to match its
// neighbour.
const contigGap=0.05;

// coalesce merges adjacent turns that are the same speaker, contiguous, and BOTH
// confirmed.
//
// Contiguous matters: merging across a real pause would claim speech during
// silence, which scores as a false alarm and quietly degrades the ground truth
// this whole pass exists to produce. Same speaker either side of a five-second
// gap is two turns, not one.
//
// Both-confirmed matters for a subtler reason: a split produces two adjacent
// turns with the SAME speaker by construction, and merging on that alone would
// undo the split the instant it was made. Requiring an affirmative ruling on
// each side means a fresh split survives, and the halves only rejoin if you
// actually say they are the same person.
function coalesce(){
  let did=false;
  for(let i=segs.length-1;i>0;i--){
    const a=segs[i-1], b=segs[i];
    if(!a.speaker||a.speaker!==b.speaker) continue;
    if(b.start-a.end>contigGap) continue;
    if(!a.confirmed||!b.confirmed) continue;
    if(a.unclear||b.unclear) continue;
    a.end=b.end;
    a.text=(a.text+' '+b.text).replace(/\s+/g,' ').trim();
    segs.splice(i,1);
    if(cur>=i) cur=Math.max(0,cur-1);
    did=true;
  }
  return did;
}

function focus(i){
  if(i<0||i>=segs.length) return;
  cur=i; caret=1;
  markCur();
  scrollToCur();
}

// Centring a turn is right until the turn is taller than the screen, and then it
// is exactly wrong: the middle is shown and the OPENING — the words that say who
// started talking — is scrolled off above. Word-level attribution produces turns
// of 130 seconds when one speaker holds the floor, so this is common, not an
// edge case.
//
// Measured against the band actually visible: the header is sticky and the key
// bar is fixed, so both cover content that getBoundingClientRect still counts as
// on-screen.
function scrollToCur(){
  const el=L.querySelectorAll('.seg')[cur];
  if(!el) return;
  const head=document.querySelector('header').offsetHeight;
  const foot=document.getElementById('help').offsetHeight;
  const band=window.innerHeight-head-foot;
  const r=el.getBoundingClientRect();
  // An open editor belongs to the turn even though it is a sibling node, so it
  // counts toward the height that decides top-align versus centre.
  const ed=document.getElementById('edit');
  const h=r.height+(ed&&ed.previousElementSibling===el?ed.getBoundingClientRect().height+8:0);
  const pad=8;
  const top = h>band-pad*2
    ? window.scrollY+r.top-head-pad          // too tall: pin the opening
    : window.scrollY+r.top-head-(band-h)/2;  // fits: centre it in the band
  window.scrollTo({top:Math.max(0,top),behavior:'smooth'});
}
function play(){ const s=segs[cur]; if(!s) return; audioGraph(); A.currentTime=s.start; stopAt=s.end; A.play(); }

// resume decides where to listen after an edit, from where the playhead already
// is. Three cases, and each exists because the alternative replays something you
// just heard.
//
// INSIDE the turn — keep going, only move the stop point. The reason you joined
// is that the words carry on across the old boundary, and the only way to hear
// that is to listen THROUGH it.
//
// AT OR PAST the end — you already heard this turn to its finish, which is
// usually WHY you just edited it. Restarting would replay the whole thing, and
// stopping would break the rhythm, so move on to the next turn and keep playing.
// That is what makes a pass feel like listening to a conversation rather than
// operating a machine.
//
// BEFORE the start — no position worth keeping; seek and play.
function resume(){
  const s=segs[cur]; if(!s) return;
  if(A.currentTime>=s.start&&A.currentTime<s.end-0.05){
    stopAt=s.end;
    if(A.paused){audioGraph();A.play()}
    return;
  }
  if(A.currentTime>=s.end-0.05&&cur<segs.length-1){
    focus(cur+1); play();
    return;
  }
  play();
}
A.addEventListener('timeupdate',()=>{
  if(stopAt!==null&&A.currentTime>=stopAt){A.pause();stopAt=null}
  follow();
});

// follow updates classes in place rather than re-rendering. timeupdate fires
// several times a second and a full re-render would rebuild every segment, fight
// the scroll position, and drop the caret mid-edit.
let lastSeg=-1,lastWord=null;
function follow(){
  const t=A.currentTime;
  let si=-1;
  for(let i=0;i<segs.length;i++){ if(t>=segs[i].start&&t<segs[i].end){si=i;break} }
  const rows=L.querySelectorAll('.seg');
  if(si!==lastSeg){
    if(lastSeg>=0&&rows[lastSeg]) rows[lastSeg].classList.remove('playing');
    if(si>=0&&rows[si]) rows[si].classList.add('playing');
    lastSeg=si;
  }
  if(lastWord){lastWord.classList.remove('now');lastWord=null}
  if(si<0||!rows[si]) return;
  const ws=rows[si].querySelectorAll('w');
  let best=null;
  for(const w of ws){ if(parseFloat(w.dataset.t)<=t) best=w; else break }
  if(best){best.classList.add('now');lastWord=best}
}

function save(){
  document.getElementById('saved').textContent='saving…';
  return fetch('api/segments',{method:'POST',headers:{'Content-Type':'application/json'},
    body:JSON.stringify({segments:segs})}).then(r=>r.json())
    .then(()=>document.getElementById('saved').textContent='saved')
    .catch(()=>document.getElementById('saved').textContent='SAVE FAILED');
}
// Undo keeps whole snapshots. A 114-segment list is a few KB; an inverse-op
// stack is where editors get their subtlest bugs, and this is not the place to
// spend that risk.
function push(){ hist.push(JSON.stringify(segs)); if(hist.length>200) hist.shift(); }
function undo(){
  if(!hist.length) return;
  segs=JSON.parse(hist.pop());
  if(cur>=segs.length) cur=segs.length-1;
  render(); save();
}

function assign(u){
  const s=segs[cur]; if(!s) return;
  push();
  s.speaker=u; s.confirmed=true; s.unclear=false;
  const merged=coalesce();
  render(); save();
  // A merge already moved the cursor onto the combined turn; advancing again
  // would skip whatever follows it.
  if(mode==='list'&&!merged) next();
}
function next(){ if(cur<segs.length-1){focus(cur+1);play()} }

// Jump to the next turn nobody has ruled on. A pass is never linear — joins and
// splits send you backwards, and a corrected turn leaves neighbours untouched —
// so "what is left" stops being visible once the list is long. Wraps, because
// the answer to "none after here" is usually "there are some before here".
function seekUnconfirmed(dir){
  const n=segs.length;
  for(let d=1;d<=n;d++){
    const i=((cur+dir*d)%n+n)%n;
    const s=segs[i];
    if(!s.confirmed&&!s.unclear){ focus(i); play(); return }
  }
  document.getElementById('saved').textContent='all reviewed';
}

// joinPrev merges this turn into the one before it and plays the result, so the
// join can be heard immediately — the whole point of the repair is that the
// words belong together, and that is an audible claim.
function joinPrev(){
  if(cur<1) return;
  const a=cur-1;
  push();
  const merged={
    id:segs[a].id, start:segs[a].start, end:segs[cur].end,
    text:(segs[a].text+' '+segs[cur].text).replace(/\s+/g,' ').trim(),
    speaker:segs[a].speaker, confirmed:false, unclear:false
  };
  segs.splice(a,2,merged);
  cur=a; caret=1; render(); save(); resume();
}

function splitHere(){
  const s=segs[cur]; const w=words(s.text);
  if(caret<1||caret>=w.length) return;
  push();
  // No word timestamps in the transcript, so the cut time is interpolated by
  // word position. It is an estimate, and it is why the audio still has to be
  // listened to — but the TEXT boundary, which is what attribution turns on,
  // is exact.
  const frac=caret/w.length, at=s.start+(s.end-s.start)*frac;
  const left={id:s.id,start:s.start,end:at,text:w.slice(0,caret).join(' '),speaker:s.speaker,confirmed:false,unclear:false};
  const right={id:s.id,start:at,end:s.end,text:w.slice(caret).join(' '),speaker:s.speaker,confirmed:false,unclear:false};
  segs.splice(cur,1,left,right);
  mode='list'; caret=1; render(); save(); help(); resume();
}

// A LABEL IS AN IDENTITY CLAIM. Naming two ids the same person means they ARE
// the same person, and the truth file has to say so — otherwise scoring counts
// them as two speakers while the human who listened said one, which is precisely
// the error this pass exists to eliminate.
//
// Diarization over-splits a voice routinely (mic distance, volume, channel), so
// this is the common path, not an edge case. It is confirmed rather than silent
// because a typo would otherwise merge minutes of audio irreversibly-looking;
// undo covers it, but the confirmation states the cost first.
// renameTo applies a name, merging into an existing speaker of the same name.
function renameTo(u,name){
  const norm=name.trim().toLowerCase();
  const twin=norm?Object.keys(speakers).find(k=>k!==u&&(speakers[k]||'').trim().toLowerCase()===norm):null;
  if(twin){
    const mine=secsOf(u), theirs=secsOf(twin);
    if(!confirm('"'+name+'" already names another speaker.\n\nTreat them as the SAME person? '+
        Math.round(mine)+'s + '+Math.round(theirs)+'s merge into one.')){ render(); help(); return }
    const keep = theirs>=mine ? twin : u, drop = theirs>=mine ? u : twin;
    push();
    segs.forEach(x=>{ if(x.speaker===drop) x.speaker=keep });
    delete speakers[drop];
    speakers[keep]=name;
    coalesce();
    Promise.all([
      fetch('api/speaker',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({uuid:drop,remove:true})}),
      fetch('api/speaker',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({uuid:keep,label:name})})
    ]).then(()=>{roster();render();save();help()});
    return;
  }
  speakers[u]=name;
  fetch('api/speaker',{method:'POST',headers:{'Content-Type':'application/json'},
    body:JSON.stringify({uuid:u,label:name})}).then(()=>{roster();render();help()});
}

function nameSpeaker(){
  const s=segs[cur]; const u=s&&s.speaker; if(!u) return;
  const name=prompt('name for '+labelOf(u),speakers[u]||'');
  if(name===null) return;
  const norm=name.trim().toLowerCase();
  const twin=norm?Object.keys(speakers).find(k=>k!==u&&(speakers[k]||'').trim().toLowerCase()===norm):null;
  if(twin){
    const mine=secsOf(u), theirs=secsOf(twin);
    if(!confirm('"'+name+'" already names another speaker.\n\nTreat them as the SAME person? '+
        Math.round(mine)+'s + '+Math.round(theirs)+'s merge into one.')){
      return;
    }
    // Absorb into whichever id holds more speech: the larger sample is the more
    // stable voiceprint, and keeping it means the id in the truth file still
    // matches the one the acoustic tools reason about.
    const keep = theirs>=mine ? twin : u, drop = theirs>=mine ? u : twin;
    push();
    segs.forEach(x=>{ if(x.speaker===drop) x.speaker=keep });
    delete speakers[drop];
    speakers[keep]=name;
    coalesce();
    Promise.all([
      fetch('api/speaker',{method:'POST',headers:{'Content-Type':'application/json'},
        body:JSON.stringify({uuid:drop,remove:true})}),
      fetch('api/speaker',{method:'POST',headers:{'Content-Type':'application/json'},
        body:JSON.stringify({uuid:keep,label:name})})
    ]).then(()=>{roster();render();save()});
    return;
  }
  speakers[u]=name;
  fetch('api/speaker',{method:'POST',headers:{'Content-Type':'application/json'},
    body:JSON.stringify({uuid:u,label:name})}).then(()=>{roster();render()});
}
function secsOf(u){ return segs.reduce((t,x)=>t+(x.speaker===u?x.end-x.start:0),0) }
function cycle(d){
  const s=segs[cur]; if(!s||!order.length) return;
  let i=order.indexOf(s.speaker);
  i=(i<0?0:i+d);
  if(i<0)i=order.length-1; if(i>=order.length)i=0;
  push(); s.speaker=order[i]; s.confirmed=true; s.unclear=false; coalesce(); render(); save();
}

function help(){
  const list=[['space','play / pause'],['r','replay'],['j / k','next / prev turn'],
    ['↑ / ↓','change speaker'],['1–9','assign speaker'],['enter','pick speaker  (enter again = confirm)'],
    ['c','correct the transcript'],['a','join with previous'],['s','split this turn'],
    ['x','unclear'],['d','label this speaker'],['u','undo'],
    ['/  ·  ?','next / prev unreviewed'],['-  ·  =','volume down / up (to 300%)'],
    ['click a word','jump the audio there']];
  const split=[['← / →','move the cut point'],['enter','split here'],
    ['esc  ·  ← past start','cancel'],['space','play'],['u','undo']];
  const text=[['enter','save the correction'],['shift+enter','newline'],['esc','cancel']];
  const pk=[['a–z','narrow by name'],['↑ / ↓','move the selection'],['1–9','select by number'],
    ['enter','accept  (no match = rename to what you typed)'],['backspace','undo a letter'],['esc','cancel']];
  const rows=(mode==='split'?split:mode==='text'?text:mode==='pick'?pk:list);
  const name=(mode==='split'?'SPLIT':mode==='text'?'CORRECT':mode==='pick'?'SPEAKER':'REVIEW');
  H.innerHTML='<h5><span class=mode>'+name+'</span> — '+
    (mode==='split'?'choose the word boundary to cut at':
     mode==='text'?'retype what was actually said':
     mode==='pick'?'type to narrow, or type a new name':'listen, confirm or repair, advance')+
    '</h5><div class=grid>'+
    rows.map(r=>'<span><kbd>'+r[0]+'</kbd> '+r[1]+'</span>').join('')+'</div>';
}

document.addEventListener('keydown',e=>{
  if(e.target.tagName==='INPUT'||e.target.tagName==='TEXTAREA') return;
  const k=e.key;
  if(k==='u'){e.preventDefault();undo();return}

  if(mode==='split'){
    if(k==='Escape'){mode='list';render();help();return}
    if(k==='ArrowLeft'){
      e.preventDefault();
      // Walking the caret off the front cancels. The first boundary is before
      // the second word, so "left of that" is not a narrower cut — it is no cut,
      // and it should mean what it looks like rather than sticking at the edge.
      if(caret<=1){ mode='list'; caret=1; render(); help(); return }
      caret--; render(); return;
    }
    if(k==='ArrowRight'){e.preventDefault();caret=Math.min(words(segs[cur].text).length-1,caret+1);render();return}
    if(k==='Enter'){e.preventDefault();splitHere();return}
    if(k===' '){e.preventDefault();A.paused?play():A.pause();return}
    return;
  }

  if(mode==='text') return; // the textarea owns its own keys

  if(mode==='pick'){
    e.preventDefault();
    if(k==='Escape'){mode='list';pick='';pickSel=0;render();help();return}
    if(k==='Enter'){
      const ms=pickerMatches();
      mode='list';
      if(pick&&ms.length===0){
        // Typing a name nothing answers to is not a failed search — it is the
        // name this speaker should have. Picking and naming are the same act,
        // so it does not need a separate key.
        const u=segs[cur].speaker, name=pick;
        pick='';pickSel=0;
        if(u) renameTo(u,name); else {render();help()}
        return;
      }
      const u=ms[pickSel]||segs[cur].speaker;
      pick='';pickSel=0;
      if(u) assign(u); else {render();help()}
      help();return;
    }
    if(k==='Backspace'){pick=pick.slice(0,-1);pickSel=0;render();return}
    if(k==='ArrowDown'){const n=pickerMatches().length; if(n) pickSel=(pickSel+1)%n; render(); return}
    if(k==='ArrowUp'){const n=pickerMatches().length; if(n) pickSel=(pickSel-1+n)%n; render(); return}
    if(k>='1'&&k<='9'&&!pick){
      // A number selects; enter still commits. Nothing in the picker acts on a
      // single keystroke, so a mistyped key is never a silent reassignment.
      const u=order[+k-1], ms=pickerMatches();
      if(u&&ms.includes(u)) pickSel=ms.indexOf(u);
      render(); return;
    }
    if(k.length===1&&/\S/.test(k)){ pick+=k.toLowerCase(); pickSel=0; render() }
    return;
  }

  if(k==='Enter'){e.preventDefault();if(segs[cur]){mode='pick';pick='';pickSel=0;render();help();scrollToCur()}return}
  if(k==='c'){
    const s=segs[cur]; if(!s) return;
    mode='text'; render(); help(); scrollToCur(); return;
  }
  if(k===' '){e.preventDefault();A.paused?play():A.pause();return}
  if(k==='r'){play();return}
  if(k==='j'){focus(cur+1);play();return}
  if(k==='k'){focus(cur-1);play();return}
  if(k==='ArrowDown'){e.preventDefault();cycle(1);return}
  if(k==='ArrowUp'){e.preventDefault();cycle(-1);return}
  if(k>='1'&&k<='9'){const u=order[+k-1]; if(u) assign(u); return}
  if(k==='-'||k==='_'){e.preventDefault();audioGraph();setVol(vol()-10);return}
  if(k==='='||k==='+'){e.preventDefault();audioGraph();setVol(vol()+10);return}
  if(k==='/'){e.preventDefault();seekUnconfirmed(1);return}
  if(k==='?'){e.preventDefault();seekUnconfirmed(-1);return}
  if(k==='a'){joinPrev();return}
  if(k==='s'){
    // A one-word turn has no interior boundary, so there is nothing to choose.
    if(words(segs[cur].text).length<2) return;
    mode='split'; caret=1; render(); help(); scrollToCur(); return;
  }
  if(k==='x'){push();const s=segs[cur]; s.unclear=true; s.confirmed=false; render(); save(); next(); return}
  if(k==='d'){nameSpeaker();return}
});
</script></body></html>`
