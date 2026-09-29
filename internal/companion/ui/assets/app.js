const base = document.querySelector('meta[name="agentclip-base"]').content;
const feedbackById = new Map();
const contentPrefetch = new Map();
let prefetchedBytes = 0;
const dateTime = new Intl.DateTimeFormat('pt-BR',{dateStyle:'medium',timeStyle:'short'});

// h builds an element. Every string child becomes a text node, so nothing that
// came from the server (file names, paths, error messages) can turn into markup.
// The page never assigns HTML: see TestDashboardScriptNeverBuildsMarkupFromStrings.
function h(tag, props, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(props || {})) {
    if (value === undefined || value === null || value === false) continue;
    if (key === 'class') node.className = value;
    else if (key === 'data') for (const [name, item] of Object.entries(value)) node.dataset[name] = String(item);
    else node.setAttribute(key, value === true ? '' : String(value));
  }
  node.append(...children.flat(Infinity).filter(child => child !== undefined && child !== null && child !== false).map(child => child instanceof Node ? child : document.createTextNode(String(child))));
  return node;
}
const text = value => String(value ?? '—');

function formatDateTime(value){const date=new Date(value);return Number.isNaN(date.getTime())?'—':dateTime.format(date)}
function formatBytes(bytes){if(!Number.isFinite(bytes)||bytes<0)return '—';const units=['B','KB','MB','GB'];let value=bytes,unit=0;while(value>=1024&&unit<units.length-1){value/=1024;unit++}return new Intl.NumberFormat('pt-BR',{maximumFractionDigits:value>=10?0:1}).format(value)+' '+units[unit]}
function statusRow(label, value, extra) {
  return h('div', {class: 'status-row' + (extra ? ' ' + extra : '')}, h('dt', null, label), h('dd', null, value));
}
async function copyText(text){if(navigator.clipboard?.writeText){try{await navigator.clipboard.writeText(text);return}catch(_){/* Try the legacy clipboard path below. */}}const area=document.createElement('textarea');area.value=text;area.setAttribute('readonly','');area.style.position='fixed';area.style.opacity='0';document.body.append(area);area.select();let ok=false;try{ok=document.execCommand('copy')}finally{area.remove()}if(!ok)throw new Error('clipboard unavailable')}
function fetchContent(id,signal){return fetch(base+'/api/inbound/'+encodeURIComponent(id)+'/content',{cache:'no-store',signal}).then(response=>{if(!response.ok)throw new Error('content unavailable');return response.text()})}
function removePrefetch(id,entry,abort){if(contentPrefetch.get(id)!==entry)return;contentPrefetch.delete(id);prefetchedBytes=Math.max(0,prefetchedBytes-entry.size);if(abort&&!entry.inUse)entry.controller.abort()}
function prefetchContent(id,size){if(!Number.isFinite(size)||size<0||size>1048576||contentPrefetch.has(id))return null;while(contentPrefetch.size>=8||prefetchedBytes+size>1048576){const oldest=contentPrefetch.entries().next();if(oldest.done)return null;removePrefetch(oldest.value[0],oldest.value[1],true)}const entry={createdAt:Date.now(),size,inUse:false,controller:new AbortController()};entry.promise=fetchContent(id,entry.controller.signal);contentPrefetch.set(id,entry);prefetchedBytes+=size;entry.promise.catch(()=>removePrefetch(id,entry,false));setTimeout(()=>removePrefetch(id,entry,true),15000);return entry.promise}
function setFeedback(id,kind,message,style){const entry=feedbackById.get(id)||{};entry[kind]={message,style};feedbackById.set(id,entry);const target=[...document.querySelectorAll('.copy-feedback')].find(node=>node.dataset.feedbackId===id);if(target){target.textContent=Object.values(entry).map(item=>item.message).filter(Boolean).join(' ');target.className='copy-feedback '+(style||'')}}
async function copyPath(button){const id=button.dataset.id;try{await copyText(button.dataset.path);setFeedback(id,'path','Caminho copiado.','positive')}catch(_){setFeedback(id,'path','Não foi possível copiar o caminho.','bad')}}
async function copyContent(button){const id=button.dataset.id;button.disabled=true;setFeedback(id,'content','Copiando conteúdo…','');try{const cached=contentPrefetch.get(id);let content;if(cached&&Date.now()-cached.createdAt<15000){cached.inUse=true;try{content=await cached.promise}finally{cached.inUse=false;removePrefetch(id,cached,false)}}else content=await fetchContent(id);await copyText(content);setFeedback(id,'content','Conteúdo copiado.','positive')}catch(_){setFeedback(id,'content','Não foi possível copiar o conteúdo.','bad')}finally{button.disabled=false}}
async function inbound(id,action){const r=await fetch(base+'/api/inbound/'+encodeURIComponent(id)+'/'+action,{method:'POST'});if(!r.ok){alert(await r.text());return}await refresh()}
function previewActions(o) {
  const id = encodeURIComponent(o.id);
  return [
    h('a', {class: 'btn btn-primary', target: '_blank', rel: 'noopener', href: base + '/api/inbound/' + id + '/content'}, 'Abrir conteúdo'),
    h('button', {class: 'btn btn-secondary copy-content', type: 'button', data: {id: o.id, size: o.size}}, 'Copiar conteúdo'),
  ];
}
function fileCard(o){
  const id = encodeURIComponent(o.id);
  const ext = (o.name || '').split('.').pop().toUpperCase().slice(0, 3) || 'FILE';
  const feedback = Object.values(feedbackById.get(o.id) || {}).map(item => item.message).filter(Boolean).join(' ');
  return h('article', {class: 'file-card'},
    h('div', {class: 'file-icon', 'aria-hidden': 'true'}, h('span', null, text(ext))),
    h('div', {class: 'file-info'},
      h('h3', {class: 'h3'}, text(o.name)),
      h('p', {class: 'file-meta'}, 'Recebido em ', text(formatDateTime(o.delivered_at)), ' ', h('span', null, '·'), ' ', text(formatBytes(o.size))),
      h('p', {class: 'file-path'}, text(o.path))),
    h('div', {class: 'file-actions'},
      h('div', {class: 'primary-actions'}, o.previewable ? previewActions(o) : []),
      h('div', {class: 'secondary-actions'},
        h('a', {class: 'btn btn-quiet', href: base + '/api/inbound/' + id + '/download'}, 'Baixar'),
        h('button', {class: 'btn btn-quiet copy-path', type: 'button', data: {path: o.path, id: o.id}}, 'Copiar caminho')),
      h('span', {class: 'copy-feedback', 'aria-live': 'polite', data: {feedbackId: o.id}}, feedback)));
}
function offerRow(o) {
  return h('div', {class: 'offer-row'},
    h('span', null,
      h('span', {class: 'file-name'}, text(o.name)),
      h('span', {class: 'muted metadata'}, 'Oferta recebida em ', text(formatDateTime(o.created_at)), ' · ', text(formatBytes(o.size)), h('br'), 'Expira em ', text(formatDateTime(o.expires_at)))),
    h('span', {class: 'actions'},
      h('button', {class: 'btn btn-accept', data: {id: o.id}}, 'Aceitar'),
      h('button', {class: 'btn btn-reject', data: {id: o.id}}, 'Recusar')));
}
const emptyCopy = message => h('p', {class: 'muted empty-copy'}, message);
function setConnection(connected, label) {
  const connection = document.querySelector('#connection');
  connection.classList.toggle('offline', !connected);
  connection.replaceChildren(h('span', {class: 'status-dot' + (connected ? '' : ' offline')}), ' ' + label);
  const live = document.querySelector('#live-indicator');
  live.classList.toggle('offline', !connected);
  live.setAttribute('aria-label', label);
}
function statusRows(s, connected) {
  const items = s.clipboard?.items || [];
  const clipboard = s.clipboard?.armed
    ? (items.length ? [h('span', {class: 'positive'}, items.length + ' item(ns)'), h('span', {class: 'clipboard-name'}, text(items.map(i => i.name || i.kind).join(', ')))] : h('span', {class: 'muted'}, 'Sem itens'))
    : h('span', {class: 'muted'}, 'Sem clipboard armado');
  const rows = [
    statusRow('Perfil', text(s.profile)),
    statusRow('Servidor', h('code', null, text(s.destination))),
    statusRow('Túnel', connected ? h('span', {class: 'positive'}, 'Conectado') : h('span', {class: 'bad'}, 'Desconectado')),
    statusRow('Clipboard', clipboard, 'clipboard-row'),
    statusRow('Expira', s.clipboard?.armed && s.clipboard?.expires_at ? text(formatDateTime(s.clipboard.expires_at)) : '—'),
  ];
  if (s.tunnel?.last_error) rows.push(statusRow('Último erro', h('span', {class: 'bad'}, text(s.tunnel.last_error))));
  if (s.release?.update_available) rows.push(statusRow('Atualização disponível', h('span', {class: 'bad'}, h('code', null, text(s.release.latest_version)), ' · execute ', h('code', null, 'agentclip upgrade'))));
  else if (s.release?.error) rows.push(statusRow('Atualização', h('span', {class: 'muted'}, text(s.release.error))));
  return rows;
}
async function refresh(){
  try {
    const r = await fetch(base + '/api/status', {cache: 'no-store'});
    if (!r.ok) throw new Error('status unavailable');
    const s = await r.json();
    const connected = !!s.tunnel?.connected;
    setConnection(connected, 'Túnel ' + (connected ? 'conectado' : 'desconectado'));
    document.querySelector('#status').replaceChildren(...statusRows(s, connected));
    const offers = s.inbound?.offers || [];
    document.querySelector('#offer-count').textContent = offers.length;
    document.querySelector('#offers').replaceChildren(...(offers.length
      ? [h('p', {class: 'approval-status'}, 'Aguardando sua aprovação ', h('strong', null, '(' + offers.length + ')')), ...offers.map(offerRow)]
      : [emptyCopy('Nenhum arquivo aguardando aprovação.'), h('div', {class: 'empty-state-icon', 'aria-hidden': 'true'}, h('span', null, '✓'))]));
    const received = s.inbound?.received || [];
    document.querySelector('#file-count').textContent = received.length;
    document.querySelector('#received').replaceChildren(...(received.length ? received.map(fileCard) : [emptyCopy('Nenhum arquivo recebido nesta sessão.')]));
    document.querySelector('#updated').textContent = 'Atualizado às ' + formatDateTime(new Date());
  } catch (_) {
    setConnection(false, 'Estado indisponível');
    document.querySelector('#updated').textContent = 'Falha ao atualizar · dados indisponíveis';
    document.querySelector('#status').replaceChildren(statusRow('Status', h('span', {class: 'bad'}, 'Não foi possível consultar o Companion.')));
    document.querySelector('#offer-count').textContent = '—';
    document.querySelector('#offers').replaceChildren(emptyCopy('Estado das aprovações indisponível.'));
    document.querySelector('#file-count').textContent = '—';
    document.querySelector('#received').replaceChildren(emptyCopy('Lista de arquivos indisponível.'));
  }
}
document.addEventListener('pointerover',e=>{const button=e.target.closest('.copy-content');if(button)(prefetchContent(button.dataset.id,Number(button.dataset.size))||Promise.resolve()).catch(()=>{})});document.addEventListener('focusin',e=>{const button=e.target.closest('.copy-content');if(button)(prefetchContent(button.dataset.id,Number(button.dataset.size))||Promise.resolve()).catch(()=>{})});document.addEventListener('click',e=>{const button=e.target.closest('button');if(!button)return;if(button.matches('.copy-content'))copyContent(button);else if(button.matches('.copy-path'))copyPath(button);else if(button.matches('.btn-accept'))inbound(button.dataset.id,'accept');else if(button.matches('.btn-reject'))inbound(button.dataset.id,'reject')});document.querySelector('#stop').onclick=async()=>{if(confirm('Parar o Companion?')){await fetch(base+'/api/stop',{method:'POST'});setTimeout(refresh,500)}};refresh();setInterval(refresh,2000);
