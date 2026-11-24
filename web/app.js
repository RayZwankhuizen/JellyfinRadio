const content = document.querySelector('#content');
const title = document.querySelector('#page-title');
const titles = {'learning-recordings':'Leerfragmenten',player:'Speler',dashboard:'Dashboard',stations:'Alle zenders',favorites:'Favorieten',genres:'Genres',countries:'Andere landen',custom:'Eigen zenders',audio:'Audioanalyse',diagnostics:'Streamdiagnose',imports:'Importgeschiedenis', 'country-management':'Landen beheren'};
let stations = [];
let managedCountries = [];
const radioView = document.querySelector('#radio-view');
const radioIdle = document.querySelector('#radio-idle');
const radioPlayer = document.querySelector('#radio-player');
const radioAudio = document.querySelector('#radio-audio');
const spectrogramCanvas = document.querySelector('#radio-spectrogram');
const spectrogram = spectrogramCanvas.getContext('2d');
let audioContext, analyser, audioSource, spectrumFrame, captureNode, captureSilence, captureModule;
let currentCodec = '';
let streamStartTimer;
let currentStationId = '', audioProfile = null, dspAvailable = false, latestLiveFrame = null, classifying = false, liveGeneration = 0;
let metadataPollTimer;
let renderedMetadata = '';
let renderedHeaders = '';
let currentStreamMetadata;
const icyDetections = new Map();
const audioCategories = {
  music: 'Muziek', speech: 'Spraak', speech_over_music: 'Spraak over muziek',
  singing: 'Zang', jingle: 'Jingle', uncertain: 'Onbekend',
};

function audioCategory(category) {
  return Object.hasOwn(audioCategories, category) ? audioCategories[category] : 'Onbekend';
}

function icyEntryKey(entry) {
  return entry?.time && entry.raw_icy ? JSON.stringify([entry.time, entry.raw_icy]) : '';
}

function recordIcyDetection(key, result, capturedAt) {
  if (!key) return false;
  const recognition = icyDetections.get(key) || {categories: new Set()};
  const label = audioCategory(result.category);
  if (recognition.latest?.label === label) return false;
  recognition.categories.add(label);
  recognition.latest = {label, confidence: result.confidence, capturedAt};
  icyDetections.set(key, recognition);
  if (icyDetections.size > 100) icyDetections.delete(icyDetections.keys().next().value);
  return true;
}

const streamDetails = document.querySelector('#radio-metadata-details');
const streamHeaders = document.querySelector('#radio-metadata-headers');
const duckingHistory = document.querySelector('#radio-ducking-history');
const metadataCopy = document.querySelector('#radio-metadata-copy');
const streamTitleCategories = {
  music: 'Muziek', speech: 'Spraak', jingle: 'Jingle', advertisement: 'Reclame', news: 'Nieuws',
};
const streamTitleLabels = new Map();

function streamTitleLabelKey(title) {
  return 'smart-radio-title-label:' + JSON.stringify([currentStationId, title]);
}

function manualCategoryPicker(title, index) {
  if (!title) return '';
  const key = streamTitleLabelKey(title);
  if (!streamTitleLabels.has(key)) {
    let stored;
    try { stored = localStorage.getItem(key); } catch {}
    streamTitleLabels.set(key, Object.hasOwn(streamTitleCategories, stored) ? stored : '');
  }
  const selected = streamTitleLabels.get(key);
  const id = `stream-title-category-${index}`;
  const options = Object.entries(streamTitleCategories).map(([value, label]) =>
    `<option value="${value}"${value === selected ? ' selected' : ''}>${label}</option>`).join('');
  return `<label class="stream-title-category" for="${id}">Handmatig label</label><select id="${id}" class="select stream-title-category-select" data-title-key="${escape(key)}"><option value="">Geen label</option>${options}</select>`;
}

streamDetails.addEventListener('change', event => {
  const select = event.target.closest('.stream-title-category-select');
  if (!select) return;
  const key = select.dataset.titleKey;
  const value = Object.hasOwn(streamTitleCategories, select.value) ? select.value : '';
  streamTitleLabels.set(key, value);
  try {
    if (value) localStorage.setItem(key, value);
    else localStorage.removeItem(key);
  } catch {}
  streamDetails.querySelectorAll('.stream-title-category-select').forEach(picker => {
    if (picker.dataset.titleKey === key) picker.value = value;
  });
});

streamDetails.addEventListener('focusout', event => {
  if (event.target.matches('.stream-title-category-select')) {
    requestAnimationFrame(() => {
      if (currentStreamMetadata) renderStreamMetadata(currentStreamMetadata);
    });
  }
});

const duckingAdvice = {
  speech: 'Dempingssuggestie · Spraak',
  jingle: 'Dempingssuggestie · Jingle',
  speech_over_music: 'Demping overwegen · Spraak over muziek',
};
const stationDuckingHistory = new Map();
let lastDuckingDetection;
const duckingStorageKey = uuid => `smart-radio-ducking-history:${uuid}`;

function metadataRow(label, value, recognition = '', className = '', picker = '') {
  return `<dl class="radio-metadata-row ${className}"><dt>${escape(label)}</dt><dd><code>${escape(value)}</code>${recognition ? `<span class="radio-metadata-recognition">Herkend: ${escape(recognition)}</span>` : ''}${picker}</dd></dl>`;
}

function loadDuckingHistory(uuid) {
  if (!stationDuckingHistory.has(uuid)) {
    let entries = [];
    try {
      const stored = JSON.parse(localStorage.getItem(duckingStorageKey(uuid)));
      if (Array.isArray(stored)) entries = stored;
    } catch {}
    stationDuckingHistory.set(uuid, entries);
  }
  return stationDuckingHistory.get(uuid);
}

function renderDuckingHistory(uuid) {
  duckingHistory.innerHTML = loadDuckingHistory(uuid).map(entry =>
    metadataRow(`Demping · ${new Date(entry.time).toLocaleString('nl-NL')}`, entry.text, entry.recognition || entry.text.split(' · ')[1] || 'Onbekend', 'radio-ducking-entry')).join('');
  duckingHistory.parentElement.hidden = !duckingHistory.firstElementChild;
}

function recordDuckingDetection(result) {
  const category = result?.category;
  if (!(result?.confidence >= 0.75) || !Object.hasOwn(duckingAdvice, category)) {
    lastDuckingDetection = undefined;
    return;
  }
  const time = Date.now();
  const repeated = lastDuckingDetection?.category === category && time - lastDuckingDetection.time < 8000;
  lastDuckingDetection = {category, time};
  if (repeated) return;

  const text = `${duckingAdvice[category]} · Modelscore ${Math.round(result.confidence * 100)}%`;
  const entries = loadDuckingHistory(currentStationId);
  entries.unshift({time, text, recognition: audioCategory(category)});
  entries.length = Math.min(entries.length, 100);
  try { localStorage.setItem(duckingStorageKey(currentStationId), JSON.stringify(entries)); } catch {}
  renderDuckingHistory(currentStationId);
  metadataCopy.disabled = false;
}

async function api(path, options) {
  const management=path.startsWith('/api/');
  const send=token=>{const headers=new Headers(options?.headers||{});if(management&&token)headers.set('Authorization',`Bearer ${token}`);return fetch(path,{...options,headers});};
  let token=management?sessionStorage.getItem('smart-radio-api-token')||'':'';
  let response=await send(token);
  if(management&&response.status===401){
    sessionStorage.removeItem('smart-radio-api-token');
    token=prompt('Voer de Jellyfin Smart Radio beheertoken in:')?.trim()||'';
    if(!token)throw new Error('Beheertoken vereist.');
    response=await send(token);
    if(response.status===401){sessionStorage.removeItem('smart-radio-api-token');throw new Error('Beheertoken onjuist.');}
    sessionStorage.setItem('smart-radio-api-token',token);
  }
  const type = response.headers.get('content-type') || '';
  if (response.ok && type.startsWith('audio/wav')) return response.blob();
  const payload = type.includes('json') ? await response.json() : await response.text();
  if (!response.ok) throw new Error(payload.error || response.statusText);
  return payload;
}
function route() { return decodeURIComponent(location.hash.slice(1) || 'player'); }
function navActive(page) {
  document.querySelectorAll('nav a').forEach(item => item.classList.toggle('active', item.dataset.page === page));
}
const countryNav = document.querySelector('#country-nav');
const favoriteNav = document.querySelector('#favorite-nav');
let catalog;

function stationGroup(page, label, list, previous) {
  const group = document.createElement('details');
  group.className = 'station-nav-group';
  group.dataset.group = page;
  group.open = previous?.open ?? page === 'favorites';
  const management = page === 'country/unknown' ? '' : `<a href="#${escape(encodeURI(page))}" data-page="${escape(page)}">Zenders beheren</a>`;
  group.innerHTML = `<summary>${escape(label)}</summary><div class="station-nav-body"><input type="search" placeholder="Zoek zender" aria-label="${escape('Zoek zender in ' + label)}">${management}<div class="station-nav-results"></div></div>`;
  const search = group.querySelector('input');
  const results = group.querySelector('.station-nav-results');
  search.value = previous?.querySelector('input').value || '';
  const update = () => {
    const query = search.value.trim().toLocaleLowerCase();
    results.innerHTML = list.filter(station => `${station.name} ${station.tags || ''} ${station.language || ''}`.toLocaleLowerCase().includes(query))
      .map(station => `<button type="button" ${playAttributes(station)} aria-pressed="${station.uuid === currentStationId}">${escape(station.name)}</button>`).join('')
      || '<p role="status">Geen zenders gevonden.</p>';
  };
  search.addEventListener('input', update);
  group.addEventListener('toggle', () => { if (group.open) update(); });
  if (group.open) update();
  return group;
}

