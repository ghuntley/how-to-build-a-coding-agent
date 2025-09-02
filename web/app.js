(() => {
	const messagesEl = document.getElementById('messages');
	const formEl = document.getElementById('chat-form');
	const inputEl = document.getElementById('input');
	const statusEl = document.getElementById('status');

	let sessionId = null;

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
		const res = await fetch('/api/session', { method: 'POST' });
		const data = await res.json();
		sessionId = data.session_id;
		statusEl.textContent = 'Connected';
		return sessionId;
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
			const res = await fetch('/api/message', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ session_id: sessionId, message: text }),
			});
			const data = await res.json();
			if (!res.ok) throw new Error(data.error || 'Request failed');
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

	// Kick off session on load
	ensureSession().catch((e) => appendError(e.message || String(e)));
})();

