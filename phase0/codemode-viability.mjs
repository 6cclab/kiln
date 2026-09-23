// Phase 0(d): can the local model write correct multi-step JavaScript against a
// discovered tool catalog?
//
// This is the sole input to the CodeMode decision. CodeMode (opencode, MIT) is
// strictly better on context than tool-gating -- it keeps large intermediate
// results out of the window entirely -- but it was built for frontier models and
// costs an `effect` dependency in a non-Effect codebase. If a 9B-to-30B local
// model cannot reliably write these programs, that dependency buys nothing.
//
// Scored structurally, not by output equality: the program must parse, call the
// real tool paths, invent none, and actually reduce the data.

import { parse } from "acorn";
import { simple } from "acorn-walk";

const HOST = process.env.OLLAMA_HOST ?? "http://127.0.0.1:11434";
const MODEL = process.env.HARNESS_MODEL ?? "qwen3-cc:latest";
const SAMPLES = Number(process.env.SAMPLES ?? 4);

const CATALOG = `
tools.grafana.listDashboards(input: { query?: string }): Promise<{ uid: string; title: string; folder: string }[]>
tools.grafana.getDashboard(input: { uid: string }): Promise<{ uid: string; title: string; panels: { id: number; title: string; type: string }[] }>
tools.grafana.queryPrometheus(input: { expr: string; start: string; end: string }): Promise<{ metric: Record<string,string>; values: [number, number][] }[]>
tools.sentry.listIssues(input: { project: string }): Promise<{ id: string; title: string; count: number; lastSeen: string }[]>
tools.sentry.getIssue(input: { id: string }): Promise<{ id: string; title: string; stacktrace: string; tags: Record<string,string> }>
tools.argocd.listApplications(input: {}): Promise<{ name: string; namespace: string; syncStatus: string; healthStatus: string }[]>
tools.argocd.getApplication(input: { name: string }): Promise<{ name: string; resources: { kind: string; name: string; status: string }[] }>
`.trim();

const SYSTEM = `You write short JavaScript programs that call tools.

Available tools (TypeScript signatures):
${CATALOG}

Rules:
- Respond with ONLY a JavaScript code block. No explanation.
- Call tools at their exact paths. Do not invent tools or rename paths.
- Tool calls return promises. Use await, and Promise.all for independent calls.
- Filter and reduce the data inside the program. Return only what was asked for.
- End the program with a return statement. Top-level await is allowed.`;

const TASKS = [
  {
    name: "dashboards+panels",
    prompt:
      "Find every dashboard whose title contains 'ollama', fetch each one, and return only the titles of panels whose type is 'timeseries'.",
    mustCall: ["tools.grafana.listDashboards", "tools.grafana.getDashboard"],
  },
  {
    name: "sentry-top-issues",
    prompt:
      "For project 'budget-track', return the titles of the 3 issues with the highest count, along with the 'release' tag of each.",
    mustCall: ["tools.sentry.listIssues", "tools.sentry.getIssue"],
  },
  {
    name: "argocd-unhealthy",
    prompt:
      "Return the names of all ArgoCD applications that are not Healthy, and for each, the kinds of its resources that are not Synced.",
    mustCall: ["tools.argocd.listApplications", "tools.argocd.getApplication"],
  },
  {
    name: "cross-namespace",
    prompt:
      "For every ArgoCD application that is OutOfSync, find Sentry issues in a project of the same name and return app name plus issue count.",
    mustCall: ["tools.argocd.listApplications", "tools.sentry.listIssues"],
  },
  {
    name: "single-call-filter",
    prompt: "Return just the uids of dashboards in the 'Infrastructure' folder.",
    mustCall: ["tools.grafana.listDashboards"],
  },
];

const VALID_PATHS = new Set(
  CATALOG.split("\n").map((l) => l.slice(0, l.indexOf("("))),
);

function extractCode(text) {
  const fence = text.match(/```(?:javascript|js|ts|typescript)?\s*\n([\s\S]*?)```/);
  return (fence ? fence[1] : text).trim();
}

// Collect dotted member-expression paths that start with `tools`, so both the
// required-call check and the hallucination check read the same source of truth.
function toolPaths(ast) {
  const found = new Set();
  const flatten = (node) => {
    if (node.type === "Identifier") return node.name;
    if (node.type === "MemberExpression" && !node.computed) {
      const obj = flatten(node.object);
      return obj ? `${obj}.${node.property.name}` : null;
    }
    return null;
  };
  simple(ast, {
    CallExpression(node) {
      const path = flatten(node.callee);
      if (path?.startsWith("tools.")) found.add(path);
    },
  });
  return found;
}

