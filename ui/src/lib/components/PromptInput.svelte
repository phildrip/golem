<script lang="ts">
	import { Send, CornerDownLeft, Sparkles } from '@lucide/svelte';

	interface Props {
		disabled?: boolean;
		onSubmit: (prompt: string) => void;
	}

	let { disabled = false, onSubmit }: Props = $props();

	let text = $state('');

	function handleKeyDown(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			submit();
		}
	}

	function submit() {
		const trimmed = text.trim();
		if (!trimmed || disabled) return;
		onSubmit(trimmed);
		text = '';
	}
</script>

<div class="p-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] bg-[#090d16] border-t border-slate-800/80">
	<div class="relative flex items-end gap-2 max-w-4xl mx-auto bg-slate-900/90 border border-slate-800 focus-within:border-blue-500/60 rounded-xl p-2 transition-colors shadow-inner">
		<textarea
			bind:value={text}
			onkeydown={handleKeyDown}
			placeholder="Message Golem (e.g. 'Investigate performance bottlenecks and delegate benchmarks')..."
			rows="2"
			{disabled}
			class="flex-1 bg-transparent resize-none border-none outline-none text-base md:text-xs text-slate-100 placeholder:text-slate-500 p-1 min-h-[44px] max-h-32"
		></textarea>

		<button
			type="button"
			onclick={submit}
			disabled={disabled || !text.trim()}
			class="flex items-center justify-center min-h-[44px] min-w-[44px] md:min-h-0 md:min-w-0 h-11 w-11 md:h-8 md:w-auto md:px-3 rounded-lg bg-blue-600 hover:bg-blue-500 disabled:bg-slate-800 disabled:text-slate-600 text-white font-medium text-xs transition-colors shadow-sm shrink-0"
			title="Send message"
		>
			<Send class="w-4 h-4 md:w-3.5 md:h-3.5" />
		</button>
	</div>
	<div class="hidden sm:flex items-center justify-between max-w-4xl mx-auto px-1 pt-1.5 text-[10px] text-slate-500 font-mono">
		<span class="flex items-center gap-1">
			<Sparkles class="w-2.5 h-2.5 text-blue-400" />
			<span>Subagent swarm, tools, and fractal memory enabled</span>
		</span>
		<span>Press <kbd class="px-1 py-0.5 rounded bg-slate-800 text-slate-400">Enter</kbd> to submit</span>
	</div>
</div>