async function loadCountryNav() {
  try { managedCountries = await api('/api/countries') || []; }
  catch { managedCountries = []; }
  try { catalog = await api('/api/stations') || []; }
  catch { catalog = undefined; favoriteNav.textContent = 'Zenderlijst niet beschikbaar.'; return; }

  const previous = new Map([...document.querySelectorAll('.station-nav-group')].map(group => [group.dataset.group, group]));
  const groups = new Map(managedCountries.map(country => [country.code, {label: country.name, stations: []}]));
  const available = catalog.filter(station => !station.excluded).sort((a, b) => a.name.localeCompare(b.name, 'nl'));
  for (const station of available) {
    const code = station.country_code || 'unknown';
    if (!groups.has(code)) groups.set(code, {label: station.country || station.country_code || 'Onbekend land', stations: []});
    groups.get(code).stations.push(station);
  }
  favoriteNav.replaceChildren(stationGroup('favorites', 'Favorieten', available.filter(station => station.favorite), previous.get('favorites')));
  countryNav.replaceChildren(...[...groups].sort(([, a], [, b]) => a.label.localeCompare(b.label, 'nl')).map(([code, group]) => {
    const page = `country/${code}`;
    return stationGroup(page, group.label, group.stations, previous.get(page));
  }));
}

function playAttributes(station) {
  return `data-play="${escape(station.uuid)}" data-station-name="${escape(station.name)}" data-codec="${escape(station.codec || '')}" data-bitrate="${station.bitrate || 0}"`;
}

function updateCurrentStation() {
  document.querySelectorAll('.station-nav-results [data-play]').forEach(button => {
    button.setAttribute('aria-pressed', String(button.dataset.play === currentStationId));
  });
}

function showView(page) {
  navActive(page);
  const player = page === 'player';
  content.hidden = player;
  radioView.hidden = !player;
  title.textContent = titles[page] || '';
}

