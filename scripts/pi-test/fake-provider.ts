// Points pi at the rig's local endpoint (scripts/pi-test/fake-model.mjs) so a
// real turn can run without credentials, a network, or any spend.
//
// Test-only: this is loaded with -e by the rig and is never installed.

import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

export default function (pi: ExtensionAPI) {
	pi.registerProvider("fake", {
		baseUrl: `http://127.0.0.1:${process.env.FAKE_MODEL_PORT ?? 8099}/v1`,
		apiKey: "not-a-secret",
		api: "openai-completions",
		models: [
			{
				id: "fake-1",
				name: "Fake 1",
				reasoning: false,
				input: ["text"],
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
				contextWindow: 128000,
				maxTokens: 4096,
			},
		],
	});
}
