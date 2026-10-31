<script lang="ts">
	import "../app.css";
	let { children } = $props();
</script>

<div class="h-[100dvh] w-full flex flex-col bg-[#0b0f19] text-slate-100 overflow-hidden">
	{@render children()}
</div>
