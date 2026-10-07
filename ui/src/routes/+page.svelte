<script lang="ts">
	import { Bot, Terminal, Cpu } from "@lucide/svelte";
</script>

<div class="flex-1 flex flex-col items-center justify-center p-8 text-center space-y-4">
	<div class="h-16 w-16 rounded-2xl bg-blue-600/20 border border-blue-500/40 flex items-center justify-center text-blue-400 shadow-lg shadow-blue-500/10">
		<Bot class="w-8 h-8" />
	</div>
	<h1 class="text-2xl font-bold tracking-tight text-white">Golem Multi-Agent Cockpit</h1>
	<p class="text-sm text-slate-400 max-w-md">
		Autonomous agent orchestration, parallel session tabs, shared fractal memory, and hashline verification.
	</p>
	<div class="flex gap-4 pt-4">
		<div class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-slate-900 border border-slate-800 text-xs text-slate-400">
			<Cpu class="w-4 h-4 text-emerald-400" />
			<span>bbolt Memory Mapped</span>
		</div>
		<div class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-slate-900 border border-slate-800 text-xs text-slate-400">
			<Terminal class="w-4 h-4 text-blue-400" />
			<span>Realtime WebSockets</span>
		</div>
	</div>
</div>
