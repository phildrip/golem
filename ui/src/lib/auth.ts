// Web auth: the server gates /api/* and /ws behind a bearer token, but leaves
// the SPA shell open so this prompt can render. A successful login sets an
// HttpOnly cookie, which is what makes the WebSocket upgrade work - browsers
// cannot attach headers to a WS handshake.

export type AuthStatus = {
	authRequired: boolean;
	authenticated: boolean;
};

export async function fetchAuthStatus(): Promise<AuthStatus> {
	const res = await fetch('/api/auth');
	if (!res.ok) {
		// An older server without the auth endpoints: assume it is open.
		return { authRequired: false, authenticated: true };
	}
	return (await res.json()) as AuthStatus;
}

export async function login(token: string): Promise<boolean> {
	const res = await fetch('/api/login', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ token })
	});
	return res.ok;
}

export async function logout(): Promise<void> {
	await fetch('/api/logout', { method: 'POST' });
}
