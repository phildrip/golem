import type { WSEvent } from './types';

export type EventCallback = (event: WSEvent) => void;

class RealtimeClient {
	private ws: WebSocket | null = null;
	private listeners: Set<EventCallback> = new Set();
	private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	private url: string = '';
	public isConnected: boolean = false;

	constructor() {
		if (typeof window !== 'undefined') {
			const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
			this.url = `${proto}//${window.location.host}/ws`;
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
				if (this.reconnectTimer) {
					clearTimeout(this.reconnectTimer);
					this.reconnectTimer = null;
				}
			};

			this.ws.onmessage = (event) => {
				try {
					const data = JSON.parse(event.data) as WSEvent;
					for (const cb of this.listeners) {
						cb(data);
					}
				} catch {
					// Ignore malformed payloads
				}
			};

			this.ws.onclose = () => {
				this.isConnected = false;
				this.scheduleReconnect();
			};

			this.ws.onerror = () => {
				this.isConnected = false;
				if (this.ws) {
					this.ws.close();
				}
			};
		} catch {
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
