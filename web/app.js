'use strict';

const apiBase = '/v0/management/provider-balances';
const $ = id => document.getElementById(id);
let managementKey = '';
let providers = [];
let editingID = '';
let deletingID = '';
let generation = 0;
const refreshing = new Set();
const refreshErrors = new Map();
const currencyOptions = new Map(Array.from($('provider-currency').options, option => [option.value, option]).filter(([code]) => code && code !== 'CUSTOM'));

function notice(message = '', failure = false) {
  $('notice').textContent = message;
  $('notice').hidden = !message;
  $('notice').classList.toggle('failure', failure);
}

async function api(path, method = 'GET', body) {
  const response = await fetch(apiBase + path, {
    method,
    headers: {Authorization: 'Bearer ' + managementKey, 'Content-Type': 'application/json'},
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'omit',
    cache: 'no-store',
    signal: AbortSignal.timeout(50000),
  });
  if (response.status === 401 || response.status === 403) {
    throw new Error('Доступ к управлению отклонён. Проверьте ключ управления и разрешение удалённого доступа CLIProxyAPI.');
  }
  let data;
  try { data = await response.json(); }
  catch { throw new Error('CLIProxyAPI вернул некорректный ответ. Проверьте, включён ли плагин.'); }
  if (!response.ok) throw new Error(data.error || 'Не удалось выполнить запрос');
  return data;
}

function showTab(name) {
  for (const tab of ['balances', 'settings']) {
    $(tab).hidden = tab !== name;
    $(tab + '-tab').classList.toggle('active', tab === name);
    if (tab === name) $(tab + '-tab').setAttribute('aria-current', 'page');
    else $(tab + '-tab').removeAttribute('aria-current');
  }
}

function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function render() {
  $('cards').replaceChildren();
  $('provider-list').replaceChildren();
  if (!providers.length) {
    const empty = element('div', 'empty');
    empty.append(element('h3', '', 'Поставщиков пока нет'), element('p', '', 'Добавьте имя, URL сайта и токен во вкладке «Настройки».'));
    $('cards').append(empty);
    $('provider-list').append(element('p', 'muted', 'Список пуст'));
  }
  for (const provider of providers) {
    const card = element('article', 'card');
    const busy = refreshing.has(provider.id);
    card.dataset.providerId = provider.id;
    card.classList.toggle('loading', busy);
    card.setAttribute('aria-busy', String(busy));
    const content = element('div', 'card-content');
    content.inert = busy;
    const heading = element('div', 'card-heading');
    const identity = element('div');
    const link = element('a', 'provider-url', provider.url);
    link.href = provider.url;
    link.target = '_blank';
    link.rel = 'noopener noreferrer';
    identity.append(element('h3', '', provider.name), link);
    const wallet = element('button', 'wallet');
    wallet.title = 'Обновить баланс: ' + provider.name;
    wallet.setAttribute('aria-label', wallet.title);
    wallet.disabled = busy;
    // The SVG is static; all provider-controlled content uses textContent.
    wallet.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="M20 8V5a2 2 0 0 0-2-2H6a3 3 0 0 0 0 6h14v11H6a3 3 0 0 1-3-3V6"/><path d="M20 12h-5a2 2 0 0 0 0 4h5"/><path d="M15 14h1"/></svg>';
    wallet.addEventListener('click', () => refresh(provider.id));
    heading.append(identity, wallet);
    content.append(heading);
    const value = element('div', 'amount');
    if (provider.balance) {
      const balance = provider.balance;
      const raw = balance.unit === 'tokens' || balance.unit === 'quota';
      const custom = provider.currency === 'CUSTOM' && !!balance.display_unit;
      const code = balance.display_unit || balance.unit;
      const currency = custom ? undefined : currencyOptions.get(code);
      const digits = raw ? 0 : custom || currency?.parentElement.dataset.digits === '8' ? 8 : 4;
      value.textContent = new Intl.NumberFormat('ru-RU', {maximumFractionDigits: digits}).format(balance.amount);
      const unit = raw ? (balance.unit === 'quota' ? 'квота' : 'токены') : custom ? code : currency?.dataset.symbol || (code === 'units' ? 'ед.' : code);
      const unitLabel = element('span', 'unit', unit);
      unitLabel.title = currency ? currency.textContent : code;
      unitLabel.setAttribute('aria-label', currency ? currency.textContent : unit);
      value.append(unitLabel);
    } else value.textContent = '—';
    if (provider.balance) value.title = 'Последний успешный запрос: ' + new Date(provider.balance.checked_at).toLocaleString('ru-RU');
    content.append(value);
    if (provider.api_type === 'anymodel' && provider.balance?.tokens !== undefined) {
      content.append(element('p', 'token-balance', new Intl.NumberFormat('ru-RU').format(provider.balance.tokens) + ' токенов'));
    }
    const error = refreshErrors.get(provider.id) || provider.last_error;
    if (error && !busy) {
      const message = element('p', 'error', error);
      message.setAttribute('role', 'alert');
      content.append(message);
    }
    if (provider.balance?.warning) content.append(element('p', 'warning', provider.balance.warning));
    const footer = element('div', 'card-footer');
    const edit = element('button', '', 'Настроить');
    edit.disabled = busy;
    edit.addEventListener('click', () => { editProvider(provider.id); showTab('settings'); });
    footer.append(edit);
    content.append(footer);
    card.append(content);
    if (busy) {
      const loader = element('div', 'card-loader');
      loader.setAttribute('role', 'status');
      const spinner = element('span', 'spinner');
      spinner.setAttribute('aria-hidden', 'true');
      loader.append(spinner, element('span', 'sr-only', 'Загрузка баланса: ' + provider.name));
      card.append(loader);
    }
    $('cards').append(card);
    const entry = element('button', 'provider-entry', provider.name);
    entry.classList.toggle('selected', editingID === provider.id);
    entry.addEventListener('click', () => editProvider(provider.id));
    $('provider-list').append(entry);
  }
}

