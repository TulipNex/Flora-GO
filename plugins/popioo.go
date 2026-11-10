package plugins

import (
	"context"
	"encoding/json"
	"fmt"

	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waAICommon"
	"github.com/polymorfa/hypermeow/proto/waAICommonDeprecated"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterPopioo)
}

// RegisterPopioo mendaftarkan command `.popioo` (alias tictac / htmlwidget):
// Mengirim widget Meta AI interaktif dengan embedded HTML webview game TicTacToe.
func RegisterPopioo(r *Registry) {
	r.Register(Command{
		Name:        "popioo",
		Aliases:     []string{"tictac", "htmlwidget"},
		Description: "Tampilkan widget TicTacToe Meta AI (HTML Embedded Screen)",
		Category:    CategoryDev,
		Handler:     handlePopioo,
	})
}

func handlePopioo(ctx context.Context, mctx *store.MessageContext) error {
	// Command uji pengembang: hanya owner.
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	msg := buildPopiooTicTacToeMessage()
	extra := whatsmeow.SendRequestExtra{ID: whatsmeow.GenerateMessageID()}

	resp, err := mctx.Client.SendMessage(ctx, mctx.Chat, msg, extra)
	if err != nil {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Gagal kirim Popioo TicTacToe: %v", err))
	}

	store.LogOutgoing(mctx, "popioo", "Popioo TicTacToe Widget")
	store.TrackBotMessageID(resp.ID, "popioo")
	return nil
}