function openPlayer(focusTarget) {
  if (route() !== 'player') history.pushState(null, '', '#player');
  showView('player');
  if (focusTarget) document.querySelector(focusTarget).focus();
}
function escape(value='') { return String(value).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }
function pageHeader(kicker, heading, description, action='') {
  return `<div class="page-heading"><div><p class="eyebrow">${kicker}</p><h1>${heading}</h1><p class="subtitle">${description}</p></div>${action}</div>`;
}
function metric(label, value, note) { return `<article class="metric"><div class="metric-label">${label}</div><div class="metric-value">${value}</div><div class="metric-note">${note}</div></article>`; }
function stationTags(station) { return [...new Set((station.tags||'').split(',').map(tag=>tag.trim()).filter(Boolean))]; }
function stationCard(station) {
  const logo = station.logo_url ? `<img class="logo" src="${escape(station.logo_url)}" alt="" loading="lazy" referrerpolicy="no-referrer" onerror="this.replaceWith(Object.assign(document.createElement('div'),{className:'logo logo-fallback',textContent:'SR'}))">` : '<div class="logo logo-fallback">SR</div>';
  const tags=stationTags(station);
  const tagMarkup=tags.length?tags.map(tag=>`<span class="tag">${escape(tag)}</span>`).join(''):'<span class="station-no-genre">Geen zenderlabels</span>';
  return `<article class="station ${station.excluded?'excluded':''}"><div class="station-top">${logo}<div class="station-title"><strong title="${escape(station.name)}">${escape(station.name)}</strong><span>${escape(station.country || station.country_code || 'Onbekend land')}${station.excluded?' · uitgesloten':''}</span></div><button class="star ${station.favorite?'on':''}" data-favorite="${escape(station.uuid)}" aria-label="${station.favorite?'Verwijder favoriet':'Maak favoriet'}">${station.favorite?'★':'☆'}</button><button class="button quiet exclude-button" data-excluded="${escape(station.uuid)}" aria-label="${station.excluded?'Zender weer opnemen':'Zender uitsluiten'}">${station.excluded?'Opnemen':'Uitsluiten'}</button></div><div class="station-genres" aria-label="Radio Browser-zenderlabels"><span class="station-genre-label">Genres / zenderlabels</span>${tagMarkup}</div><div class="station-meta"><span>${escape(station.codec || 'Stream')} ${station.bitrate ? `· ${station.bitrate} kbps` : ''}</span><span>${escape(station.language || '')}</span></div><div class="station-actions"><button class="button quiet" type="button" ${playAttributes(station)}>Luister</button><button class="button quiet" type="button" data-streamdata="${escape(station.uuid)}">Streamdata</button><button class="button quiet" type="button" data-metadata-record="${escape(station.uuid)}" data-recording="false">Metadata opnemen</button><span class="metadata-record-status" data-metadata-record-status="${escape(station.uuid)}" role="status"></span><button class="button quiet station-delete" type="button" data-delete-station="${escape(station.uuid)}" data-station-name="${escape(station.name)}" data-source="${escape(station.source)}">Verwijderen</button></div></article>`;
}
function renderStations(list) { return list.length ? `<div class="station-grid">${list.map(stationCard).join('')}</div>` : '<div class="panel empty">Geen zenders gevonden. Synchroniseer de catalogus of voeg een eigen zender toe.</div>'; }
async function loadStations(params='') {
  stations = !params && catalog ? catalog : await api(`/api/stations${params}`) || [];
}
function stationPickerMarkup(prefix) {
  const countries=new Map(),managedByCode=new Map(),countryByCode=new Map();
  for(const country of managedCountries) {
    const code=(country.code||'').trim().toUpperCase();
    if(code&&!managedByCode.has(code))managedByCode.set(code,country.name);
  }
  const addCountry=(name,code)=>{
    const normalizedCode=(code||'').trim().toUpperCase();
    const label=(name||normalizedCode).trim(),key=label.toLowerCase().replace(/^the\s+/,'').normalize('NFD').replace(/[\u0300-\u036f]/g,'').replace(/[^a-z0-9]/g,'');
    if(!key)return;
    if(normalizedCode&&countryByCode.has(normalizedCode))return;
    if(!countries.has(key))countries.set(key,{label,codes:new Set()});
    const entry=countries.get(key);
    if(normalizedCode){entry.codes.add(normalizedCode);countryByCode.set(normalizedCode,entry);}
  };
  for(const country of managedCountries)addCountry(country.name,country.code);
  for(const station of stations)if(station.country_code){const code=station.country_code.trim().toUpperCase();addCountry(managedByCode.get(code)||station.country||code,code);}
  const countryOptions=[...countries.values()].sort((a,b)=>a.label.localeCompare(b.label)).map(country=>`<option value="${escape([...country.codes].join(','))}">${escape(country.label)}</option>`).join('');
  return `<section class="panel"><div class="panel-head"><div><h2>Selecteer een radiozender</h2><p>Filter de geïmporteerde catalogus op zendernaam, land en genre.</p></div></div><div class="station-picker"><label>Zoek op zendernaam<input id="${prefix}-name" type="search" placeholder="Bijv. ICE RADIO" autocomplete="off"></label><label>Land<select id="${prefix}-country" class="select"><option value="">Alle landen</option>${countryOptions}</select></label><label>Genre zoeken<input id="${prefix}-genre" type="search" placeholder="Bijv. rock, pop, country, Christmas" autocomplete="off"></label><label class="station-picker-choice">Te beheren zender<select id="${prefix}-station" class="select"><option value="">Selecteer een zender</option></select></label><span class="subtitle" id="${prefix}-count">${stations.length} zenders beschikbaar</span></div></section>`;
}
function bindStationPicker(prefix,onSelect) {
  const name=document.querySelector(`#${prefix}-name`),country=document.querySelector(`#${prefix}-country`),genre=document.querySelector(`#${prefix}-genre`),select=document.querySelector(`#${prefix}-station`),count=document.querySelector(`#${prefix}-count`);
  const update=()=>{
    const previous=select.value,q=name.value.trim().toLowerCase(),countryCodes=new Set(country.value?country.value.split(','):[]),tag=genre.value.trim().toLowerCase();
    const found=stations.filter(s=>(!q||s.name.toLowerCase().includes(q))&&(!countryCodes.size||countryCodes.has((s.country_code||'').toUpperCase()))&&(!tag||(s.tags||'').toLowerCase().includes(tag)));
    select.innerHTML=`<option value="">${found.length?'Selecteer een zender':'Geen zenders gevonden'}</option>${found.map(s=>`<option value="${escape(s.uuid)}">${escape(s.name)} · ${escape(s.country||s.country_code||'Onbekend land')}</option>`).join('')}`;
    select.value=found.some(s=>s.uuid===previous)?previous:'';
    count.textContent=`${found.length} zenders`;
    if(previous&&!select.value) onSelect('');
  };
  name.addEventListener('input',update);country.addEventListener('change',update);genre.addEventListener('input',update);select.addEventListener('change',()=>onSelect(select.value));update();
}
async function showDashboard() {
  const data = await api('/api/status');
  await loadStations();
  content.innerHTML = `${pageHeader('OVERZICHT','Radio voor Jellyfin','Beheer je catalogus en stel audioanalyse per zender zorgvuldig in.')}
    <div class="cards">${metric('Zenders',data.stations,'beschikbaar in de catalogus')}${metric('Favorieten',data.favorites,'persoonlijke selectie')}${metric('Backendgeheugen',`${Math.round(data.memory_bytes/1048576)} MB`,'actief gebruik')}${metric('Backend CPU',data.process_cpu_percent==null?'n.v.t.':`${Math.round(data.process_cpu_percent)}%`,'gemeten over 100 ms · 1 core = 100%')}</div>
    <section class="panel"><div class="panel-head"><div><h2>Jellyfin-afspeellijst</h2><p>Voeg deze M3U toe via Jellyfin Live TV als M3U-tuner.</p></div><a class="button primary" href="/stations.m3u" target="_blank" rel="noreferrer">Open stations.m3u</a></div><div class="health"><span class="status-dot"></span><div>Backend actief<small>SQLite database bereikbaar · ${data.goroutines} actieve routines</small></div></div></section>
    ${managedCountries.length?`<div class="quick-links">${managedCountries.map(c=>`<a class="quick-link" href="#country/${encodeURIComponent(c.code)}"><strong>${escape(c.name)}</strong><span>${c.station_count} radiozenders</span></a>`).join('')}</div>`:''}
    <section class="panel"><div class="panel-head"><div><h2>Onlangs geïmporteerd</h2><p>Een selectie uit de catalogus</p></div><a class="button quiet" href="#stations">Alles bekijken</a></div>${renderStations(stations.slice(0,6))}</section>`;
}
async function showStations(page) {
  const country = page.startsWith('country/') ? page.split('/')[1] : '';
  const favorite = page === 'favorites';
  const heading = country ? (managedCountries.find(item=>item.code===country)?.name || country) : favorite ? 'Favorieten' : page === 'custom' ? 'Eigen zenders' : page === 'genres' ? 'Genres' : page === 'countries' ? 'Andere landen' : 'Alle zenders';
  title.textContent = heading;
  await loadStations(`${country?`?country=${country}`:favorite?'?favorite=true':''}`);
  const filtered = page === 'custom' ? stations.filter(item => item.source === 'custom') : page === 'countries' ? stations.filter(item => !['NL','BE','ES'].includes(item.country_code)) : stations;
  content.innerHTML = `${pageHeader('CATALOGUS',heading,'Zoek, filter en beheer streams uit Radio Browser en je eigen collectie.', '<button class="button primary" id="add-station">＋ Eigen zender</button>')}
    <div class="toolbar"><input class="search" id="search" type="search" placeholder="Zoek zender, genre of label" aria-label="Zoek zender, genre of label"><select class="select" id="genre"><option value="">Alle zenderlabels</option>${[...new Set(filtered.flatMap(stationTags))].sort((a,b)=>a.localeCompare(b)).map(tag=>`<option>${escape(tag)}</option>`).join('')}</select><select class="select" id="language"><option value="">Alle talen</option>${[...new Set(filtered.flatMap(x=>(x.language||'').split(',').map(t=>t.trim()).filter(Boolean)))].sort().map(t=>`<option>${escape(t)}</option>`).join('')}</select><span class="subtitle" id="station-count"></span></div><div id="station-results"></div>`;
  const update = () => { const q=document.querySelector('#search').value.toLowerCase();const g=document.querySelector('#genre').value.toLowerCase();const language=document.querySelector('#language').value.toLowerCase();const found=filtered.filter(x=>(`${x.name} ${stationTags(x).join(' ')} ${x.language}`.toLowerCase().includes(q))&&(!g||stationTags(x).some(tag=>tag.toLowerCase()===g))&&(!language||(x.language||'').toLowerCase().includes(language)));document.querySelector('#station-count').textContent=`${found.length} zenders`;document.querySelector('#station-results').innerHTML=renderStations(found); };
  document.querySelector('#search').addEventListener('input',update);document.querySelector('#genre').addEventListener('change',update);document.querySelector('#language').addEventListener('change',update);document.querySelector('#add-station').onclick=()=>document.querySelector('#station-dialog').showModal();update();
}
async function showAudio() {
  await loadStations();
  const [audioStatus, globalSettings] = await Promise.all([api('/api/status'), api('/api/audio/settings')]);
  const slider = (name, label, value, step=1) => `<label>${label}<span class="setting-value" data-value-for="${name}">${value}%</span><input type="range" name="${name}" min="0" max="100" step="${step}" value="${value}"></label>`;
  content.innerHTML = `${pageHeader('AUDIO-INTELLIGENTIE','Audioanalyse','Analyseer fragmenten en stel live demping per zender in.')}
    <div class="audio-warning"><strong>Live DSP buffert ongeveer vijf seconden.</strong> Zet DSP per zender aan om herkende spraak en bevestigde herhalingen te dempen. Muziek en zang krijgen voorrang.</div>
    <section class="panel"><div class="panel-head"><div><h2>Algemene demping</h2><p>Doelvolume voor herkende niet-muziek, van 0% (stil) tot 100% (ongewijzigd).</p></div></div><form id="global-audio-settings" class="audio-settings"><label>Doelvolume niet-muziek <span class="setting-value" id="global-non-music-value">${Math.round(globalSettings.non_music_volume*100)}%</span><input type="range" name="non_music_volume" min="0" max="100" value="${Math.round(globalSettings.non_music_volume*100)}"></label><button class="button primary" type="submit">Algemene instelling opslaan</button><span id="global-audio-status" role="status"></span></form><p class="subtitle">Na opslaan geldt dit doelvolume bij de volgende verbinding met een DSP-zender.</p></section>
    ${stationPickerMarkup('audio')}
    <section class="panel"><div class="panel-head"><div><h2>Profiel per zender</h2><p>Schakel live demping in voor deze zender en stel de herkenningsdrempel en fades in.</p></div></div>
      <p id="audio-station-prompt" class="subtitle">Selecteer hierboven eerst een zender.</p>
      <form id="audio-settings" class="audio-settings" hidden>
        <label><input type="checkbox" name="dsp_enabled" ${audioStatus.audio_dsp_available ? '' : 'disabled'}> Live demping voor deze zender</label>
        ${slider('manual_gain','Vast niveau voor streamdiagnose',100)}<p class="subtitle">${audioStatus.audio_dsp_available ? 'De globale DSP-proef is ingeschakeld. Audio wordt opnieuw gecodeerd naar MP3; bij een fout vóór de start valt de proxy terug op de originele stream. Een fout tijdens het streamen kan de huidige afspeelsessie beëindigen.' : 'DSP staat globaal uit. Zet AUDIO_DSP_ENABLED=true om live demping beschikbaar te maken.'}</p>
        ${slider('speech_volume','Spraakvolume',20)}${slider('advertisement_volume','Reclamevolume',20)}${slider('news_volume','Nieuwsvolume',20)}${slider('presenter_volume','Presentatorvolume',20)}
        <label>Confidence threshold<input type="number" name="confidence_threshold" min="0" max="1" step="0.01" value="0.8"></label>
        <label>Fade-in (seconden)<input type="number" name="fade_in_seconds" min="0" max="10" step="0.1" value="1.5"></label>
        <label>Fade-out (seconden)<input type="number" name="fade_out_seconds" min="0" max="10" step="0.1" value="1.5"></label>
        <label class="setting-lock"><input type="checkbox" name="singing_protection" checked> Zang beschermen</label>
        <button class="button primary" type="submit">Profiel opslaan</button><span id="audio-settings-status" role="status"></span>
      </form></section>
    <section class="panel"><div class="panel-head"><div><h2>Fragmentanalyse</h2><p>WAV, MP3 of ander FFmpeg-formaat · maximaal 8 MiB</p></div></div><div class="upload"><label>Audiofragment<input id="audio-file" type="file" accept="audio/*,.wav,.mp3,.ogg,.flac"></label><button class="button primary" id="analyze">Analyse uitvoeren</button></div><p id="analysis-error" class="error"></p><div id="analysis-results"></div><p class="subtitle">Dit analyseert alleen het geüploade fragment en verandert geen stream.</p></section>
    <section class="panel"><div class="panel-head"><div><h2>Standaardbeleid</h2><p>YAMNet herkende in de vorige zangproef 1 van 19 vensters als zang; spraak over piano werd 8 van 8 keer als muziek geclassificeerd.</p></div></div><div class="cards">${metric('Muziek','100%','geen gainwijziging')}${metric('Zang','100%','beschermd')}${metric('Spraakdoel','20%','nog niet toegepast')}${metric('Fade','1,5 s','configureerbaar per zender')}</div></section>`;
  const form = document.querySelector('#audio-settings');
  const globalForm = document.querySelector('#global-audio-settings');
  globalForm.elements.non_music_volume.addEventListener('input',event=>{document.querySelector('#global-non-music-value').textContent=`${event.target.value}%`;});
  globalForm.addEventListener('submit',async event=>{
    event.preventDefault();const button=globalForm.querySelector('button[type="submit"]');button.disabled=true;
    try { const settings=await api('/api/audio/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({non_music_volume:Number(globalForm.elements.non_music_volume.value)/100})});globalForm.elements.non_music_volume.value=Math.round(settings.non_music_volume*100);document.querySelector('#global-audio-status').textContent='Algemene instelling opgeslagen.'; }
    catch(error) { document.querySelector('#global-audio-status').textContent=`Opslaan mislukt: ${error.message}`; }
    finally { button.disabled=false; }
  });
  const loadProfile = async uuid => {
    form.hidden=!uuid;document.querySelector('#audio-station-prompt').hidden=Boolean(uuid);
    if(!uuid)return;
    const profile = await api(`/api/stations/${encodeURIComponent(uuid)}/audio`);
    for (const name of ['speech_volume','advertisement_volume','news_volume','presenter_volume']) {
      form.elements[name].value = Math.round(profile[name] * 100);
      document.querySelector(`[data-value-for="${name}"]`).textContent = `${form.elements[name].value}%`;
    }
    for (const name of ['confidence_threshold','fade_in_seconds','fade_out_seconds']) form.elements[name].value = profile[name];
    form.elements.singing_protection.checked = profile.singing_protection;
    form.elements.dsp_enabled.checked = audioStatus.audio_dsp_available && profile.dsp_enabled;
    form.elements.manual_gain.value = Math.round(profile.manual_gain * 100);
    document.querySelector('[data-value-for="manual_gain"]').textContent = `${form.elements.manual_gain.value}%`;
  };
  bindStationPicker('audio',uuid=>loadProfile(uuid).catch(error=>alert(error.message)));
  form.addEventListener('input',event=>{if(event.target.type==='range')document.querySelector(`[data-value-for="${event.target.name}"]`).textContent=`${event.target.value}%`;});
  form.addEventListener('submit',async event=>{
    event.preventDefault();const button=form.querySelector('button[type="submit"]');button.disabled=true;
    try { const uuid=document.querySelector('#audio-station').value;if(!uuid)return;const value=Object.fromEntries(new FormData(form));value.analysis_only=true;value.filter_enabled=false;value.dsp_enabled=form.elements.dsp_enabled.checked;value.manual_gain=Number(value.manual_gain)/100;value.singing_protection=form.elements.singing_protection.checked;
      for(const name of ['speech_volume','advertisement_volume','news_volume','presenter_volume'])value[name]=Number(value[name])/100;
      for(const name of ['confidence_threshold','fade_in_seconds','fade_out_seconds'])value[name]=Number(value[name]);
      await api(`/api/stations/${encodeURIComponent(uuid)}/audio`,{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(value)});
      document.querySelector('#audio-settings-status').textContent='Opgeslagen. De instellingen gelden bij opnieuw verbinden.';
    } catch(error) { document.querySelector('#audio-settings-status').textContent=error.message; }
    finally { button.disabled=false; }
  });
  document.querySelector('#analyze').onclick=async()=>{const file=document.querySelector('#audio-file').files[0];if(!file)return;const button=document.querySelector('#analyze');button.disabled=true;button.textContent='Analyseren…';document.querySelector('#analysis-error').textContent='';try{const result=await api('/api/analyze',{method:'POST',headers:{'Content-Type':file.type||'application/octet-stream'},body:file});document.querySelector('#analysis-results').innerHTML=`<h3>Resultaat · ${result.frame_count} vensters · alleen analyse</h3>${result.frames.map(f=>`<div class="result-row"><span>${escape(f.category)}</span><div class="bar"><i style="width:${Math.round(f.confidence*100)}%"></i></div><span>${Math.round(f.confidence*100)}%</span></div>`).join('')}<p class="subtitle">CPU ${result.inference_cpu_seconds}s · inference ${result.inference_wall_seconds}s · piekgeheugen ${(result.peak_rss_kib/1024).toFixed(1)} MiB · gain altijd 100%</p>`;}catch(error){document.querySelector('#analysis-error').textContent=error.message;}finally{button.disabled=false;button.textContent='Analyse uitvoeren';}};
}
async function showDiagnostics() {
  await loadStations();
  const audioStatus=await api('/api/status');
  content.innerHTML=`${pageHeader('BEWAKING','Streamdiagnose','Selecteer eerst een zender. Vergelijk bron en proxy-uitvoer met of zonder DSP. De test verzamelt maximaal 8 KiB en slaat geen audio op.')}${stationPickerMarkup('diagnostic')}<div id="diagnostic-selected"><div class="panel empty">Selecteer hierboven een zender om de diagnosetests te tonen.</div></div>`;
  const renderSelected=async uuid=>{
    const root=document.querySelector('#diagnostic-selected'),station=stations.find(item=>item.uuid===uuid);
    if(!station){root.innerHTML='<div class="panel empty">Selecteer hierboven een zender om de diagnosetests te tonen.</div>';return;}
    const audioProfile=await api(`/api/stations/${encodeURIComponent(uuid)}/audio`);
    root.innerHTML=`<article class="panel diagnostic" data-id="${escape(station.uuid)}"><div class="panel-head"><div><h2>${escape(station.name)}</h2><p>${escape(station.country||station.country_code)} · catalogusformaat ${escape(station.codec||'onbekend')} ${station.bitrate?`· ${station.bitrate} kbps`:''}</p></div></div><div class="diagnostic-actions"><button class="button quiet check-stream" type="button">Bronverbinding</button><button class="button quiet probe-stream" type="button" data-dsp="false">Proxy zonder DSP</button><button class="button quiet probe-stream" type="button" data-dsp="true" ${audioStatus.audio_dsp_available?'':'disabled'}>Proxy met DSP</button><button class="button quiet" type="button" id="station-dsp-toggle" ${audioStatus.audio_dsp_available?'':'disabled'}>${audioProfile.dsp_enabled?'DSP voor deze zender uitschakelen':'DSP voor deze zender inschakelen'}</button></div><p class="subtitle" id="station-dsp-status">DSP staat voor deze zender ${audioProfile.dsp_enabled?'aan':'uit'}.${audioStatus.audio_dsp_available?'':' Globale DSP staat uit.'}</p><p class="subtitle check-result">${station.reachable==null?'Bron nog niet gecontroleerd':station.reachable?`Laatste broncontrole: HTTP bereikbaar · ${station.latency_ms} ms · ${escape(station.checked_at)}`:`Laatste broncontrole mislukt · ${escape(station.checked_at)}`}</p><p class="subtitle probe-result" data-probe-result="false">Uitvoer zonder DSP: nog niet getest</p><p class="subtitle probe-result" data-probe-result="true">Uitvoer met DSP: ${audioStatus.audio_dsp_available?'nog niet getest':'globaal uitgeschakeld (AUDIO_DSP_ENABLED=false)'}</p></article>`;
    const card=root.querySelector('.diagnostic');
    const dspToggle=card.querySelector('#station-dsp-toggle'),dspStatus=card.querySelector('#station-dsp-status');
    dspToggle.onclick=async()=>{dspToggle.disabled=true;try{audioProfile.dsp_enabled=!audioProfile.dsp_enabled;await api(`/api/stations/${encodeURIComponent(uuid)}/audio`,{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(audioProfile)});dspToggle.textContent=`DSP voor deze zender ${audioProfile.dsp_enabled?'uitschakelen':'inschakelen'}`;dspStatus.textContent=`DSP staat voor deze zender ${audioProfile.dsp_enabled?'aan':'uit'}.`;}catch(error){audioProfile.dsp_enabled=!audioProfile.dsp_enabled;dspStatus.textContent=error.message;}finally{dspToggle.disabled=!audioStatus.audio_dsp_available;}};
    card.querySelector('.check-stream').onclick=async event=>{const button=event.currentTarget,result=card.querySelector('.check-result');button.disabled=true;result.textContent='Bron controleert HTTP en eerste streambyte (maximaal 12 seconden)…';try{const health=await api(`/api/stations/${encodeURIComponent(uuid)}/check`,{method:'POST'});result.textContent=health.reachable?`Bron bereikbaar · HTTP ${health.status_code} · ${health.latency_ms} ms`:`Bron niet bereikbaar · ${health.error}`;result.style.color=health.reachable?'var(--green)':'var(--red)';}catch(error){result.textContent=error.message;}finally{button.disabled=false;}};
    card.querySelectorAll('.probe-stream').forEach(button=>button.onclick=async event=>{const control=event.currentTarget,dsp=control.dataset.dsp==='true',result=card.querySelector(`[data-probe-result="${dsp}"]`);control.disabled=true;result.textContent=`Proxy-uitvoer testen ${dsp?'met':'zonder'} DSP (maximaal 8 KiB, timeout 12 seconden)…`;try{const probe=await api(`/api/stations/${encodeURIComponent(uuid)}/probe`,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({dsp})});const parts=[`HTTP ${probe.status_code}`,probe.format||probe.content_type||'formaat onbekend',`${probe.sample_bytes} samplebytes`,`${probe.latency_ms} ms`];if(dsp)parts.push(probe.dsp_applied?`DSP toegepast · gain ${Math.round((probe.gain??1)*100)}%`:'DSP niet toegepast · passthrough fallback');if(probe.error)parts.push(probe.error);result.textContent=`Uitvoer ${dsp?'met':'zonder'} DSP: ${parts.join(' · ')}`;const audioLike=probe.format!=='HLS-playlist'&&(probe.content_type?.startsWith('audio/')||['MP3/MPEG audio','AAC audio','Ogg audio','FLAC audio','WAV audio'].includes(probe.format));const good=probe.status_code>=200&&probe.status_code<300&&probe.sample_bytes>=1024&&!probe.error&&audioLike;result.style.color=good&&(!dsp||probe.dsp_applied)?'var(--green)':good&&dsp?'#f4b942':'var(--red)';}catch(error){result.textContent=`Proxytest mislukt: ${error.message}`;result.style.color='var(--red)';}finally{control.disabled=false;}});
  };
  bindStationPicker('diagnostic',uuid=>renderSelected(uuid).catch(error=>alert(error.message)));
}
async function showImports() {
  const items=await api('/api/import-history');
  content.innerHTML=`${pageHeader('SYNCHRONISATIE','Importgeschiedenis','Radio Browser wordt standaard elke 24 uur gesynchroniseerd.')}${items.length?`<div class="panel">${items.map(x=>`<div class="result-row"><strong>${escape(x.country_code)}</strong><span>${escape(x.started_at)} · ${escape(x.status)} · ${x.imported} toegevoegd · ${x.skipped} overgeslagen</span><span>${x.error?'Fout':''}</span></div>`).join('')}</div>`:'<div class="panel empty">Nog geen import uitgevoerd. Start een synchronisatie om de catalogus te vullen.</div>'}`;
}
async function showCountryManagement() {
  const [countries, blocked] = await Promise.all([api('/api/countries'),api('/api/station-blocks')]);
  let available = [];
  let lookupError = '';
  try { available = await api('/api/radio-browser/countries'); }
  catch (error) { lookupError = error.message; }
  const draw = () => {
    const q=document.querySelector('#country-search').value.toLowerCase().trim();
    const options=available.filter(c=>`${c.name} ${c.code}`.toLowerCase().includes(q)&&!countries.some(current=>current.code===c.code));
    document.querySelector('#available-country').innerHTML=options.length?options.map(c=>`<option value="${escape(c.code)}">${escape(c.name)} (${escape(c.code)}) · ${c.station_count} zenders</option>`).join(''):'<option value="">Geen landen gevonden</option>';
  };
  content.innerHTML=`${pageHeader('BEHEER','Landen beheren','Voeg een land toe om de zenders uit Radio Browser te importeren. Een land verwijderen stopt toekomstige imports en verwijdert de geïmporteerde zenders uit je catalogus.')}
    <section class="panel"><div class="panel-head"><div><h2>Land toevoegen</h2><p>Zoek op landnaam of ISO-landcode; na toevoegen worden zenders direct geïmporteerd.</p></div></div>
      <div class="country-add"><input id="country-search" type="search" placeholder="Zoek land of landcode" aria-label="Zoek een land"><select id="available-country" class="select" aria-label="Beschikbare landen"></select><button id="country-add" class="button primary" type="button">Land toevoegen</button></div>
      <p class="subtitle" id="country-status">${lookupError?`Landenlijst niet geladen: ${escape(lookupError)}`:'Landenlijst van Radio Browser'}</p></section>
    <section class="panel"><div class="panel-head"><div><h2>Geïmporteerde landen</h2><p>Verwijderen wist de Radio Browser-zenders en hun favorieten/audio-instellingen. Eigen zenders blijven behouden.</p></div></div>
      <div class="management-list">${countries.length?countries.map(c=>`<div class="management-row"><div><strong>${escape(c.name)}</strong><span>${escape(c.code)} · ${c.station_count} zenders</span></div><button class="button quiet country-remove" type="button" data-country="${escape(c.code)}" data-country-name="${escape(c.name)}" data-count="${c.station_count}">Land verwijderen</button></div>`).join(''):'<p class="subtitle">Er zijn nog geen landen ingesteld.</p>'}</div></section>
    <section class="panel"><div class="panel-head"><div><h2>Verwijderde Radio Browser-zenders</h2><p>Deze zenders blijven na synchronisatie verwijderd. Je kunt ze hier herstellen.</p></div></div>
      <div class="management-list">${blocked.length?blocked.map(s=>`<div class="management-row"><div><strong>${escape(s.name)}</strong><span>${escape(s.country_code||'Onbekend land')}</span></div><button class="button quiet station-restore" type="button" data-station="${escape(s.uuid)}">Herstellen</button></div>`).join(''):'<p class="subtitle">Geen verwijderde zenders.</p>'}</div></section>`;
  draw();
  document.querySelector('#country-search').addEventListener('input',draw);
  document.querySelector('#country-add').addEventListener('click',async()=>{
    const country=available.find(c=>c.code===document.querySelector('#available-country').value); if(!country)return;
    const status=document.querySelector('#country-status'); const button=document.querySelector('#country-add'); button.disabled=true; status.textContent=`${country.name} toevoegen en zenders importeren…`;
    try { await api('/api/countries',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({code:country.code,name:country.name})}); const result=await api(`/api/countries/${encodeURIComponent(country.code)}/sync`,{method:'POST'}); await render(); document.querySelector('#country-status').textContent=`${country.name}: ${result.imported} zenders geïmporteerd, ${result.skipped} overgeslagen.`; }
    catch(error){status.textContent=`Toevoegen/importeren mislukt: ${error.message}`;button.disabled=false;}
  });
  document.querySelectorAll('.country-remove').forEach(button=>button.addEventListener('click',async()=>{
    const message=`Land ${button.dataset.countryName} verwijderen? Dit verwijdert ${button.dataset.count} geïmporteerde zenders uit deze catalogus. Eigen zenders blijven behouden.`;
    if(!confirm(message))return;
    try { await api(`/api/countries/${encodeURIComponent(button.dataset.country)}`,{method:'DELETE'}); await render(); } catch(error) { alert(error.message); }
  }));
  document.querySelectorAll('.station-restore').forEach(button=>button.addEventListener('click',async()=>{
    try { const restored=await api(`/api/station-blocks/${encodeURIComponent(button.dataset.station)}/restore`,{method:'POST'}); const active=await api('/api/countries'); if(active.some(country=>country.code===restored.country_code)) await api(`/api/countries/${encodeURIComponent(restored.country_code)}/sync`,{method:'POST'}); await render(); } catch(error) { alert(error.message); }
  }));
}
let learningPreviewURL;

