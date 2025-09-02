(() => {
	const messagesEl = document.getElementById('messages');
	const formEl = document.getElementById('chat-form');
	const inputEl = document.getElementById('input');
	const statusEl = document.getElementById('status');
	const loginBtn = document.getElementById('login-btn');
	const signupBtn = document.getElementById('signup-btn');
	const logoutBtn = document.getElementById('logout-btn');
	const subscribeBtn = document.getElementById('subscribe-btn');
	const portalBtn = document.getElementById('portal-btn');
	const userEmailEl = document.getElementById('user-email');

	let sessionId = null;
	let token = localStorage.getItem('token') || '';

	function setAuthToken(t) {
		token = t || '';
		if (token) localStorage.setItem('token', token); else localStorage.removeItem('token');
		refreshAuthUI();
	}

	async function api(path, opts = {}) {
		const headers = Object.assign({ 'Content-Type': 'application/json' }, opts.headers || {});
		if (token) headers['Authorization'] = `Bearer ${token}`;
		const res = await fetch(path, Object.assign({}, opts, { headers }));
		if (res.headers.get('content-type')?.includes('application/json')) {
			const data = await res.json();
			return { res, data };
		}
		return { res, data: null };
	}

	function appendMessage(role, text) {
		const div = document.createElement('div');
		div.className = `msg ${role}`;
		document.createTextNode(text);
		div.textContent = text;
		messagesEl.appendChild(div);
		messagesEl.scrollTop = messagesEl.scrollHeight;
	}

	function appendTool(name, input) {
		const div = document.createElement('div');
		div.className = 'msg tool';
		const pre = document.createElement('pre');
		pre.textContent = `tool: ${name}(${input ? JSON.stringify(input) : ''})`;
		div.appendChild(pre);
		messagesEl.appendChild(div);
		messagesEl.scrollTop = messagesEl.scrollHeight;
	}

	function appendResult(name, result) {
		const div = document.createElement('div');
		div.className = 'msg result';
		const pre = document.createElement('pre');
		pre.textContent = `result: ${result}`;
		div.appendChild(pre);
		messagesEl.appendChild(div);
		messagesEl.scrollTop = messagesEl.scrollHeight;
	}

	function appendError(err) {
		const div = document.createElement('div');
		div.className = 'msg error';
		div.textContent = `error: ${err}`;
		messagesEl.appendChild(div);
		messagesEl.scrollTop = messagesEl.scrollHeight;
	}

	async function ensureSession() {
		if (sessionId) return sessionId;
		statusEl.textContent = 'Connecting...';
		const { data } = await api('/api/session', { method: 'POST' });
		sessionId = data.session_id;
		statusEl.textContent = 'Connected';
		return sessionId;
	}

	async function fetchMe() {
		if (!token) { userEmailEl.textContent = ''; return { email: '', subscription_ok: false }; }
		try {
			const { res, data } = await api('/api/me');
			if (!res.ok) throw new Error('unauthorized');
			userEmailEl.textContent = data.email;
			return data;
		} catch {
			setAuthToken('');
			return { email: '', subscription_ok: false };
		}
	}

	async function loginOrSignup(kind) {
		const email = prompt('Email:');
		if (!email) return;
		const password = prompt('Password:');
		if (!password) return;
		const { res, data } = await api(`/api/${kind}`, { method: 'POST', body: JSON.stringify({ email, password }) });
		if (!res.ok) { alert(data?.error || 'Failed'); return; }
		setAuthToken(data.token);
		appendMessage('assistant', `Auth: ${kind} successful.`);
		refreshAuthUI();
	}

	async function startCheckout() {
		const { res, data } = await api('/api/checkout', { method: 'POST' });
		if (!res.ok) { alert(data?.error || 'Checkout failed'); return; }
		window.location.href = data.url;
	}

	async function openPortal() {
		const { res, data } = await api('/api/portal', { method: 'POST' });
		if (!res.ok) { alert(data?.error || 'Portal failed'); return; }
		window.location.href = data.url;
	}

	function refreshAuthUI() {
		const authed = !!token;
		loginBtn.style.display = authed ? 'none' : '';
		signupBtn.style.display = authed ? 'none' : '';
		logoutBtn.style.display = authed ? '' : 'none';
		subscribeBtn.style.display = authed ? '' : 'none';
		portalBtn.style.display = authed ? '' : 'none';
	}

	formEl.addEventListener('submit', async (e) => {
		e.preventDefault();
		const text = inputEl.value.trim();
		if (!text) return;
		await ensureSession();
		appendMessage('user', `You: ${text}`);
		inputEl.value = '';
		formEl.querySelector('button').disabled = true;
		try {
			const { res, data } = await api('/api/message', { method: 'POST', body: JSON.stringify({ session_id: sessionId, message: text }) });
			if (!res.ok) {
				if (res.status === 402) {
					appendError('Subscription required. Click Subscribe to continue.');
				} else if (res.status === 401) {
					appendError('Please log in first.');
				}
				throw new Error(data?.error || 'Request failed');
			}
			for (const ev of data.events) {
				if (ev.type === 'assistant') appendMessage('assistant', `Claude: ${ev.text}`);
				else if (ev.type === 'tool') appendTool(ev.tool, ev.input);
				else if (ev.type === 'result') appendResult(ev.tool, ev.result);
				else if (ev.type === 'error') appendError(ev.error);
			}
		} catch (err) {
			appendError(err.message || String(err));
		} finally {
			formEl.querySelector('button').disabled = false;
			inputEl.focus();
		}
	});

	loginBtn.addEventListener('click', () => loginOrSignup('login'));
	signupBtn.addEventListener('click', () => loginOrSignup('signup'));
	logoutBtn.addEventListener('click', () => { setAuthToken(''); userEmailEl.textContent = ''; appendMessage('assistant', 'Logged out.'); });
	subscribeBtn.addEventListener('click', startCheckout);
	portalBtn.addEventListener('click', openPortal);

	// Kick off session and auth state on load
	ensureSession().catch((e) => appendError(e.message || String(e)));
	fetchMe().then((me) => {
		if (me.email) userEmailEl.textContent = me.email;
	});
	refreshAuthUI();
})();