function authHint() {
  const anymodel = $('api-type').value === 'anymodel';
  const cookie = $('auth-mode').value === 'cookie';
  const auto = !cookie && $('auto-refresh').checked;
  const provider = providers.find(item => item.id === editingID);
  const sameAPI = (provider?.api_type || 'new-api') === $('api-type').value;
  const savedCookie = sameAPI && provider?.has_refresh_cookie && provider.auth_mode === 'bearer' && $('provider-url').value.trim().replace(/\/$/, '') === provider.url && $('provider-user-id').value.trim() === (provider.user_id || '');
  $('bearer-refresh-settings').hidden = cookie;
  $('auto-refresh').disabled = cookie;
  $('refresh-cookie-field').hidden = !auto;
  $('refresh-cookie').disabled = !auto;
  $('refresh-cookie').required = auto && !savedCookie;
  $('refresh-cookie').placeholder = savedCookie ? 'Сохранена · оставьте пустым, чтобы не менять' : 'Cookie из запроса /api/user/auth/refresh';
  const savedToken = sameAPI && provider?.has_token && $('auth-mode').value === provider.auth_mode && $('provider-url').value.trim().replace(/\/$/, '') === provider.url;
  $('provider-token').required = !auto && !savedToken;
  $('token-label').textContent = cookie ? 'Cookie сессии' : 'Токен доступа';
  $('token-hint').textContent = anymodel
    ? 'Скопируйте весь Request Headers → Cookie из запроса /api/me. Нужна cookie am_session; дополнительные cookies защиты сайта тоже можно оставить. При завершении сессии замените Cookie здесь.'
    : cookie
    ? 'Скопируйте значение Cookie из Request Headers запроса /api/user/self.'
    : auto ? 'Необязательно: сервер получит Bearer-токен по HttpOnly cookie из запроса обновления.'
    : 'Скопируйте значение Authorization из запроса /api/user/self в DevTools → Network.';
  const expiry = provider?.token_expires_at;
  $('token-expiry').hidden = cookie || !expiry;
  if (expiry) $('token-expiry').textContent = 'Срок текущего токена: ' + new Date(expiry * 1000).toLocaleString('ru-RU') + (provider?.token_refreshed_at ? '. Автообновлён: ' + new Date(provider.token_refreshed_at).toLocaleString('ru-RU') : '');
}

function currencyHint() {
  const custom = $('provider-currency').value === 'CUSTOM';
  $('custom-currency-field').hidden = !custom;
  $('custom-currency').disabled = !custom;
  $('custom-currency').required = custom;
  $('currency-hint').textContent = $('provider-currency').value
    ? 'Меняется только обозначение валюты. Сумма и курс поставщика сохраняются; обмена валют нет. Исходная квота и токены показываются в своих единицах.'
    : 'В режиме «Авто» используется валюта поставщика. Если она не указана, показываются единицы баланса.';
}

function editProvider(id = '') {
  editingID = id;
  const provider = providers.find(item => item.id === id);
  $('provider-form').reset();
  $('provider-name').value = provider?.name || '';
  $('provider-url').value = provider?.url || '';
  $('api-type').value = provider?.api_type || 'new-api';
  $('auth-mode').value = provider?.auth_mode || 'bearer';
  $('provider-user-id').value = provider?.user_id || '';
  $('provider-currency').value = provider?.currency || '';
  $('custom-currency').value = provider?.custom_currency || '';
  $('auto-refresh').checked = provider?.auto_refresh || false;
  $('provider-token').required = !provider;
  $('provider-token').placeholder = provider?.has_token ? 'Сохранён · оставьте пустым, чтобы не менять' : '';
  $('editor-title').textContent = provider ? 'Настройки: ' + provider.name : 'Новый поставщик';
  $('delete-provider').hidden = !provider;
  apiTypeHint();
  render();
}