function clearLearningPreview() {
  if (!learningPreviewURL) return;
  URL.revokeObjectURL(learningPreviewURL);
  learningPreviewURL = undefined;
}

async function showLearningRecordings() {
  const recordings = await api('/api/learning/candidates');
  if (route() !== 'learning-recordings') return;
  const typeLabels = {manual: 'Handmatige opname', transition: 'Herkenningsovergang', fingerprint: 'Herhaald fragment'};
  const list = recordings.map(recording => {
    const time = new Date(recording.first_seen).toLocaleString('nl-NL');
    const type = Object.hasOwn(typeLabels, recording.type) ? typeLabels[recording.type] : 'Leerfragment';
    return `<li><button class="button quiet" type="button" data-recording-id="${escape(recording.id)}">Luister</button><span>${escape(recording.station_name)} · ${escape(time)} · ${type}</span></li>`;
  }).join('');
  content.innerHTML = `<div class="page-heading"><h1>Leerfragmenten</h1></div><section class="panel">${list
    ? `<ul class="learning-recordings-list">${list}</ul><p id="learning-preview-status" class="subtitle" role="status"></p><audio id="learning-preview" controls hidden aria-label="Opgeslagen leerfragment"></audio>`
    : '<p>Nog geen leerfragmenten opgeslagen.</p>'}</section>`;
  const preview = document.querySelector('#learning-preview');
  if (!preview) return;
  const status = document.querySelector('#learning-preview-status');
  for (const button of content.querySelectorAll('[data-recording-id]')) {
    button.addEventListener('click', async () => {
      button.disabled = true;
      status.textContent = 'Fragment laden…';
      try {
        const wav = await api(`/api/learning/candidates/${encodeURIComponent(button.dataset.recordingId)}/preview`);
        if (!preview.isConnected || route() !== 'learning-recordings') return;
        clearLearningPreview();
        learningPreviewURL = URL.createObjectURL(wav);
        preview.src = learningPreviewURL;
        preview.hidden = false;
        status.textContent = button.nextElementSibling.textContent;
        await preview.play().catch(() => {});
      } catch (error) {
        status.textContent = error.message;
      } finally {
        button.disabled = false;
      }
    });
  }
}

