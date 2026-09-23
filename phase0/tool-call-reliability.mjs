// Phase 0(c): can the local model emit well-formed tool calls reliably?
//
// This is the project's largest unvalidated risk: if it fails, the harness has
// no foundation regardless of how good the context budgeting is.
//
// Deliberately uses the OpenAI-compatible endpoint (/v1/chat/completions), not
// Ollama's native /api/chat, because that is the wire format pi-ai's
// `openai-completions` API would drive. This tests the model AND the format.
//
// Gate: >=95% well-formed over 20 prompts.

const HOST = process.env.OLLAMA_HOST ?? "http://127.0.0.1:11434";
const MODEL = process.env.HARNESS_MODEL ?? "qwen3-cc:latest";

// The intended resident set from the plan's `small` tier.
const TOOLS = [
  {
    type: "function",
    function: {
      name: "bash",
      description: "Run a shell command and return its output.",
      parameters: {
        type: "object",
        properties: {
          command: { type: "string", description: "The command to run" },
          timeout_ms: { type: "number", description: "Timeout in milliseconds" },
        },
        required: ["command"],
      },
    },
  },
  {
    type: "function",
    function: {
      name: "read",
      description: "Read a file from disk.",
      parameters: {
        type: "object",
        properties: {
          path: { type: "string", description: "Absolute path to the file" },
          offset: { type: "number", description: "Line to start from" },
          limit: { type: "number", description: "Number of lines to read" },
        },
        required: ["path"],
      },
    },
  },
  {
    type: "function",
    function: {
      name: "edit",
      description: "Replace an exact string in a file.",
      parameters: {
        type: "object",
        properties: {
          path: { type: "string" },
          old_string: { type: "string" },
          new_string: { type: "string" },
        },
        required: ["path", "old_string", "new_string"],
      },
    },
  },
  {
    type: "function",
    function: {
      name: "write",
      description: "Write content to a file, overwriting it.",
      parameters: {
        type: "object",
        properties: { path: { type: "string" }, content: { type: "string" } },
        required: ["path", "content"],
      },
    },
  },
  {
    type: "function",
    function: {
      name: "session_search",
      description: "Search past sessions for relevant prior work.",
      parameters: {
        type: "object",
        properties: { query: { type: "string" }, limit: { type: "number" } },
        required: ["query"],
      },
    },
  },
];

const SCHEMAS = new Map(TOOLS.map((t) => [t.function.name, t.function.parameters]));

// `expect: null` means the correct behavior is to answer in prose and NOT call a
// tool. Those cases matter as much as the positive ones: a model that calls a
// tool for everything burns a 32k window on nothing.
const CASES = [
  { prompt: "List the files in /Users/andrepato/projects.", expect: "bash" },
  { prompt: "What's in /etc/hosts? Read it.", expect: "read" },
  { prompt: "Read the first 50 lines of /var/log/system.log", expect: "read" },
  { prompt: "In /tmp/a.txt, change the word 'foo' to 'bar'.", expect: "edit" },
  { prompt: "Create /tmp/hello.txt containing the text 'hello world'.", expect: "write" },
  { prompt: "Have I worked on the ollama gateway before? Check past sessions.", expect: "session_search" },
  { prompt: "Show me the current git status.", expect: "bash" },
  { prompt: "How much disk space is free?", expect: "bash" },
  { prompt: "Open /Users/andrepato/.zshrc and show me what's in it.", expect: "read" },
  { prompt: "Rename the variable 'cfg' to 'config' in /tmp/main.go.", expect: "edit" },
  { prompt: "Save a file at /tmp/notes.md with a heading that says Notes.", expect: "write" },
  { prompt: "Find any prior discussion of budget tiers in my history.", expect: "session_search" },
  { prompt: "Run the test suite.", expect: "bash" },
  { prompt: "What does /Users/andrepato/projects/harness/package.json contain?", expect: "read" },
  { prompt: "Count the lines in every .go file under the current directory.", expect: "bash" },
  { prompt: "Fix the typo 'recieve' in /tmp/doc.txt, it should be 'receive'.", expect: "edit" },
  { prompt: "What is the capital of France?", expect: null },
  { prompt: "Explain the difference between a mutex and a semaphore.", expect: null },
  { prompt: "Write a file /tmp/x.json holding an empty JSON object.", expect: "write" },
  { prompt: "Thanks, that's all for now.", expect: null },
];