function apiTypeHint() {
  const anymodel = $('api-type').value === 'anymodel';
  $('api-type-hint').textContent = anymodel
    ? 'Баланс в USD и остаток токенов AnyModel. Вход через Cookie личного кабинета.'
    : 'Баланс аккаунта поставщика на базе New API. Вход через Bearer или Cookie.';
  $('provider-url-hint').textContent = anymodel
    ? 'Адрес сайта: https://anymodel.org, без /app/dashboard и /api/me.'
    : 'Адрес сайта личного кабинета. Без /console и /api/user/self.';
  $('new-api-user-settings').hidden = anymodel;
  $('provider-user-id').disabled = anymodel;
  $('auth-mode').disabled = anymodel;
  if (anymodel) {
    $('auth-mode').value = 'cookie';
    if (!$('provider-url').value.trim()) $('provider-url').value = 'https://anymodel.org';
  }
  authHint();
  currencyHint();
}

async function refresh(id) {
  if (refreshing.has(id)) return;
  const currentGeneration = generation;
  refreshing.add(id);
  refreshErrors.delete(id);
  render();
  notice();
  try {
    const {provider} = await api('/refresh', 'POST', {id});
    if (currentGeneration !== generation) return;
    providers = providers.map(item => item.id === id ? provider : item);
  } catch (error) {
    if (currentGeneration === generation) {
      const message = error.name === 'TimeoutError'
        ? 'Время ожидания истекло. Повторите запрос.'
        : error instanceof TypeError ? 'Не удалось связаться с сервером. Проверьте соединение и повторите запрос.' : error.message;
      refreshErrors.set(id, message);
    }
  } finally {
    if (currentGeneration === generation) { refreshing.delete(id); render(); }
  }
}

$('login-form').addEventListener('submit', async event => {
  event.preventDefault();
  managementKey = $('management-key').value.trim();
  const button = event.submitter;
  button.disabled = true;
  try {
    const data = await api('/providers');
    providers = data.providers;
    $('management-key').value = '';
    $('login').hidden = true;
    $('app').hidden = false;
    $('logout').hidden = false;
    notice();
    editProvider();
  } catch (error) { managementKey = ''; notice(error.message, true); }
  finally { button.disabled = false; }
});

$('logout').addEventListener('click', () => {
  generation++;
  managementKey = '';
  providers = [];
  refreshing.clear();
  refreshErrors.clear();
  editProvider();
  $('app').hidden = true;
  $('login').hidden = false;
  $('logout').hidden = true;
  notice();
});
$('balances-tab').addEventListener('click', () => showTab('balances'));
$('settings-tab').addEventListener('click', () => showTab('settings'));
$('add-from-balances').addEventListener('click', () => { editProvider(); showTab('settings'); $('provider-name').focus(); });
$('new-provider').addEventListener('click', () => { editProvider(); notice(); $('provider-name').focus(); });
$('cancel-edit').addEventListener('click', () => { editProvider(); notice(); });
$('auth-mode').addEventListener('change', authHint);
$('api-type').addEventListener('change', apiTypeHint);
$('auto-refresh').addEventListener('change', authHint);
$('provider-url').addEventListener('input', authHint);
$('provider-user-id').addEventListener('input', authHint);
$('provider-currency').addEventListener('change', currencyHint);
$('provider-form').addEventListener('submit', async event => {
  event.preventDefault();
  const currentGeneration = generation;
  $('save-provider').disabled = true;
  try {
    const {provider} = await api('/providers', 'PUT', {
      id: editingID, name: $('provider-name').value, url: $('provider-url').value, api_type: $('api-type').value,
      auth_mode: $('auth-mode').value, token: $('provider-token').value, user_id: $('api-type').value === 'anymodel' ? '' : $('provider-user-id').value,
      currency: $('provider-currency').value, custom_currency: $('custom-currency').value,
      auto_refresh: $('auth-mode').value === 'bearer' && $('auto-refresh').checked,
      refresh_cookie: $('auth-mode').value === 'bearer' && $('auto-refresh').checked ? $('refresh-cookie').value : '',
    });
    if (currentGeneration !== generation) return;
    const index = providers.findIndex(item => item.id === provider.id);
    if (index < 0) providers.push(provider); else providers[index] = provider;
    refreshErrors.delete(provider.id);
    editProvider(provider.id);
    showTab('balances');
    notice('Поставщик сохранён. Нажмите на кошелёк, чтобы получить баланс.');
  } catch (error) {
    if (currentGeneration === generation) notice(error.message, true);
  } finally { $('save-provider').disabled = false; }
});
$('delete-provider').addEventListener('click', () => {
  deletingID = editingID;
  const provider = providers.find(item => item.id === deletingID);
  if (!provider) return;
  $('delete-message').textContent = 'Будут удалены настройки, токен и сохранённый баланс «' + provider.name + '».';
  $('delete-dialog').returnValue = '';
  $('delete-dialog').showModal();
});
$('delete-dialog').addEventListener('close', async () => {
  if ($('delete-dialog').returnValue !== 'delete') return;
  const id = deletingID;
  const currentGeneration = generation;
  try {
    await api('/providers?id=' + encodeURIComponent(id), 'DELETE');
    if (currentGeneration !== generation) return;
    providers = providers.filter(item => item.id !== id);
    refreshErrors.delete(id);
    editProvider();
    notice('Поставщик удалён');
  } catch (error) { if (currentGeneration === generation) notice(error.message, true); }
});
