package plugins

const popiooHTML = `<!DOCTYPE html>
<html lang="id">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Popioo TicTacToe — Dark</title>
<style>
:root{
  --bg:transparent;
  --card:transparent;
  --card-2:#2a3942;
  --ink:#e9edef;
  --ink-soft:#aebac1;
  --muted:#8696a0;
  --accent:#00a884;
  --accent-2:#008069;
  --line:#2a3942;
  --line-strong:#374248;
  --cell-bg:#111b21;
  --o:#00a884;
  --x:#e9edef;
  --sys:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;
}
*{margin:0;padding:0;box-sizing:border-box}
html,body{
  background:transparent;
  color:var(--ink);
  font-family:var(--sys);
  min-height:100vh;
  overflow-x:hidden;
  -webkit-font-smoothing:antialiased;
}
.stage{
  min-height:100vh;
  display:flex;flex-direction:column;
  align-items:center;justify-content:center;
  padding:24px 16px;
}
.card{
  width:100%;
  max-width:360px;
}
.header{
  display:flex;align-items:baseline;justify-content:space-between;
  margin-bottom:14px;padding-bottom:12px;
  border-bottom:1px solid var(--line);
  gap:8px;
}
.header__title{
  font-size:17px;font-weight:600;
  color:var(--ink);letter-spacing:-.005em;
}
.header__sub{font-size:12px;color:var(--muted)}
.status{
  display:flex;align-items:center;justify-content:space-between;
  margin-bottom:14px;font-size:13px;gap:8px;
}
.status__turn{display:flex;align-items:center;gap:8px;color:var(--ink-soft)}
.status__indicator{
  width:9px;height:9px;border-radius:50%;
  background:var(--x);
  position:relative;flex-shrink:0;
  transition:background .2s ease;
}
.status__indicator.is-o{background:var(--o)}
.status__indicator.is-thinking::after{
  content:'';position:absolute;inset:-3px;
  border-radius:50%;border:1.5px solid var(--o);
  animation:ring 1.1s ease-out infinite;
}
@keyframes ring{
  0%{transform:scale(.7);opacity:1}
  100%{transform:scale(2.2);opacity:0}
}
.status__score{
  display:flex;gap:12px;
  font-variant-numeric:tabular-nums;
  color:var(--muted);font-size:12px;
}
.status__score b{
  color:var(--ink);font-weight:600;margin-left:3px;
}
.board{
  width:100%;aspect-ratio:1;
  position:relative;
  display:grid;
  grid-template-columns:1fr 1fr 1fr;
  grid-template-rows:1fr 1fr 1fr;
  background:var(--cell-bg);
  border-radius:8px;overflow:hidden;
  border:1px solid var(--line);
}
.cell{
  position:relative;background:transparent;border:none;
  cursor:pointer;padding:0;
  font-family:inherit;color:inherit;
}
.cell:disabled{cursor:default}
.cell:focus-visible{outline:2px solid var(--accent);outline-offset:-3px}
.cell::before{
  content:'';position:absolute;right:0;top:6%;bottom:6%;
  width:1px;background:var(--line-strong);
}
.cell:nth-child(3n)::before{display:none}
.cell::after{
  content:'';position:absolute;bottom:0;left:6%;right:6%;
  height:1px;background:var(--line-strong);
}
.cell:nth-last-child(-n+3)::after{display:none}
.cell.is-winning{background:rgba(0,168,132,.08)}
.cell svg{
  position:absolute;inset:0;margin:auto;
  width:58%;height:58%;
  display:block;pointer-events:none;
}
.cell .mark-x line{
  stroke:var(--x);stroke-width:2.2;
  stroke-linecap:round;
  stroke-dasharray:100;stroke-dashoffset:100;
  animation:draw .22s cubic-bezier(0,0,.2,1) forwards;
}
.cell .mark-x line:nth-child(2){animation-delay:.06s}
.cell .mark-o circle{
  stroke:var(--o);stroke-width:2.2;
  stroke-linecap:round;fill:none;
  stroke-dasharray:300;stroke-dashoffset:300;
  animation:draw .3s cubic-bezier(0,0,.2,1) forwards;
}
@keyframes draw{to{stroke-dashoffset:0}}
.winning-line{
  position:absolute;pointer-events:none;z-index:2;
  height:2px;background:var(--o);border-radius:2px;
  transform-origin:left center;
}
.winning-line.is-x{background:var(--x)}
.controls{
  margin-top:14px;display:flex;gap:6px;align-items:center;
}
.btn{
  background:var(--cell-bg);color:var(--ink-soft);
  border:1px solid var(--line);
  padding:8px 12px;border-radius:6px;
  font-size:12px;font-family:inherit;font-weight:500;
  cursor:pointer;transition:all .15s ease;
  white-space:nowrap;
}
.btn:hover{background:var(--card-2);color:var(--ink);border-color:var(--line-strong)}
.btn--primary{
  background:var(--accent);color:#111b21;
  border-color:var(--accent);font-weight:600;
  flex:1;
}
.btn--primary:hover{background:var(--accent-2);border-color:var(--accent-2);color:#111b21}
.levels{
  display:flex;gap:4px;background:var(--cell-bg);
  border:1px solid var(--line);
  padding:3px;border-radius:6px;
}
.level-btn{
  background:transparent;border:none;color:var(--muted);
  font-size:11px;font-family:inherit;
  padding:4px 8px;border-radius:4px;cursor:pointer;
  transition:all .15s ease;font-weight:500;
}
.level-btn:hover{color:var(--ink)}
.level-btn.is-active{
  background:var(--card-2);color:var(--ink);
}
.modal{
  position:fixed;inset:0;display:flex;
  align-items:center;justify-content:center;
  padding:16px;z-index:10;
  transition:opacity .2s ease;
}
.modal[hidden]{display:none}
.modal__backdrop{
  position:absolute;inset:0;background:rgba(11,20,26,.7);
  backdrop-filter:blur(4px);-webkit-backdrop-filter:blur(4px);
}
.modal__card{
  position:relative;background:#202c33;
  border:1px solid var(--line);
  border-radius:10px;padding:20px;
  width:100%;max-width:280px;text-align:center;
  box-shadow:0 12px 32px rgba(0,0,0,.4);
  transform:scale(.94);opacity:0;
  transition:transform .2s cubic-bezier(0,0,.2,1),opacity .2s ease;
}
.modal.is-open .modal__card{transform:scale(1);opacity:1}
.modal__title{
  font-size:17px;font-weight:600;color:var(--ink);margin-bottom:4px;
}
.modal__title.is-o{color:var(--o)}
.modal__sub{
  font-size:12px;color:var(--muted);margin-bottom:16px;
}
</style>
</head>
<body>
<div class="stage">
  <div class="card">
    <div class="header">
      <div class="header__title">Popioo TicTacToe</div>
      <div class="header__sub">Meta AI Embedded</div>
    </div>
    <div class="status">
      <div class="status__turn">
        <div id="indicator" class="status__indicator"></div>
        <span id="turn-text">Giliran kamu (X)</span>
      </div>
      <div class="status__score">
        <span>X <b id="score-x">0</b></span>
        <span>O <b id="score-o">0</b></span>
        <span>- <b id="score-d">0</b></span>
      </div>
    </div>
    <div id="board" class="board"></div>
    <div class="controls">
      <div class="levels" id="levels"></div>
      <button id="reset" class="btn btn--primary">Main Lagi</button>
    </div>
  </div>
</div>
<div id="modal" class="modal" hidden>
  <div class="modal__backdrop" id="modal-backdrop"></div>
  <div class="modal__card">
    <div id="modal-title" class="modal__title">Kamu menang</div>
    <div id="modal-sub" class="modal__sub">Permainan selesai</div>
    <button id="modal-retry" class="btn btn--primary">Main Lagi</button>
  </div>
</div>
<script>
const WIN_LINES=[
  [0,1,2],[3,4,5],[6,7,8],
  [0,3,6],[1,4,7],[2,5,8],
  [0,4,8],[2,4,6]
];
const LEVELS=[
  {id:0,name:'Mudah'},
  {id:1,name:'Sedang'},
  {id:2,name:'Sulit'},
  {id:3,name:'Pakar'}
];
const state={
  board:Array(9).fill(null),
  turn:'X',
  human:'X',
  ai:'O',
  level:2,
  over:false,
  winner:null,
  line:null,
  scores:{X:0,O:0,D:0},
  thinking:false
};
const $=id=>document.getElementById(id);
const boardEl=$('board');
const indicatorEl=$('indicator');
const turnText=$('turn-text');
const modal=$('modal');
function buildBoard(){
  boardEl.innerHTML='';
  for(let i=0;i<9;i++){
    const b=document.createElement('button');
    b.className='cell';
    b.dataset.index=i;
    b.setAttribute('aria-label','Kotak '+(i+1));
    b.addEventListener('click',()=>onCellClick(i));
    boardEl.appendChild(b);
  }
}
function buildLevels(){
  const wrap=$('levels');
  wrap.innerHTML='';
  LEVELS.forEach(l=>{
    const b=document.createElement('button');
    b.className='level-btn'+(l.id===state.level?' is-active':'');
    b.textContent=l.name;
    b.addEventListener('click',()=>{
      state.level=l.id;
      buildLevels();
      resetBoard();
    });
    wrap.appendChild(b);
  });
}
function renderMark(val){
  if(val==='X'){
    return '<svg class="mark-x" viewBox="0 0 40 40"><line x1="8" y1="8" x2="32" y2="32"/><line x1="32" y1="8" x2="8" y2="32"/></svg>';
  }
  if(val==='O'){
    return '<svg class="mark-o" viewBox="0 0 40 40"><circle cx="20" cy="20" r="12"/></svg>';
  }
  return '';
}
function render(){
  const cells=boardEl.children;
  for(let i=0;i<9;i++){
    const val=state.board[i];
    const c=cells[i];
    const prev=c.dataset.val;
    if(prev!==val){
      c.dataset.val=val||'';
      c.innerHTML=renderMark(val);
    }
    c.disabled=!!val||state.over||state.thinking;
    c.classList.toggle('is-winning',!!(state.line&&state.line.includes(i)));
  }
  indicatorEl.className='status__indicator'+
    (state.turn==='O'?' is-o':'')+
    (state.thinking?' is-thinking':'');
  if(state.over){
    if(state.winner==='X') turnText.textContent='Kamu menang';
    else if(state.winner==='O') turnText.textContent='AI menang';
    else turnText.textContent='Seri';
  } else if(state.thinking){
    turnText.textContent='AI sedang berpikir...';
  } else {
    turnText.textContent=state.turn===state.human?'Giliran kamu (X)':'Giliran AI (O)';
  }
  $('score-x').textContent=state.scores.X;
  $('score-o').textContent=state.scores.O;
  $('score-d').textContent=state.scores.D;
}
function onCellClick(i){
  if(state.over||state.thinking) return;
  if(state.board[i]!==null) return;
  if(state.turn!==state.human) return;
  makeMove(i,state.human);
  const r=checkWinner(state.board);
  if(r){endGame(r);return;}
  state.turn=state.ai;
  state.thinking=true;
  render();
  const delay=state.level===0?260:state.level===1?380:state.level===2?460:540;
  setTimeout(()=>{
    if(state.over) return;
    const move=chooseAIMove();
    state.thinking=false;
    if(move!==null) makeMove(move,state.ai);
    const r2=checkWinner(state.board);
    if(r2){endGame(r2);return;}
    state.turn=state.human;
    render();
  },delay);
}
function makeMove(i,player){
  state.board[i]=player;
  render();
}
function chooseAIMove(){
  const empty=[];
  state.board.forEach((v,i)=>{if(v===null) empty.push(i);});
  if(!empty.length) return null;
  const L=state.level;
  if(L===0){
    const win=findImmediate(state.board,state.ai);
    if(win!==null&&Math.random()<0.45) return win;
    return empty[Math.floor(Math.random()*empty.length)];
  }
  if(L===1){
    const win=findImmediate(state.board,state.ai);
    if(win!==null) return win;
    const block=findImmediate(state.board,state.human);
    if(block!==null&&Math.random()<0.75) return block;
    if(state.board[4]===null) return 4;
    return empty[Math.floor(Math.random()*empty.length)];
  }
  if(L===2){
    if(Math.random()<0.10) return empty[Math.floor(Math.random()*empty.length)];
    return minimaxMove(state.board,state.ai,4);
  }
  return minimaxMove(state.board,state.ai,9);
}
function findImmediate(board,player){
  for(const line of WIN_LINES){
    const cells=[board[line[0]],board[line[1]],board[line[2]]];
    const c=cells.filter(v=>v===player).length;
    const e=cells.filter(v=>v===null).length;
    if(c===2&&e===1) return line[cells.indexOf(null)];
  }
  return null;
}
function minimaxMove(board,player,maxDepth){
  let best=-Infinity,moves=[];
  for(let i=0;i<9;i++){
    if(board[i]!==null) continue;
    board[i]=player;
    const s=minimax(board,player==='O'?'X':'O',0,maxDepth,-Infinity,Infinity);
    board[i]=null;
    if(s>best){best=s;moves=[i];}
    else if(s===best){moves.push(i);}
  }
  return moves[Math.floor(Math.random()*moves.length)];
}
function minimax(board,current,depth,maxDepth,alpha,beta){
  const r=checkWinner(board);
  if(r){
    if(r.winner==='O') return 10-depth;
    if(r.winner==='X') return depth-10;
    return 0;
  }
  if(depth>=maxDepth) return 0;
  const isMax=current==='O';
  let best=isMax?-Infinity:Infinity;
  for(let i=0;i<9;i++){
    if(board[i]!==null) continue;
    board[i]=current;
    const s=minimax(board,current==='O'?'X':'O',depth+1,maxDepth,alpha,beta);
    board[i]=null;
    if(isMax){best=Math.max(best,s);alpha=Math.max(alpha,s);}
    else {best=Math.min(best,s);beta=Math.min(beta,s);}
    if(beta<=alpha) break;
  }
  return best;
}
function checkWinner(board){
  for(const line of WIN_LINES){
    const [a,b,c]=line;
    if(board[a]&&board[a]===board[b]&&board[a]===board[c]){
      return {winner:board[a],line};
    }
  }
  if(board.every(v=>v!==null)) return {winner:'D',line:null};
  return null;
}
function endGame(r){
  state.over=true;
  state.winner=r.winner;
  state.line=r.line;
  if(r.winner==='X') state.scores.X++;
  else if(r.winner==='O') state.scores.O++;
  else state.scores.D++;
  render();
  if(r.line){
    setTimeout(()=>drawWinningLine(r.line,r.winner),220);
    setTimeout(()=>showModal(r.winner),950);
  } else {
    setTimeout(()=>showModal(r.winner),400);
  }
}
function showModal(winner){
  const title=$('modal-title');
  const sub=$('modal-sub');
  if(winner==='X'){
    title.textContent='Kamu menang';
    title.className='modal__title';
    sub.textContent='Bagus sekali. Mau coba lagi?';
  } else if(winner==='O'){
    title.textContent='AI menang';
    title.className='modal__title is-o';
    sub.textContent='Kali ini lawan lebih baik. Ulangi?';
  } else {
    title.textContent='Seri';
    title.className='modal__title';
    sub.textContent='Imbang. Main sekali lagi?';
  }
  modal.hidden=false;
  void modal.offsetWidth;
  modal.classList.add('is-open');
  setTimeout(()=>$('modal-retry').focus(),80);
}
function hideModal(){
  modal.classList.remove('is-open');
  setTimeout(()=>{modal.hidden=true;},350);
}
function drawWinningLine(line,winner){
  const rect=boardEl.getBoundingClientRect();
  const s=boardEl.children[line[0]].getBoundingClientRect();
  const e=boardEl.children[line[2]].getBoundingClientRect();
  const x1=s.left+s.width/2-rect.left;
  const y1=s.top+s.height/2-rect.top;
  const x2=e.left+e.width/2-rect.left;
  const y2=e.top+e.height/2-rect.top;
  const len=Math.hypot(x2-x1,y2-y1);
  const ang=Math.atan2(y2-y1,x2-x1);
  const el=document.createElement('div');
  el.className='winning-line'+(winner==='X'?' is-x':'');
  el.style.left=x1+'px';
  el.style.top=y1+'px';
  el.style.width=len+'px';
  el.style.transform='translateY(-50%) rotate('+ang+'rad) scaleX(0)';
  el.style.transition='transform .55s cubic-bezier(0.65,0,0.35,1)';
  boardEl.appendChild(el);
  requestAnimationFrame(()=>requestAnimationFrame(()=>{
    el.style.transform='translateY(-50%) rotate('+ang+'rad) scaleX(1)';
  }));
}
function resetBoard(){
  hideModal();
  state.board=Array(9).fill(null);
  state.turn=state.human;
  state.over=false;state.winner=null;state.line=null;
  state.thinking=false;
  const wl=boardEl.querySelector('.winning-line');
  if(wl) wl.remove();
  render();
}
$('reset').addEventListener('click',resetBoard);
$('modal-retry').addEventListener('click',resetBoard);
$('modal-backdrop').addEventListener('click',resetBoard);
document.addEventListener('keydown',(e)=>{
  if(!modal.classList.contains('is-open')) return;
  if(e.key==='Escape'||e.key==='Enter'){e.preventDefault();resetBoard();}
});
buildBoard();
buildLevels();
render();
</script>
</body>
</html>`