// Hard per-request deadline. Without this a wedged Ollama runner hangs the whole
// run with no output, which is exactly what happened the first time this ran.
const REQUEST_TIMEOUT_MS = Number(process.env.REQUEST_TIMEOUT_MS ?? 180_000);
const NO_THINK = process.env.NO_THINK === "1";

async function ask(prompt) {
  const res = await fetch(`${HOST}/v1/chat/completions`, {
    method: "POST",
    signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      model: MODEL,
      messages: [
        {
          role: "system",
          content: "You are a coding assistant. Use the provided tools when they are needed.",
        },
        // Qwen3's reasoning switch. qwen3-cc and qwen3:30b-a3b ignore Ollama's
        // `think:false` -- they keep reasoning and merely move it from the
        // `thinking` field into `content`, then run past the token budget
        // without ever emitting the call. The `/no_think` suffix is the
        // in-prompt switch those templates do honor. Qwen3.5 does not need it.
        { role: "user", content: NO_THINK ? `${prompt} /no_think` : prompt },
      ],
      tools: TOOLS,
      // Deterministic: this measures capability, not sampling luck.
      temperature: 0,
      max_tokens: 512,
    }),
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}: ${(await res.text()).slice(0, 200)}`);
  return res.json();
}

// "Well-formed" is strict on purpose: a name the harness knows, arguments that
// are valid JSON, and every required property present. Anything less means the
// agent loop has to handle a malformed call at runtime.
function grade(body, expected) {
  const msg = body.choices?.[0]?.message;
  if (!msg) return { ok: false, why: "no message in response" };

  const calls = msg.tool_calls ?? [];
  if (expected === null) {
    return calls.length === 0
      ? { ok: true, why: "correctly answered without a tool" }
      : { ok: false, why: `called ${calls[0].function?.name} when no tool was needed` };
  }

  if (calls.length === 0) return { ok: false, why: "no tool call emitted" };

  const call = calls[0];
  const name = call.function?.name;
  if (!SCHEMAS.has(name)) return { ok: false, why: `hallucinated tool "${name}"` };

  let args;
  try {
    args = JSON.parse(call.function.arguments);
  } catch {
    return { ok: false, why: `arguments are not valid JSON: ${String(call.function.arguments).slice(0, 60)}` };
  }
  if (args === null || typeof args !== "object" || Array.isArray(args)) {
    return { ok: false, why: "arguments are not a JSON object" };
  }

  const missing = (SCHEMAS.get(name).required ?? []).filter((k) => !(k in args));
  if (missing.length) return { ok: false, why: `missing required: ${missing.join(", ")}` };

  // Formation is the gate. Choosing a different-but-valid tool is noted, not failed.
  const note = name === expected ? "" : ` (chose ${name}, expected ${expected})`;
  return { ok: true, why: `well-formed${note}`, offTarget: name !== expected };
}

console.log(`model: ${MODEL}   host: ${HOST}   no_think: ${NO_THINK}`);
console.log(`${CASES.length} cases, ${TOOLS.length} resident tools\n`);

let wellFormed = 0;
let onTarget = 0;
const failures = [];

for (const [i, c] of CASES.entries()) {
  let r;
  try {
    r = grade(await ask(c.prompt), c.expect);
  } catch (err) {
    r = { ok: false, why: `request failed: ${err.message}` };
  }
  if (r.ok) {
    wellFormed++;
    if (!r.offTarget) onTarget++;
  } else {
    failures.push({ prompt: c.prompt, why: r.why });
  }
  const mark = r.ok ? (r.offTarget ? "~" : "+") : "x";
  console.log(`  ${mark} [${String(i + 1).padStart(2)}] ${c.prompt.slice(0, 52).padEnd(52)} ${r.why}`);
  // Unbuffer: piping to `tail` otherwise hides all progress until exit.
  if (process.stdout.write("")) {/* flushed */}
}

const pct = (wellFormed / CASES.length) * 100;
console.log(`\nwell-formed: ${wellFormed}/${CASES.length} (${pct.toFixed(0)}%)`);
console.log(`on-target:   ${onTarget}/${CASES.length} (${((onTarget / CASES.length) * 100).toFixed(0)}%)`);

if (failures.length) {
  console.log("\nfailures:");
  for (const f of failures) console.log(`  - ${f.prompt.slice(0, 50)} -> ${f.why}`);
}

console.log(`\nGATE (>=95% well-formed): ${pct >= 95 ? "PASS" : "FAIL"}`);
process.exit(pct >= 95 ? 0 : 1);
