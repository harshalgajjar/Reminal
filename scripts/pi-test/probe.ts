// A pi extension that exists only to report what pi ended up with, then leave.
//
// Loaded alongside reminal's extension so the check can see the result from pi's
// side: which tools reached the registry, and which of them a model would
// actually be offered.
//
// It reports from before_agent_start, which is the first event that is certainly
// after every extension's session_start and still before pi contacts a provider.
// Not session_start itself: pi loads extensions passed with -e ahead of the ones
// it discovers, so a probe reporting there would run before the extension it is
// meant to be observing. And exiting here is what keeps the check free — pi never
// sends the prompt, so no credentials are needed and nothing is spent.

import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

export default function (pi: ExtensionAPI) {
	pi.on("before_agent_start", () => {
		process.stdout.write(`\nPROBE registered: ${pi.getAllTools().map((t) => t.name).sort().join(",")}\n`);
		process.stdout.write(`PROBE active: ${pi.getActiveTools().sort().join(",")}\n`);
		process.exit(0);
	});
}