function grade(text, task) {
  const code = extractCode(text);
  if (!code) return { ok: false, why: "empty response" };

  let ast;
  try {
    // allowReturnOutsideFunction: programs are bodies, not modules.
    ast = parse(code, { ecmaVersion: 2023, allowAwaitOutsideFunction: true, allowReturnOutsideFunction: true });
  } catch (err) {
    return { ok: false, why: `parse error: ${err.message.slice(0, 60)}` };
  }

  const called = toolPaths(ast);
  if (called.size === 0) return { ok: false, why: "calls no tools" };

  const bogus = [...called].filter((p) => !VALID_PATHS.has(p));
  if (bogus.length) return { ok: false, why: `hallucinated path: ${bogus[0]}` };

  const missing = task.mustCall.filter((p) => !called.has(p));
  if (missing.length) return { ok: false, why: `never calls ${missing[0].split(".").pop()}` };

  // "Reduces the data" -- a program that returns raw tool output defeats the point.
  const reduces = /\.(filter|map|slice|reduce|sort|find)\s*\(/.test(code);
  if (!reduces) return { ok: false, why: "returns raw tool output, no filtering" };

  const parallel = /Promise\.all/.test(code);
  return { ok: true, why: `valid${parallel ? " (uses Promise.all)" : ""}`, parallel };
}

// Hard per-request deadline; see tool-call-reliability.mjs.
const REQUEST_TIMEOUT_MS = Number(process.env.REQUEST_TIMEOUT_MS ?? 180_000);
// See tool-call-reliability.mjs: qwen3-cc and qwen3:30b-a3b ignore think:false.
const NO_THINK = process.env.NO_THINK === "1";

async function ask(prompt) {
  const res = await fetch(`${HOST}/v1/chat/completions`, {
    method: "POST",
    signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      model: MODEL,
      messages: [
        { role: "system", content: SYSTEM },
        { role: "user", content: NO_THINK ? `${prompt} /no_think` : prompt },
      ],
      // Modelfile default, not 0: this measures typical behavior, not the single
      // greedy path, and 20 identical greedy samples would say nothing about reliability.
      temperature: 0.3,
      max_tokens: 1200,
    }),
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const body = await res.json();
  return body.choices?.[0]?.message?.content ?? "";
}

console.log(`model: ${MODEL}   host: ${HOST}`);
console.log(`${TASKS.length} tasks x ${SAMPLES} samples = ${TASKS.length * SAMPLES} programs\n`);

let pass = 0;
let parallelCount = 0;
let n = 0;
const reasons = new Map();

for (const task of TASKS) {
  const marks = [];
  for (let s = 0; s < SAMPLES; s++) {
    n++;
    let r;
    try {
      r = grade(await ask(task.prompt), task);
    } catch (err) {
      r = { ok: false, why: `request failed: ${err.message}` };
    }
    if (r.ok) {
      pass++;
      if (r.parallel) parallelCount++;
    } else {
      reasons.set(r.why, (reasons.get(r.why) ?? 0) + 1);
    }
    marks.push(r.ok ? "+" : "x");
  }
  console.log(`  ${task.name.padEnd(20)} ${marks.join(" ")}`);
}

const pct = (pass / n) * 100;
console.log(`\nvalid programs: ${pass}/${n} (${pct.toFixed(0)}%)`);
console.log(`used Promise.all: ${parallelCount}/${pass} of the valid ones`);

if (reasons.size) {
  console.log("\nfailure modes:");
  for (const [why, count] of [...reasons].sort((a, b) => b[1] - a[1])) {
    console.log(`  ${String(count).padStart(2)}x  ${why}`);
  }
}

// Threshold reasoning: a bad program wastes a whole turn AND its context. Below
// ~80% the retry loop costs more window than gating would have saved.
console.log(`\nCODEMODE VIABLE (>=80%): ${pct >= 80 ? "YES" : "NO"}`);
console.log(
  pct >= 80
    ? "  -> read packages/codemode/src in full before taking the effect dependency"
    : "  -> do NOT take the effect dependency; build Layers 0+1 only",
);