func buildPopiooTicTacToeMessage() *waE2E.Message {
	data := map[string]any{
		"response_id": "PopiooNixelPopiooNixelPopiooNixel",
		"sections": []map[string]any{
			{
				"__typename": "GenAIUnifiedResponseSection",
				"view_model": map[string]any{
					"__typename": "GenAISingleLayoutViewModel",
					"primitive": map[string]any{
						"__typename":     "GenAIBotProgressStatusPrimitive",
						"title":          "Popioo:*Click Me*",
						"is_in_progress": true,
					},
				},
			},
		},
		"embedded_screens": []map[string]any{
			{
				"title": "Preview",
				"content": []map[string]any{
					{
						"__typename": "FOAIDNixelButtonSheets",
						"tabs": []map[string]any{
							{
								"id":         "tab_0",
								"tab_header": "Widget",
								"sections": []map[string]any{
									{
										"__typename": "GenAIUnifiedResponseSection",
										"view_model": map[string]any{
											"__typename": "GenAISingleLayoutViewModel",
											"primitive": map[string]any{
												"__typename":      "GenAIaeacdsnwHtmlPrimitive",
												"payload":         popiooHTML,
												"url":             "https://example.com",
												"trusted_sources": []string{"example.com"},
											},
										},
									},
								},
								"step_entries": []any{},
							},
						},
					},
				},
			},
		},
	}

	jsonBytes, _ := json.Marshal(data)

	richMsg := &waE2E.AIRichResponseMessage{
		MessageType: waAICommonDeprecated.AIRichResponseMessageType_AI_RICH_RESPONSE_TYPE_STANDARD.Enum(),
		UnifiedResponse: &waAICommon.AIRichResponseUnifiedResponse{
			Data: jsonBytes,
		},
		ContextInfo: &waE2E.ContextInfo{
			ForwardingScore: proto.Uint32(1),
			IsForwarded:     proto.Bool(true),
			ForwardOrigin:   waE2E.ContextInfo_META_AI.Enum(),
			ForwardedAiBotMessageInfo: &waAICommon.ForwardedAIBotMessageInfo{
				BotJID: proto.String("0@bot"),
			},
		},
	}

	return &waE2E.Message{
		MessageContextInfo: &waE2E.MessageContextInfo{
			DeviceListMetadataVersion: proto.Int32(2),
			BotMetadata:               &waAICommon.BotMetadata{},
		},
		BotForwardedMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				RichResponseMessage: richMsg,
			},
		},
	}
}

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
.cell.is-winning{background:rgba(0,168,132,0.14)}
.cell.is-winning-x{background:rgba(233,237,239,0.07)}
.mark{position:absolute;inset:22%;pointer-events:none}
.mark__svg{width:100%;height:100%;overflow:visible}
.mark__svg path,.mark__svg circle{
  fill:none;stroke:var(--x);stroke-width:9;
  stroke-linecap:round;
}
.mark--o .mark__svg path,.mark--o .mark__svg circle{stroke:var(--o)}
.mark__svg path{
  stroke-dasharray:120;stroke-dashoffset:120;
  animation:draw .3s ease-out forwards;
}
.mark__svg path:nth-child(2){animation-delay:.12s}
.mark__svg circle{
  stroke-dasharray:220;stroke-dashoffset:220;
  animation:draw .4s ease-out forwards;
}
@keyframes draw{to{stroke-dashoffset:0}}
.winning-line{
  position:absolute;height:4px;
  background:var(--o);
  transform-origin:left center;
  z-index:5;pointer-events:none;
  border-radius:2px;
}
.winning-line.is-x{background:var(--x)}
.levels{margin-top:14px}
.levels__label{
  font-size:11px;color:var(--muted);
  margin-bottom:8px;
}
.levels__list{
  display:flex;
  flex-wrap:nowrap;
  gap:8px;
  overflow-x:auto;
  overflow-y:hidden;
  -webkit-overflow-scrolling:touch;
  scrollbar-width:none;
  -ms-overflow-style:none;
  padding:2px 2px 6px;
  margin:0 -2px;
}
.levels__list::-webkit-scrollbar{
  display:none;
}
.level{
  background:transparent;
  border:1px solid var(--line-strong);
  border-radius:20px;
  padding:10px 18px;
  font-size:13px;font-weight:500;
  color:var(--ink-soft);
  cursor:pointer;
  font-family:inherit;
  transition:color .15s ease,border-color .15s ease,background .15s ease;
  white-space:nowrap;
  flex-shrink:0;
}
.level:hover{color:var(--ink);border-color:var(--ink-soft)}
.level.is-active{
  background:var(--accent);
  border-color:var(--accent);
  color:#0b141a;
}
.level:focus-visible{outline:2px solid var(--accent);outline-offset:1px}
.footer{
  margin-top:12px;
  display:flex;justify-content:center;
}
.footer__reset{
  background:none;border:none;
  color:var(--accent);
  font-family:inherit;
  font-size:13px;font-weight:500;
  cursor:pointer;
  padding:8px 16px;
  transition:color .15s ease;
}
.footer__reset:hover{color:var(--ink)}
.footer__reset:focus-visible{outline:2px solid var(--accent);outline-offset:1px}
.modal{
  position:fixed;inset:0;z-index:50;
  display:flex;align-items:center;justify-content:center;
  padding:24px;
  opacity:0;pointer-events:none;
  transition:opacity .3s ease;
}
.modal.is-open{opacity:1;pointer-events:auto}
.modal__backdrop{
  position:absolute;inset:0;
  background:transparent;
}
.modal__card{
  position:relative;
  background:var(--card-2);
  border-radius:12px;
  padding:24px 24px 0;
  text-align:center;
  max-width:320px;width:100%;
  box-shadow:0 24px 50px -12px rgba(0,0,0,0.6);
  transform:translateY(8px) scale(.96);
  transition:transform .35s cubic-bezier(.34,1.56,.64,1);
  overflow:hidden;
}
.modal.is-open .modal__card{transform:translateY(0) scale(1)}
.modal__title{
  font-size:20px;font-weight:700;
  letter-spacing:-.015em;
  color:var(--ink);margin-bottom:8px;line-height:1.25;
}
.modal__title.is-o{color:var(--o)}
.modal__sub{
  font-size:14px;color:var(--muted);
  margin-bottom:20px;line-height:1.4;
}
.modal__retry{
  background:transparent;border:none;
  color:var(--accent);
  font-family:inherit;
  font-size:16px;font-weight:600;
  letter-spacing:-.005em;
  cursor:pointer;
  padding:14px 24px;
  width:100%;
  border-top:1px solid var(--line-strong);
  transition:color .15s ease;
}
.modal__retry:hover{color:var(--ink)}
.modal__retry:focus-visible{outline:2px solid var(--accent);outline-offset:-2px}
@media (max-width:380px){
  .stage{padding:16px 12px}
  .level{padding:9px 16px;font-size:12px}
  .modal__title{font-size:18px}
}
@media (prefers-reduced-motion:reduce){
  *,*::before,*::after{
    animation-duration:.01ms!important;
    animation-iteration-count:1!important;
    transition-duration:.01ms!important;
  }
}
</style>
</head>
<body>
<main class="stage">
  <div class="card">
    <div class="header">
      <div class="header__title">Popioo TicTacToe</div>
      <div class="header__sub">Melawan AI</div>
    </div>
    <div class="status" aria-live="polite">
      <div class="status__turn">
        <span class="status__indicator" id="indicator"></span>
        <span id="status-text">Giliranmu</span>
      </div>
      <div class="status__score">
        <span>Kamu<b id="score-x">0</b></span>
        <span>Seri<b id="score-d">0</b></span>
        <span>AI<b id="score-o">0</b></span>
      </div>
    </div>
    <div class="board" id="board" role="grid" aria-label="Papan permainan tiga kali tiga"></div>
    <div class="levels">
      <div class="levels__label">Tingkat lawan</div>
      <div class="levels__list" id="levels-list" role="radiogroup" aria-label="Tingkat kesulitan"></div>
    </div>
    <div class="footer">
      <button class="footer__reset" id="reset">Ulang papan</button>
    </div>
  </div>