async function render() {
  await loadCountryNav();
  const page = route();
  clearLearningPreview();
  showView(page);
  if (page === 'player') return;
  content.innerHTML = '<div class="loading">Laden…</div>';
  try { if(page==='dashboard')await showDashboard();else if(['stations','favorites','genres','countries','custom'].includes(page)||page.startsWith('country/'))await showStations(page);else if(page==='audio')await showAudio();else if(page==='learning-recordings')await showLearningRecordings();else if(page==='diagnostics')await showDiagnostics();else if(page==='imports')await showImports();else if(page==='country-management')await showCountryManagement();else await showStations('stations'); }
  catch(error){content.innerHTML=`<section class="panel error">${escape(error.message)}<p><button class="button" onclick="location.reload()">Opnieuw laden</button></p></section>`;}
}
document.querySelector('#sync-button').onclick=async event=>{const button=event.currentTarget;button.disabled=true;button.textContent='Synchroniseren…';try{await api('/api/sync',{method:'POST'});await render();}catch(error){alert(`Synchronisatie mislukt: ${error.message}`);}finally{button.disabled=false;button.textContent='Nu synchroniseren';}};
document.querySelector('#content').addEventListener('click',async event=>{const button=event.target.closest('[data-favorite]');if(!button)return;try{await api(`/api/stations/${encodeURIComponent(button.dataset.favorite)}/favorite`,{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({favorite:!button.classList.contains('on')})});await render();}catch(error){alert(error.message);}});
document.querySelector('#content').addEventListener('click',async event=>{const button=event.target.closest('[data-excluded]');if(!button)return;const card=button.closest('.station');const excluded=!card.classList.contains('excluded');try{await api(`/api/stations/${encodeURIComponent(button.dataset.excluded)}/excluded`,{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({excluded})});await render();}catch(error){alert(error.message);}});
content.addEventListener('click',async event=>{const button=event.target.closest('[data-delete-station]');if(!button)return;const name=button.dataset.stationName;const detail=button.dataset.source==='radio-browser'?'De zender wordt ook bij volgende synchronisaties overgeslagen en is te herstellen via Landen beheren.':'Deze eigen zender wordt definitief verwijderd.';if(!confirm(`Zender ${name} verwijderen? ${detail}`))return;try{await api(`/api/stations/${encodeURIComponent(button.dataset.deleteStation)}`,{method:'DELETE'});await render();}catch(error){alert(error.message);}});
content.addEventListener('click',event=>{
  const button=event.target.closest('[data-streamdata]');if(!button)return;
  const uuid=button.dataset.streamdata;
  if (uuid !== currentStationId) playStation(button.closest('.station').querySelector('[data-play]'));
  openPlayer('#radio-metadata-title');
});
content.addEventListener('click',async event=>{
  const button=event.target.closest('[data-metadata-record]');if(!button)return;
  const station=button.dataset.metadataRecord,stopping=button.dataset.recording==='true';
  if (station !== currentStationId) {
    const play = button.closest('.station').querySelector('[data-play]');
    playStation(play);
  }
  button.disabled=true;
  try {
    const payload=await api(`/api/stations/${encodeURIComponent(station)}/metadata-recording`,{method:stopping?'DELETE':'POST'});
    if(stopping){const download=document.createElement('a');download.href=URL.createObjectURL(new Blob([payload],{type:'application/x-ndjson'}));download.download=`radio-metadata-${new Date().toISOString().replace(/[:.]/g,'-')}.jsonl`;download.click();setTimeout(()=>URL.revokeObjectURL(download.href),1000);}
    showMetadataRecording(!stopping,station);
  } catch(error) { button.closest('.station').querySelector('[data-metadata-record-status]').textContent=error.message; }
  finally { button.disabled=false; }
});
function selectStation(event) {
  const button = event.target.closest('[data-play]');
  if (!button) return;
  if (button.dataset.play !== currentStationId) playStation(button);
  openPlayer(content.contains(button) ? '#radio-heading' : '');
}

