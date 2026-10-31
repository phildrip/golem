import type { WSEvent } from './types';

export type EventCallback = (event: WSEvent) => void;
export type ConnectionStateCallback = (connected: boolean) => void;

class RealtimeClient {
	private ws: WebSocket | null = null;
	private listeners: Set<EventCallback> = new Set();
	private stateListeners: Set<ConnectionStateCallback> = new Set();
	private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	private url: string = '';
	public isConnected: boolean = false;
	private lastSeqBySession: Map<string, number> = new Map();

	constructor() {
		if (typeof window !== 'undefined') {
			const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
			this.url = `${proto}//${window.location.host}/ws`;
		}
	}

	public onConnectionChange(callback: ConnectionStateCallback): () => void {
		this.stateListeners.add(callback);
		callback(this.isConnected);
		return () => {
			this.stateListeners.delete(callback);
		};
	}

	private notifyConnectionState(connected: boolean): void {
		for (const cb of this.stateListeners) {
			cb(connected);
		}
	}

	public connect(url?: string): void {
		if (typeof window === 'undefined') return;
		if (url) this.url = url;

		if (this.ws && (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)) {
			return;
		}

		try {
			this.ws = new WebSocket(this.url);

			this.ws.onopen = () => {
				this.isConnected = true;
				this.reconnectAttempts = 0;
				if (this.reconnectTimer) {
					clearTimeout(this.reconnectTimer);
					this.reconnectTimer = null;
				}
				// Send resume frames for all known sessions so the server replays
				// any events missed while the socket was down or backgrounded.
				for (const [sessionId, lastSeq] of this.lastSeqBySession.entries()) {
					if (lastSeq > 0) {
						this.send({ type: 'resume', session_id: sessionId, last_seq: lastSeq });
					}
				}
				this.notifyConnectionState(true);
			};

			this.ws.onmessage = (event) => {
				try {
					const data = JSON.parse(event.data) as WSEvent;
					if (data.session_id && typeof data.seq === 'number' && data.seq > 0) {
						const prev = this.lastSeqBySession.get(data.session_id) || 0;
						if (data.type !== 'resume_failed' && data.seq <= prev) {
							return;
						}
						if (data.seq > prev) {
							this.lastSeqBySession.set(data.session_id, data.seq);
						}
					}
					for (const cb of this.listeners) {
						cb(data);
					}
				} catch {
					// Ignore malformed payloads
				}
			};

			this.ws.onclose = () => {
				this.isConnected = false;
				this.notifyConnectionState(false);
				this.scheduleReconnect();
			};

			this.ws.onerror = () => {
				this.isConnected = false;
				this.notifyConnectionState(false);
				if (this.ws) {
					this.ws.close();
				}
			};
		} catch {
			this.notifyConnectionState(false);
			this.scheduleReconnect();
		}
	}

	public subscribe(callback: EventCallback): () => void {
		this.listeners.add(callback);
		return () => {
			this.listeners.delete(callback);
		};
	}

	public send(data: unknown): void {
		if (this.ws && this.ws.readyState === WebSocket.OPEN) {
			this.ws.send(JSON.stringify(data));
		}
	}

	private reconnectAttempts: number = 0;

	private scheduleReconnect(): void {
		if (this.reconnectTimer) return;
		const delay = Math.min(1000 * Math.pow(1.5, this.reconnectAttempts), 15000);
		this.reconnectAttempts++;
		this.reconnectTimer = setTimeout(() => {
			this.reconnectTimer = null;
			this.connect();
		}, delay);
	}

	public disconnect(): void {
		this.reconnectAttempts = 0;
		if (this.reconnectTimer) {
			clearTimeout(this.reconnectTimer);
			this.reconnectTimer = null;
		}
		if (this.ws) {
			this.ws.close();
			this.ws = null;
		}
		this.isConnected = false;
	}
}

export const realtime = new RealtimeClient();

export async function discoverHistories(workspace?: string): Promise<import('./types').DiscoveredSession[]> {
	const query = workspace ? `?workspace=${encodeURIComponent(workspace)}` : '';
	const res = await fetch(`/api/importer/discover${query}`);
	if (!res.ok) {
		const errObj = await res.json().catch(() => null);
		throw new Error(errObj?.error || res.statusText || 'Failed to discover histories');
	}
	return res.json();
}

export async function importSession(path: string, id?: string, title?: string): Promise<import('./types').SessionInfo> {
	const res = await fetch('/api/importer/import', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ path, id, title })
	});
	if (!res.ok) {
		const errObj = await res.json().catch(() => null);
		throw new Error(errObj?.error || res.statusText || 'Failed to import session');
	}
	return res.json();
}

export async function getProfiles(): Promise<import('./types').ProfilesResponse> {
	const res = await fetch('/api/profiles');
	if (!res.ok) {
		const errObj = await res.json().catch(() => null);
		throw new Error(errObj?.error || res.statusText || 'Failed to fetch profiles');
	}
	return res.json();
}

export async function saveProfile(req: import('./types').SaveProfileRequest): Promise<import('./types').ProfilesResponse> {
	const res = await fetch('/api/profiles', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(req)
	});
	if (!res.ok) {
		const errObj = await res.json().catch(() => null);
		throw new Error(errObj?.error || res.statusText || 'Failed to save profile');
	}
	return res.json();
}

export async function setActiveProfile(name: string, scope?: string): Promise<import('./types').ProfilesResponse> {
	const res = await fetch('/api/profiles/active', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ name, scope })
	});
	if (!res.ok) {
		const errObj = await res.json().catch(() => null);
		throw new Error(errObj?.error || res.statusText || 'Failed to activate profile');
	}
	return res.json();
}

export async function deleteProfile(name: string, scope?: string): Promise<import('./types').ProfilesResponse> {
	const query = scope ? `?scope=${encodeURIComponent(scope)}` : '';
	const res = await fetch(`/api/profiles/${encodeURIComponent(name)}${query}`, {
		method: 'DELETE'
	});
	if (!res.ok) {
		const errObj = await res.json().catch(() => null);
		throw new Error(errObj?.error || res.statusText || 'Failed to delete profile');
	}
	return res.json();
}

export async function getProviderModels(provider: string, baseUrl?: string): Promise<import('./types').ModelsResponse> {
	const params = new URLSearchParams({ provider });
	if (baseUrl) {
		params.set('base_url', baseUrl);
	}
	const res = await fetch(`/api/models?${params.toString()}`);
	if (!res.ok) {
		const errObj = await res.json().catch(() => null);
		throw new Error(errObj?.error || res.statusText || 'Failed to fetch models');
	}
	return res.json();
}

export async function getGraphs(): Promise<import('./types').GraphsResponse> {
	const res = await fetch('/api/graphs');
	if (!res.ok) {
		return { specs: [], runs: [] };
	}
	return res.json();
}