</main>
<div class="modal" id="modal" hidden>
  <div class="modal__backdrop" id="modal-backdrop"></div>
  <div class="modal__card" role="dialog" aria-modal="true" aria-labelledby="modal-title">
    <h2 class="modal__title" id="modal-title">Kamu menang</h2>
    <p class="modal__sub" id="modal-sub">Bagus sekali. Mau coba lagi?</p>
    <button class="modal__retry" id="modal-retry">Main lagi</button>
  </div>
</div>
<script>
const $=id=>document.getElementById(id);
const boardEl=$('board');
const levelsListEl=$('levels-list');
const indicatorEl=$('indicator');
const statusTextEl=$('status-text');
const modal=$('modal');
const WIN_LINES=[
  [0,1,2],[3,4,5],[6,7,8],
  [0,3,6],[1,4,7],[2,5,8],
  [0,4,8],[2,4,6]
];
const LEVELS=[
  {name:'Mudah',desc:'Gerakan acak'},
  {name:'Sedang',desc:'Cek menang langsung'},
  {name:'Sulit',desc:'Minimax sedang'},
  {name:'Ahli',desc:'Minimax penuh'}
];
let state={
  board:Array(9).fill(null),
  turn:'X',
  human:'X',
  ai:'O',
  over:false,
  winner:null,
  line:null,
  level:1,
  thinking:false,
  scores:{X:0,O:0,D:0}
};
function buildBoard(){
  boardEl.innerHTML='';
  for(let i=0;i<9;i++){
    const btn=document.createElement('button');
    btn.className='cell';
    btn.setAttribute('role','gridcell');
    btn.setAttribute('aria-label','Kotak '+(i+1));
    btn.dataset.idx=i;
    btn.addEventListener('click',()=>handleCellClick(i));
    boardEl.appendChild(btn);
  }
}
function buildLevels(){
  levelsListEl.innerHTML='';
  LEVELS.forEach((lvl,idx)=>{
    const b=document.createElement('button');
    b.className='level'+(idx===state.level?' is-active':'');
    b.textContent=lvl.name;
    b.setAttribute('role','radio');
    b.setAttribute('aria-checked',idx===state.level);
    b.addEventListener('click',()=>{
      state.level=idx;
      buildLevels();
      resetBoard();
    });
    levelsListEl.appendChild(b);
  });
}
function render(){
  const cells=boardEl.children;
  for(let i=0;i<9;i++){
    const val=state.board[i];
    const cell=cells[i];
    cell.disabled=state.over||val!==null||state.thinking;
    cell.innerHTML='';
    cell.className='cell';
    if(state.line&&state.line.includes(i)){
      cell.classList.add(state.winner==='X'?'is-winning-x':'is-winning');
    }
    if(val==='X'){
      cell.innerHTML='<span class="mark mark--x"><svg class="mark__svg" viewBox="0 0 100 100"><path d="M22 22 L78 78"/><path d="M78 22 L22 78"/></svg></span>';
    } else if(val==='O'){
      cell.innerHTML='<span class="mark mark--o"><svg class="mark__svg" viewBox="0 0 100 100"><circle cx="50" cy="50" r="32"/></svg></span>';
    }
  }
  indicatorEl.className='status__indicator'+(state.turn==='O'?' is-o':'')+(state.thinking?' is-thinking':'');
  if(state.over){
    statusTextEl.textContent='Selesai';
  } else if(state.thinking){
    statusTextEl.textContent='AI sedang berpikir…';
  } else {
    statusTextEl.textContent=state.turn===state.human?'Giliranmu':'Giliran AI';
  }
  $('score-x').textContent=state.scores.X;
  $('score-o').textContent=state.scores.O;
  $('score-d').textContent=state.scores.D;
}
function handleCellClick(idx){
  if(state.over||state.board[idx]!==null||state.turn!==state.human||state.thinking) return;
  state.board[idx]=state.human;
  const res=checkWinner(state.board);
  if(res){
    endGame(res);
  } else {
    state.turn=state.ai;
    state.thinking=true;
    render();
    setTimeout(aiTurn,Math.floor(Math.random()*260)+320);
  }
}
function aiTurn(){
  if(state.over) return;
  const move=pickAIMove();
  if(move!==null){
    state.board[move]=state.ai;
  }
  state.thinking=false;
  const res=checkWinner(state.board);
  if(res){
    endGame(res);
  } else {
    state.turn=state.human;
    render();
  }
}
function pickAIMove(){
  const empty=state.board.map((v,i)=>v===null?i:-1).filter(i=>i>=0);
  if(!empty.length) return null;
  const L=state.level;
  if(L===0) return empty[Math.floor(Math.random()*empty.length)];
  if(L===1){
    const win=findImmediate(state.board,state.ai);
    if(win!==null) return win;
    if(Math.random()>0.15){
      const block=findImmediate(state.board,state.human);
      if(block!==null) return block;
    }
    if(state.board[4]===null) return 4;
    if(Math.random()>0.2){
      const corners=[0,2,6,8].filter(i=>state.board[i]===null);
      if(corners.length) return corners[Math.floor(Math.random()*corners.length)];
    }
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