content.addEventListener('click', selectStation);
countryNav.addEventListener('click', selectStation);
favoriteNav.addEventListener('click', selectStation);

function renderPlayerStation(station) {
  document.querySelector('#radio-player-name').textContent = station.name;
  const country = managedCountries.find(item => item.code === station.country_code)?.name || station.country || station.country_code;
  document.querySelector('#radio-station-location').textContent = [country, station.language].filter(Boolean).join(' · ');
  document.querySelector('#radio-station-genres').innerHTML = stationTags(station).map(tag => `<span class="tag">${escape(tag)}</span>`).join('');

  const logo = document.querySelector('#radio-station-logo');
  const fallback = document.createElement('span');
  fallback.className = 'logo logo-fallback';
  fallback.textContent = Array.from(station.name.trim()).slice(0, 2).join('').toLocaleUpperCase() || 'SR';
  logo.replaceChildren(fallback);
  const wordmark = station.uuid === '05de6435-c3e5-4d69-8e55-8818e48bbbd3' ? '/brand/jolene-country-radio.svg' : '';
  const logoURL = station.logo_url?.trim() || wordmark;
  if (logoURL) {
    const image = document.createElement('img');
    image.className = 'logo';
    image.alt = '';
    image.referrerPolicy = 'no-referrer';
    image.addEventListener('error', () => image.replaceWith(fallback), {once: true});
    if (logoURL === wordmark) image.classList.add('station-wordmark');
    image.src = logoURL;
    logo.replaceChildren(image);
  }

  const website = document.querySelector('#radio-station-website');
  let url;
  try { url = new URL(station.homepage); } catch {}
  website.hidden = !url || !['https:', 'http:'].includes(url.protocol);
  if (website.hidden) website.removeAttribute('href');
  else website.href = url.href;
}

async function playStation(button) {
  stopLiveAnalysis();
  radioPlayer.hidden = false;
  document.querySelector('#radio-metadata-rows').scrollTop = 0;
  radioIdle.hidden = true;
  currentStationId = button.dataset.play;
  const station = catalog?.find(item => item.uuid === currentStationId) || stations.find(item => item.uuid === currentStationId);
  renderPlayerStation(station || {name: button.dataset.stationName});
  updateCurrentStation();
  latestLiveFrame = null;
  lastDuckingDetection = undefined;
  duckingHistory.parentElement.open = false;
  streamHeaders.parentElement.open = false;
  renderDuckingHistory(currentStationId);
  document.querySelector('#radio-track-artist').textContent = '';
  document.querySelector('#radio-track-title').textContent = 'Wachten op streammetadata…';
  const sourceURL = document.querySelector('#radio-stream-url');
  sourceURL.textContent = 'Stream-URL ophalen…';
  sourceURL.removeAttribute('title');
  document.querySelector('#radio-copy-url').disabled = true;
  document.querySelector('#radio-copy-status').textContent = '';
  renderedMetadata = '';
  renderStreamHeaders([]);
  currentStreamMetadata = undefined;
  icyDetections.clear();
  streamDetails.innerHTML = '<p class="loading" role="status">Streamdata laden…</p>';
  metadataCopy.disabled = !duckingHistory.firstElementChild;
  document.querySelector('#radio-metadata-copy-status').textContent = '';
  startMetadataPolling(currentStationId);
  loadRadioDSP(currentStationId).catch(error => { document.querySelector('#radio-dsp-status').textContent = error.message; });
  currentCodec = button.dataset.codec || '';
  document.querySelector('#radio-player-format').textContent = `Catalogusformaat: ${currentCodec || 'onbekend'}${button.dataset.bitrate && button.dataset.bitrate !== '0' ? ` · ${button.dataset.bitrate} kbps` : ''}`;
  setRadioStatus('Verbinding maken met de stream…');
  document.querySelector('#radio-signal-status').textContent = 'Wachten op audiodata';
  clearTimeout(streamStartTimer);
  radioAudio.src = `/stream/${encodeURIComponent(button.dataset.play)}`;
  clearMeters();
  startSpectrum();
  streamStartTimer=setTimeout(()=>{if(radioAudio.readyState<1)setRadioStatus('Na 10 seconden nog geen afspeelbare audio ontvangen. De stream kan offline zijn of een browser-incompatibel formaat gebruiken.');},10000);
  try { await radioAudio.play(); }
  catch { setRadioStatus('Afspelen startte niet. Controleer de browsermelding of druk op Afspelen.'); }
}
function setRadioStatus(message) { document.querySelector('#radio-player-status').textContent=message; }
function renderStreamHeaders(rows) {
  const markup = rows.filter(([, value]) => value).map(([label, value]) => metadataRow(label, value)).join('');
  if (renderedHeaders === markup) return;
  streamHeaders.innerHTML = markup;
  streamHeaders.parentElement.hidden = !markup;
  renderedHeaders = markup;
}