const soundTestHTML = `
<style>
*{
  -webkit-tap-highlight-color:transparent;
  -webkit-user-select:none;
  user-select:none;
}
body{
  margin:0;
  background:transparent;
  font-family:Arial,sans-serif;
  color:#eee;
}
.box{
  width:100%;
  max-width:620px;
  margin:auto;
  padding:16px;
  box-sizing:border-box;
}
.card{
  background:rgba(255,255,255,.06);
  border:1px solid rgba(255,255,255,.15);
  border-radius:16px;
  padding:22px;
  box-sizing:border-box;
}
.title{
  font-size:22px;
  font-weight:bold;
  color:white;
}
.sub{
  font-size:12px;
  color:rgba(255,255,255,.5);
  margin-top:5px;
}
button{
  width:100%;
  border:0;
  border-radius:12px;
  padding:14px;
  margin-top:12px;
  font-size:15px;
  font-weight:bold;
  color:white;
  background:#6c5ce7;
  cursor:pointer;
}
button:active{
  transform:scale(.97);
}
.row{
  display:flex;
  gap:10px;
}
.row button{
  flex:1;
}
.status{
  margin-top:16px;
  text-align:center;
  font-size:12px;
  color:rgba(255,255,255,.55);
}
input{
  width:100%;
  box-sizing:border-box;
  margin-top:15px;
}
</style>

<div class="box">
  <div class="card">
    <div class="title">🔊 Sound Test</div>
    <div class="sub">Offline audio test</div>

    <button id="beep">TEST BEEP</button>

    <div class="row">
      <button id="low">LOW</button>
      <button id="mid">MID</button>
      <button id="high">HIGH</button>
    </div>

    <button id="melody">PLAY MELODY</button>

    <div style="margin-top:18px;font-size:12px;color:#aaa">
      Frequency:
      <span id="freqText">440 Hz</span>
    </div>

    <input
      id="freq"
      type="range"
      min="100"
      max="2000"
      value="440"
    >

    <div id="status" class="status">
      Ready — tekan tombol untuk mengeluarkan suara
    </div>
  </div>
</div>

<script>
let audioCtx = null;

function getAudio(){
  if(!audioCtx){
    audioCtx = new (
      window.AudioContext ||
      window.webkitAudioContext
    )();
  }
  if(audioCtx.state === 'suspended'){
    audioCtx.resume();
  }
  return audioCtx;
}

function playTone(freq, duration = 0.3, type = 'sine'){
  const ctx = getAudio();
  const osc = ctx.createOscillator();
  const gain = ctx.createGain();

  osc.type = type;
  osc.frequency.setValueAtTime(freq, ctx.currentTime);
  gain.gain.setValueAtTime(0.0001, ctx.currentTime);
  gain.gain.exponentialRampToValueAtTime(0.35, ctx.currentTime + 0.02);
  gain.gain.exponentialRampToValueAtTime(0.0001, ctx.currentTime + duration);

  osc.connect(gain);
  gain.connect(ctx.destination);
  osc.start();
  osc.stop(ctx.currentTime + duration + 0.05);

  document.getElementById('status').textContent = 'Playing ' + freq + ' Hz';
}

document.getElementById('beep').onclick = function(){
  playTone(440, 0.5, 'sine');
};

document.getElementById('low').onclick = function(){
  playTone(220, 0.5, 'sine');
};

document.getElementById('mid').onclick = function(){
  playTone(440, 0.5, 'sine');
};

document.getElementById('high').onclick = function(){
  playTone(880, 0.5, 'sine');
};

document.getElementById('freq').oninput = function(){
  document.getElementById('freqText').textContent = this.value + ' Hz';
};

document.getElementById('freq').onchange = function(){
  playTone(Number(this.value), 0.5, 'sine');
};

document.getElementById('melody').onclick = async function(){
  const notes = [
    261.63, 329.63, 392.00, 523.25, 392.00, 329.63, 261.63
  ];

  document.getElementById('status').textContent = 'Playing melody...';

  for(let i = 0; i < notes.length; i++){
    playTone(notes[i], 0.22, 'sine');
    await new Promise(function(resolve){
      setTimeout(resolve, 260);
    });
  }

  document.getElementById('status').textContent = 'Melody selesai';
};
</script>
`