function renderStreamMetadata(metadata) {
  currentStreamMetadata = metadata;
  const headers = metadata.headers || {};
  const header = name => {
    const entry = Object.entries(headers).find(([key]) => key.toLowerCase() === name);
    return Array.isArray(entry?.[1]) ? entry[1].join(', ') : entry?.[1] || '';
  };
  const name = header('icy-name').trim();
  const description = header('icy-description').trim();
  const genre = header('icy-genre').trim();
  const sameName = name && description && name.toLocaleLowerCase() === description.toLocaleLowerCase();
  const rawICY = metadata.raw_icy || '';
  const titleFromICY = raw => raw?.match(/(?:^|;)\s*StreamTitle\s*=\s*(['"])(.*?)\1(?=\s*(?:;|$))/i)?.[2]?.trim();
  const streamTitle = titleFromICY(rawICY) || metadata.title || '';
  const history = Array.isArray(metadata.icy_history) ? metadata.icy_history : [];
  const icyRows = history.length ? [...history].reverse().map(item => {
    const time = item.time ? new Date(item.time).toLocaleString() : 'tijd onbekend';
    const recognized = icyDetections.get(icyEntryKey(item));
    let recognition = 'Onbekend';
    if (recognized) {
      const {label, confidence, capturedAt} = recognized.latest;
      const timestamp = new Date(capturedAt).toLocaleTimeString('nl-NL');
      const earlier = [...recognized.categories].filter(category => category !== label);
      recognition = `${label} · Modelscore ${Math.round(confidence * 100)}% · sinds ${timestamp}`;
      if (earlier.length) recognition += ` · Eerder: ${earlier.join(', ')}`;
    }
    const title = titleFromICY(item.raw_icy) || item.streamtitle?.trim() || '';
    return [`ICY · ${time}`, `raw_icy: ${item.raw_icy || ''}${title ? ` | streamtitle: ${title}` : ''}`, recognition, title];
  }) : [['ICY', `raw_icy: ${rawICY || 'Nog geen ICY-metadatablok ontvangen'} | streamtitle: ${streamTitle || 'Geen StreamTitle ontvangen'}`, rawICY || streamTitle ? 'Onbekend' : '', streamTitle]];
  renderStreamHeaders([
    ['HTTP-respons', [metadata.protocol, metadata.status].filter(Boolean).join(' ')],
    ['Name', name],
    ...(!sameName && description ? [['Description', description]] : []),
    ['Genre', genre],
    ...Object.entries(headers).filter(([key]) => !['icy-name', 'icy-description', 'icy-genre'].includes(key.toLowerCase())).sort(([a], [b]) => a.localeCompare(b)).flatMap(([key, values]) =>
      (Array.isArray(values) ? values : [values]).map(value => [key, value])),
  ]);
  const markup = icyRows.map(([label, value, recognition, title], index) =>
    metadataRow(label, value, recognition, '', manualCategoryPicker(title, index))).join('');
  // Keep the native picker open while live streamdata arrives.
  if (renderedMetadata !== markup && !streamDetails.contains(document.activeElement)) {
    streamDetails.innerHTML = markup;
    renderedMetadata = markup;
  }
  metadataCopy.disabled = false;
}

metadataCopy.addEventListener('click', async () => {
  const rows = [...document.querySelectorAll('#radio-metadata-rows .radio-metadata-row')];
  const text = rows.map(row => {
    const label = row.querySelector('dt').textContent;
    const value = row.querySelector('code').textContent;
    const recognition = row.querySelector('.radio-metadata-recognition')?.textContent;
    const category = row.querySelector('.stream-title-category-select')?.value;
    const manualLabel = category ? `Handmatig label: ${streamTitleCategories[category]}` : '';
    return [`${label}: ${value}`, recognition, manualLabel].filter(Boolean).join('\n  ');
  }).join('\n') + '\n';
  const slug = document.querySelector('#radio-player-name').textContent.normalize('NFKD').replace(/[\u0300-\u036f]/g, '').replace(/[^\w.-]+/g, '-').replace(/^-|-$/g, '') || 'stream';
  const blobURL = URL.createObjectURL(new Blob([text], {type:'text/plain;charset=utf-8'}));
  const download = document.createElement('a');
  download.href = blobURL;
  download.download = `radio-stream-metadata-${slug}-${new Date().toISOString().replace(/[:.]/g, '-')}.txt`;
  download.click();
  setTimeout(() => URL.revokeObjectURL(blobURL), 1000);
  let copied = false;
  try { await navigator.clipboard.writeText(text); copied = true; } catch {}
  document.querySelector('#radio-metadata-copy-status').textContent = copied ? 'Gekopieerd en bestand gemaakt.' : 'Bestand gemaakt; klembord niet beschikbaar.';
});

function startMetadataPolling(uuid) {
  clearInterval(metadataPollTimer);
  const refresh=async()=>{
    try {
      const metadata=await api(`/api/stations/${encodeURIComponent(uuid)}/metadata`);
      if(uuid!==currentStationId)return;
      showMetadataRecording(metadata.recording,uuid);
      document.querySelector('#radio-track-artist').textContent=metadata.artist||'';
      document.querySelector('#radio-track-title').textContent=metadata.track||metadata.title||(metadata.received?'Geen songtitel in de streammetadata':'Wachten op ICY-streammetadata…');
      const suppressed = Boolean(metadata.suppressed || metadata.marker_suppressed);
      radioPlayer.classList.toggle('radio-suppressed', suppressed);
      document.querySelector('#radio-suppression-status').textContent = metadata.suppressed ? 'Volume verlaagd' : metadata.marker_suppressed ? 'Stream onderdrukt' : '';
      const url=document.querySelector('#radio-stream-url');url.textContent=metadata.stream_url||'Geen stream-URL beschikbaar';url.title=metadata.stream_url||'';
      document.querySelector('#radio-copy-url').disabled=!metadata.stream_url;
      renderStreamMetadata(metadata);
    } catch {
      if (uuid !== currentStationId) return;
      document.querySelector('#radio-track-artist').textContent='';
      document.querySelector('#radio-track-title').textContent='Streammetadata niet beschikbaar';
      radioPlayer.classList.remove('radio-suppressed');
      document.querySelector('#radio-suppression-status').textContent = '';
      renderedMetadata = '';
      currentStreamMetadata = undefined;
      renderStreamHeaders([]);
      streamDetails.innerHTML = metadataRow('Metadata', 'Niet beschikbaar');
      metadataCopy.disabled = !duckingHistory.firstElementChild;
      const sourceURL=document.querySelector('#radio-stream-url');
      sourceURL.textContent='Bronstream niet beschikbaar';
      sourceURL.removeAttribute('title');
      document.querySelector('#radio-copy-url').disabled=true;
    }
  };
  refresh();metadataPollTimer=setInterval(refresh,3000);
}

function showMetadataRecording(active, uuid=currentStationId) {
  const recording=Boolean(active);
  document.querySelectorAll('[data-metadata-record]').forEach(button=>{
    if(button.dataset.metadataRecord!==uuid)return;
    button.dataset.recording=String(recording);
    button.textContent=recording?'Opname stoppen':'Metadata opnemen';
    const status=document.querySelector(`[data-metadata-record-status="${CSS.escape(uuid)}"]`);
    if(status)status.textContent=recording?'Opname loopt':'';
  });
}
function startSpectrum() {
  const AudioContextClass=window.AudioContext||window.webkitAudioContext;
  if(!AudioContextClass){document.querySelector('#radio-signal-status').textContent='Live spectrum niet beschikbaar in deze browser';return;}
  try {
    if(!audioContext){audioContext=new AudioContextClass();analyser=audioContext.createAnalyser();analyser.fftSize=2048;audioSource=audioContext.createMediaElementSource(radioAudio);audioSource.connect(analyser);analyser.connect(audioContext.destination);}
    audioContext.resume().catch(()=>{});cancelAnimationFrame(spectrumFrame);drawSpectrum();
  } catch { document.querySelector('#radio-signal-status').textContent='Live spectrum niet beschikbaar'; }
}
async function loadRadioDSP(uuid) {
  const [profile,status]=await Promise.all([api(`/api/stations/${encodeURIComponent(uuid)}/audio`),api('/api/status')]);
  if(uuid!==currentStationId)return;
  audioProfile=profile;dspAvailable=status.audio_dsp_available;renderRadioDSP();
}
function renderRadioDSP() {
  const button=document.querySelector('#radio-dsp-toggle'),status=document.querySelector('#radio-dsp-status');
  if(!audioProfile){button.disabled=true;button.textContent='DSP laden…';return;}
  const enabled=audioProfile.dsp_enabled;
  button.disabled=false;button.textContent=enabled?'DSP uitzetten':'DSP aanzetten';button.setAttribute('aria-pressed',String(enabled));
  status.textContent=`Zenderinstelling: ${enabled?'aan':'uit'}${dspAvailable?'':' · globale DSP uit, instelling is nog niet actief'}`;
}
document.querySelector('#radio-dsp-toggle').addEventListener('click',async event=>{
  const button=event.currentTarget;
  if(!currentStationId||!audioProfile)return;
  button.disabled=true;let saveError='';const station=currentStationId;
  const wasPlaying=!radioAudio.paused,next={...audioProfile,dsp_enabled:!audioProfile.dsp_enabled};
  try {
    const saved=await api(`/api/stations/${encodeURIComponent(station)}/audio`,{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(next)});
    if(station!==currentStationId)return;
    audioProfile=saved;
    renderRadioDSP();
    if(radioAudio.currentSrc){setRadioStatus('DSP-instelling opgeslagen; opnieuw verbinden…');radioAudio.pause();radioAudio.src=`/stream/${encodeURIComponent(station)}`;radioAudio.load();if(wasPlaying)try{await radioAudio.play();}catch{setRadioStatus('DSP-instelling opgeslagen. Druk op Afspelen om opnieuw te verbinden.');}}
  } catch(error) { if(station===currentStationId){audioProfile={...next,dsp_enabled:!next.dsp_enabled};saveError=error.message;} }
  finally { renderRadioDSP();if(saveError)document.querySelector('#radio-dsp-status').textContent=`Opslaan mislukt: ${saveError}`; }
});
document.querySelector('#radio-copy-url').addEventListener('click',async()=>{
  const url=document.querySelector('#radio-stream-url').textContent;
  if(!url||url==='Geen stream-URL beschikbaar')return;
  try { await navigator.clipboard.writeText(url);document.querySelector('#radio-copy-status').textContent='Stream-URL gekopieerd'; }
  catch { document.querySelector('#radio-copy-status').textContent='Kopiëren niet beschikbaar; selecteer de URL handmatig.'; }
});
const learnButton = document.querySelector('#radio-learn');
const learnStatus = document.querySelector('#radio-learn-status');
const learnRetry = document.querySelector('#radio-learn-retry');
let pendingLearningRecording;
let learningRecording;

function recordingWAV(chunks, byteLength) {
  const header = new ArrayBuffer(44);
  const view = new DataView(header);
  const writeText = (offset, value) => {
    for (let i = 0; i < value.length; i++) view.setUint8(offset + i, value.charCodeAt(i));
  };
  writeText(0, 'RIFF');
  view.setUint32(4, 36 + byteLength, true);
  writeText(8, 'WAVEfmt ');
  view.setUint32(16, 16, true);
  view.setUint16(20, 1, true);
  view.setUint16(22, 1, true);
  view.setUint32(24, 16000, true);
  view.setUint32(28, 32000, true);
  view.setUint16(32, 2, true);
  view.setUint16(34, 16, true);
  writeText(36, 'data');
  view.setUint32(40, byteLength, true);
  return new Blob([header, ...chunks], {type: 'audio/wav'});
}

async function saveLearningRecording() {
  if (!pendingLearningRecording) return;
  const recording = pendingLearningRecording;
  learnRetry.hidden = true;
  learnButton.disabled = true;
  learnStatus.textContent = 'Leerfragment opslaan…';
  try {
    await api('/api/learning/recordings', {
      method: 'POST',
      headers: {
        'Content-Type': 'audio/wav',
        'X-Station-ID': recording.station,
        'X-Recording-Started': recording.startedAt.toISOString(),
      },
      body: recording.wav,
    });
    pendingLearningRecording = undefined;
    learnStatus.textContent = `Opgeslagen · ${recording.duration} s, inclusief ${recording.preRoll} s aanloop.`;
    learnButton.disabled = !captureNode || radioAudio.paused;
    if (route() === 'learning-recordings') showLearningRecordings().catch(() => {});
  } catch (error) {
    learnStatus.textContent = `Opslaan mislukt · ${error.message}`;
    learnRetry.hidden = false;
  }
}

learnRetry.addEventListener('click', saveLearningRecording);

function finishLearningRecording(recording) {
  if (learningRecording !== recording) return;
  clearTimeout(recording.stopTimer);
  learningRecording = undefined;
  learnButton.textContent = 'Leer';
  if (!recording.bytes) {
    learnButton.disabled = !captureNode || radioAudio.paused;
    learnStatus.textContent = 'Geen audio ontvangen voor dit fragment.';
    return;
  }
  const duration = recording.bytes / 32000;
  pendingLearningRecording = {
    station: recording.station,
    startedAt: new Date(recording.startedAt.getTime() - recording.preRoll * 1000),
    duration: duration.toLocaleString('nl-NL', {maximumFractionDigits: 1}),
    preRoll: recording.preRoll.toLocaleString('nl-NL', {maximumFractionDigits: 1}),
    wav: recordingWAV(recording.chunks, recording.bytes),
  };
  saveLearningRecording();
}

function stopLearningRecording() {
  const recording = learningRecording;
  if (!recording || recording.stopTimer) return;
  learnButton.disabled = true;
  learnStatus.textContent = 'Fragment afronden…';
  recording.node.port.postMessage('stop-recording');
  recording.stopTimer = setTimeout(() => finishLearningRecording(recording), 2000);
}

learnButton.addEventListener('click', () => {
  if (learningRecording) {
    stopLearningRecording();
    return;
  }
  if (!captureNode || radioAudio.paused || pendingLearningRecording) return;
  learningRecording = {node: captureNode, station: currentStationId, startedAt: new Date(), chunks: [], bytes: 0, preRoll: 0};
  captureNode.port.postMessage('start-recording');
  learnButton.textContent = 'Stop opname';
  learnStatus.textContent = 'Opname loopt.';
});

function handleCaptureMessage(node, data) {
  if (data instanceof ArrayBuffer) {
    if (node !== captureNode) return;
    if (!learningRecording && !pendingLearningRecording && !radioAudio.paused) {
      learnButton.disabled = false;
      if (!learnStatus.textContent.startsWith('Opgeslagen')) learnStatus.textContent = 'Neem op met maximaal 10 seconden aanloop.';
    }
    classifyLiveFrame(data);
    return;
  }

  const recording = learningRecording;
  if (recording?.node !== node) return;
  if (data.type === 'recording-chunk') {
    if (!recording.chunks.length) recording.preRoll = data.samples.byteLength / 32000;
    recording.bytes += data.samples.byteLength;
    recording.chunks.push(data.samples);
  } else if (data.type === 'recording-complete') {
    finishLearningRecording(recording);
  }
}

async function startLiveAnalysis() {
  if (!audioContext?.audioWorklet || typeof AudioWorkletNode === 'undefined') {
    document.querySelector('#live-detection').textContent = 'Liveclassifier niet beschikbaar in deze browser';
    if (!learningRecording) learnStatus.textContent = 'Opnemen is niet beschikbaar in deze browser.';
    return;
  }
  try {
    captureModule ||= audioContext.audioWorklet.addModule('/audio-capture.js');
    await captureModule;
    if(radioAudio.paused||captureNode)return;
    captureNode=new AudioWorkletNode(audioContext,'radio-pcm-capture',{numberOfInputs:1,numberOfOutputs:1,outputChannelCount:[1]});
    const node = captureNode;
    captureSilence = audioContext.createGain();
    captureSilence.gain.value = 0;
    audioSource.connect(node);
    node.connect(captureSilence);
    captureSilence.connect(audioContext.destination);
    node.port.onmessage = event => handleCaptureMessage(node, event.data);
    document.querySelector('#live-detection').textContent='Liveanalyse actief · YAMNet';
  } catch {
    document.querySelector('#live-detection').textContent = 'Liveanalyse niet beschikbaar';
    if (!learningRecording) learnStatus.textContent = 'Opnemen is niet beschikbaar.';
  }
}
async function classifyLiveFrame(buffer) {
  if(classifying||radioAudio.paused)return;
  classifying=true;const generation=liveGeneration,station=currentStationId;
  const capturedAt = Date.now();
  const metadata = currentStreamMetadata;
  const entry = metadata?.icy_history?.at(-1);
  const icyKey = entry?.raw_icy === metadata?.raw_icy ? icyEntryKey(entry) : '';
  try {
    const result=await api('/api/live-classify',{method:'POST',headers:{'Content-Type':'application/octet-stream'},body:buffer});
    if(radioAudio.paused||generation!==liveGeneration||station!==currentStationId)return;
    latestLiveFrame=result;
    recordDuckingDetection(result);
    if (recordIcyDetection(icyKey, result, capturedAt) && currentStreamMetadata) renderStreamMetadata(currentStreamMetadata);
    const category = audioCategory(result.category);
    const top=(result.top_labels||[]).slice(0,3).map(item=>`${item.name} ${Math.round(item.score*100)}%`).join(' · ');
    document.querySelector('#live-detection').textContent=`Model ziet ${category.toLowerCase()} · ${Math.round(result.confidence*100)}%${top?` · Mogelijke model-labels: ${top}`:''}`;
    for(const key of ['music','speech','singing','jingle'])setMeter(key,result[key]);
  } catch(error) {
    if(generation!==liveGeneration||station!==currentStationId)return;
    lastDuckingDetection = undefined;
    document.querySelector('#live-detection').textContent=`Liveanalyse niet beschikbaar · ${error.message}`;
  }
  finally { if(generation===liveGeneration)classifying=false; }
}
function setMeter(name,value) {
  const percent=Math.max(0,Math.min(1,Number(value)||0))*100;
  const fill=document.querySelector(`#meter-${name}`);fill.style.width=`${percent}%`;fill.parentElement.setAttribute('aria-valuenow',String(Math.round(percent)));
  document.querySelector(`#value-${name}`).textContent=`${Math.round(percent)}%`;
}
function clearMeters() { for(const name of ['bass','mid','high'])setMeter(name,0);for(const name of ['speech','singing','jingle','music']){setMeter(name,0);document.querySelector(`#value-${name}`).textContent='—';}document.querySelector('#live-detection').textContent='Wachten op audioanalyse…'; }
function stopLiveAnalysis() {
  liveGeneration++;
  stopLearningRecording();
  if (captureNode) {
    audioSource.disconnect(captureNode);
    captureNode.disconnect();
    captureSilence.disconnect();
  }
  captureNode = null;
  captureSilence = null;
  learnButton.disabled = true;
  latestLiveFrame = null;
  classifying = false;
  lastDuckingDetection = undefined;
}
function drawSpectrum() {
  if(!analyser||radioAudio.paused)return;
  const bins=new Uint8Array(analyser.frequencyBinCount);analyser.getByteFrequencyData(bins);
  let peak=0;for(const value of bins)peak=Math.max(peak,value);
  updateMixer(bins);
  document.querySelector('#radio-signal-status').textContent=peak>7?`Audiosignaal binnen · piek ${Math.round(peak/255*100)}%`:'Geen of zeer zacht audiosignaal';
  spectrumFrame=requestAnimationFrame(drawSpectrum);
}
function updateMixer(bins) {
  const rate=audioContext.sampleRate,fft=analyser.fftSize;
  for(const [name,low,high] of [['bass',30,250],['mid',250,4000],['high',4000,12000]]){
    const first=Math.max(0,Math.floor(low*fft/rate)),last=Math.min(bins.length-1,Math.ceil(high*fft/rate));let total=0;
    for(let i=first;i<=last;i++)total+=bins[i];
    setMeter(name,total/Math.max(1,last-first+1)/255);
  }
  const width=spectrogramCanvas.width,height=spectrogramCanvas.height,bars=48,step=width/bars;
  spectrogram.clearRect(0,0,width,height);
  for(let i=0;i<bars;i++){
    const frequency=35*Math.pow(12000/35,i/(bars-1)),bin=Math.min(bins.length-1,Math.round(frequency*fft/rate)),level=bins[bin]/255;
    const barHeight=Math.max(2,level*(height-3));
    spectrogram.fillStyle=`hsl(${214-level*64} 78% ${28+level*30}%)`;
    spectrogram.fillRect(i*step+1,height-barHeight,Math.max(1,step-2),barHeight);
  }
}
radioAudio.addEventListener('playing', () => { clearTimeout(streamStartTimer);setRadioStatus('');startSpectrum();startLiveAnalysis(); });
radioAudio.addEventListener('waiting', () => setRadioStatus('Bufferen: de browser wacht op audiogegevens…'));
radioAudio.addEventListener('stalled', () => setRadioStatus('Geen nieuwe audiogegevens ontvangen; de stream kan stilvallen.'));
radioAudio.addEventListener('canplay', () => { clearTimeout(streamStartTimer);setRadioStatus('Browser heeft afspeelbare audiogegevens ontvangen.'); });
radioAudio.addEventListener('pause', () => { if(!radioAudio.ended&&!radioAudio.error&&radioAudio.currentSrc)setRadioStatus('Afspelen gepauzeerd.');cancelAnimationFrame(spectrumFrame);if(radioAudio.paused)stopLiveAnalysis(); });
radioAudio.addEventListener('ended', stopLiveAnalysis);
radioAudio.addEventListener('error', () => {
  stopLiveAnalysis();
  cancelAnimationFrame(spectrumFrame);
  const code=radioAudio.error?.code;
  const detail=code===4?`Browser ondersteunt het ontvangen formaat niet (${currentCodec||'codec onbekend'}).`:code===3?`Browser kan de audiogegevens niet decoderen (${currentCodec||'codec onbekend'}).`:code===2?'Netwerkfout tijdens het ophalen van de stream.':code===1?'Stream is afgebroken.':'Stream kon niet worden gestart.';
  setRadioStatus(detail);document.querySelector('#radio-signal-status').textContent='Geen gedecodeerd audiosignaal';
});
document.querySelector('#station-form').addEventListener('submit',async event=>{event.preventDefault();const form=event.currentTarget;const data=Object.fromEntries(new FormData(form));data.country_code=(data.country_code||'').toUpperCase();try{await api('/api/stations',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(data)});form.reset();document.querySelector('#station-dialog').close();await render();}catch(error){document.querySelector('#form-error').textContent=error.message;}});
window.addEventListener('hashchange',render);render();
