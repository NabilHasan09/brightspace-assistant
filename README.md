# Brightspace Course Assistant — v1 Plan

> **Status: design document. No implementation yet.**
> This README is the full technical plan — architecture, verified API surface, build order, and test strategy.
> Nothing below is built. Work starts at step 1 of [Build order](#build-order).

An AI assistant for CUNY's Brightspace (D2L) LMS, in Go. Answers course questions —
study material, deadlines, grades, feedback, announcements, discussions, policy —
by routing them to specialist agents exposed as MCP tools.

**Quick links:** [Intent taxonomy](#intent-taxonomy) · [Routing](#routing--tool-selection) · [Retrieval](#retrieval-hybrid-vector--structural) · [Storage](#storage-no-database-server) · [MCP design](#mcp-specialists-as-tools) · [Build order](#build-order)

---

## Context

An AI chatbot that answers course-related questions against CUNY's Brightspace (D2L) LMS. Not a two-trick bot: a student's real questions span study material, deadlines, grades, feedback, announcements, discussions, and course policy. Incoming questions are **classified by intent and routed to a specialist agent** with a scoped toolset and prompt.

**Feasibility verdict: yes.** Every intent below is backed by documented D2L Valence endpoints — verified, not assumed. The blockers are institutional, not technical:

1. **OAuth clients cannot be self-registered.** Requires a Brightspace admin via Admin Tools → Manage Extensibility. No student path exists.
2. **CUNY gates integrations** behind the LMS Third-Party Tools Request Form (Security, Accessibility/VPAT, FERPA, functionality, cost). Faculty/staff submit it; students aren't listed as eligible.
3. **FERPA.** Course content, grades, and submissions are education records; routing them to a third-party LLM needs a vendor agreement. This kills more student projects than any technical limit.
4. **No free sandbox.** Dev-COP is being decommissioned; sandboxes come via D2L's Partner Program.
5. Two content caveats: only **file-type topics** return bytes (link/publisher topics return nothing), and **submissions are attributed to the calling user's token** — so submission must run on the student's own OAuth token, never a service account.

**Decisions:** personal project, no institutional access → build against a mock layer mirroring the real API. Read-only intents in v1; submission deferred to v2. Go.

**Strategy:** define a `brightspace.Client` interface whose methods mirror the real Valence routes exactly, implemented twice — `MockClient` (fixtures) now, `LiveClient` (OAuth2) if credentials ever land. Everything above the interface is identical either way.

## Intent taxonomy

Each intent is a specialist: a scoped system prompt + a tool subset + an effort setting. Every one is grounded in real endpoints — an intent with no data behind it doesn't ship.

| Intent | Answers | Backing endpoints |
|---|---|---|
| **`materials`** | Find, summarize, explain course content | `content/root/`, `content/modules/{id}/structure/`, `content/topics/{id}/file` |
| **`deadlines`** | What's due, when, exam dates | `calendar/events/myEvents/` (date-ranged), `dropbox/folders/`, `quizzes/` |
| **`assignments`** | Requirements, formats, attempts, submission status | `dropbox/folders/`, `.../submissions/`, submission feedback |
| **`grades`** | Current grades, feedback, what-if projections | `grades/values/myGradeValues/`, `grades/final/values/myGradeValue` — both include instructor `Comments` |
| **`announcements`** | Recent updates, schedule changes, missed notices | `news/`, plus cross-course feed `/lp/{v}/feed/` (`since`/`until`) |
| **`discussions`** | Prompts, replies, unread posts | `discussions/forums/`, `.../topics/`, `.../posts/` (posts carry `IsRead`) |
| **`logistics`** | Late policy, grading breakdown, office hours, textbook | **No API** — derived from the syllabus document + `classlist/` |
| **`planning`** | Cross-course triage and prioritization | Multi-course `calendar/events/myEvents/?orgUnitIdsCSV=` + grades + dropbox |

Two things worth flagging:

- **`logistics` has no endpoint.** Policy questions live inside the syllabus PDF, not in a structured field. That specialist works by locating and reading the syllabus — it's materials retrieval wearing a different hat. Worth building separately anyway, because the prompt ("quote the policy verbatim, cite the section") differs sharply from open study help.
- **`planning` is cheaper than it looks.** The calendar API takes `orgUnitIdsCSV` with a date range, so "everything due next week across all my courses" is one call, not a fan-out across enrollments.

## Questions the bot should handle

**Materials & study**
- "Give me 5 documents from MTH1003 that would help me with my next exam"
- "Summarize the Week 8 lecture slides"
- "Which readings cover eigenvalues?"
- "I missed Tuesday's class — what did I miss?"
- "Is there a practice exam posted?"
- "Where does the textbook disagree with the lecture notes on limits?"

**Deadlines & schedule**
- "What's due this week?" · "Anything due before Friday?"
- "When's my next exam in MTH1003?" · "What's my finals schedule?"
- "How long do I have left on the HW3 dropbox?"

**Assignments**
- "What are the requirements for HW3?" · "What file format does the essay need?"
- "Did my HW2 submission actually go through?"
- "How many attempts do I get on the lab report?"
- *(v2)* "Submit this to MTH3005"

**Grades & feedback**
- "What's my current grade in MTH1003?" · "How did I do on HW2?"
- "What feedback did my professor leave on my essay?"
- "What do I need on the final to finish with a B+?"
- "Which assignment hurt my grade the most?"

**Announcements**
- "Any announcements I've missed this week?"
- "Did the professor move the exam date?"

**Discussions**
- "What's this week's discussion prompt?" · "Did anyone reply to my post?"
- "Do I have unread discussion posts?" · "Summarize the class discussion on chapter 4"

**Logistics**
- "What's the late submission policy for MTH1003?"
- "How is my final grade calculated?" · "Who's my TA, and when are office hours?"
- "How many absences am I allowed?" · "What textbook do I need?"

**Cross-course planning**
- "What should I work on today?"
- "Show me everything due next week across all my courses"
- "Which class am I furthest behind in?"
- "I have 3 hours tonight — what's the highest-value thing to do?"

## Routing = tool selection

**There is no separate router component.** Specialists are tools; picking a tool is the routing. The orchestrator sees eight `ask_*_specialist` tools via `tools/list` and selects among them in one turn.

```
user question
     │
     ▼  orchestrator (Claude Desktop, or the Go chat app)
     │  tools/list → 8 specialists, each with a Purpose
     │
     ├─ picks one ──────────▶ ask_materials_specialist(question, context)
     ├─ picks several ──────▶ parallel tool calls, synthesized natively
     └─ unclear ────────────▶ ask_planning_specialist (union of tools)
```

Multi-intent questions need no special handling: *"what's due this week and what should I study for it?"* becomes two parallel tool calls, and the model synthesizes the results — that's ordinary parallel tool use, not machinery we build.

**A specialist is a config, not a process:**

```go
type Specialist struct {
    Intent  Intent
    Purpose string                // becomes the MCP tool description — this is what drives selection
    System  string                // scoped system prompt
    Tools   []anthropic.BetaTool  // scoped subset
    Effort  string                // per-specialist cost tuning
}
```

`Purpose` is load-bearing: it becomes the MCP tool `description`, which is what the orchestrator reads when choosing. Write it as a trigger condition ("Call this when the user asks about grades, feedback, or what score they need") rather than a label — prescriptive descriptions measurably improve selection.

**Why specialists at all**, rather than one agent with every tool:

- **Tool-selection accuracy degrades with tool count.** Eight specialists beats one agent choosing among ~25 raw tools — and the specialist's *own* loop then chooses among only its four.
- **The prompts genuinely differ.** `grades` must be precise and refuse to guess at numbers it lacks. `materials` should synthesize freely. `logistics` should quote policy verbatim. One prompt can't be all three.
- **Cost scales with the question.** `deadlines` runs at low effort; `planning` earns high effort and adaptive thinking.

**Cost of dropping the router:** we lose the explicit confidence signal and the ability to log a routing decision separately from the answer. Mitigate by having each specialist handler log its invocation with the question, so misroutes stay diagnosable. If selection accuracy turns out to be a real problem, a classifier can be reintroduced *inside* the Go orchestrator without touching the MCP surface — but don't build it preemptively.

Specialists can still **escalate**: a `deadlines` handler that finds the real question was about grades returns a short "this looks like a grades question" result rather than guessing, and the orchestrator re-dispatches.

## Retrieval: hybrid vector + structural

Vector search over chunks, combined with structural metadata filtering. The two answer different questions and neither is sufficient alone:

- **Vector search** finds *passages*: "which readings cover eigenvalues" works even when the module is titled "Week 9: Linear Transformations," and "explain the proof on slide 12" retrieves the slide rather than the whole 60-page deck.
- **Structural metadata** answers what embeddings provably cannot: "Exam 2 covers the modules between Exam 1 and now" is a date-and-ordering operation, not a similarity one.

**Pipeline for content-backed intents:**

1. **Structural narrowing** (plain Go over the store) — filter candidates by course, date range, module ordering, topic type. Cheap, exact, and does the reasoning similarity can't.
2. **Vector search** over chunks within that candidate set → top-K passages.
3. **Claude synthesizes** from the passages plus their document metadata, citing by title and slide/page.

Only `materials` and `logistics` use this. The other six intents are structured API calls — grades, deadlines, announcements, discussions, assignments, and planning read fields, not prose.

**Stack:**

| Piece | Choice | Why |
|---|---|---|
| Vector store | **chromem-go** | Pure Go, zero third-party deps, **no cgo**, optional disk persistence. 1k docs in 0.3 ms, 100k in 40 ms — a course library is well inside that. |
| Embeddings | **Voyage AI** (`voyage-4`) | Anthropic's recommended provider. 200M free tokens on the voyage-4 family; a full course library is a few million, so effectively free. |
| Chunking | Structure-aware | Per slide (PPTX), per heading (markdown/DOCX), else ~512-token windows with overlap. |

Two implementation notes:

- **chromem-go's metadata filtering is exact-match only** — no range queries. So step 1 runs in Go against the store, and each course gets its own collection (a natural partition). Over-fetch top-K generously and apply date/module filters to the results; at 0.3 ms per query that's free.
- **No built-in Voyage embedding function** — chromem-go ships helpers for OpenAI, Cohere, Mistral, Ollama and others, but Voyage needs a custom `chromem.EmbeddingFunc`. That's ~40 lines against their REST API with `net/http`.

chromem-go is pre-v1.0 and may make breaking changes, so put it behind a small `retrieve.Index` interface (`Add`, `Query`, `Delete`). If it needs replacing — sqlite-vec for full SQL range filtering, pgvector at real scale — the swap stays contained to one file. Chunks are re-embedded only when their content hash changes, which ingest already tracks.

**PDFs go to Claude, not a Go parser.** PDF text extraction is genuinely Go's weak spot — pure-Go libraries mangle multi-column layouts and tables, and the good option (`go-fitz`) needs cgo plus a MuPDF C library. Sidestep it: the API accepts PDFs natively as `document` blocks, so ingest sends each PDF to Claude once and caches the returned markdown. Layout- and table-aware extraction, no cgo, one-time cost per document.

| Format | Path |
|---|---|
| PDF | Claude document block at ingest → cached markdown |
| DOCX / PPTX | `archive/zip` + `encoding/xml` (stdlib — they're just ZIP+XML) |
| TXT / MD / HTML | direct read |
| Link-type topics | metadata only — record URL, flag as unreadable |

Ingest is content-hashed and idempotent; re-running processes only what changed.

## Storage: no database server

Three stores on disk, linked by content hash. No Postgres, no SQLite, no running service.

```
~/.cuny-ai/                  (configurable)
  manifest.json              # the document database
  blobs/sha256-<hash>        # original downloads (PDF, DOCX, PPTX)
  text/sha256-<hash>.md      # extracted markdown, keyed by blob hash
  vectors/MTH1003/           # chromem-go collection, one per course
  vectors/MTH3005/
```

**`manifest.json` is the document database** — one entry per topic:

```json
{
  "topic_id": 8842,
  "course_code": "MTH1003",
  "module_id": 771,
  "module_title": "Week 8: Integration by Parts",
  "title": "Lecture 8 Slides",
  "topic_type": 1,
  "mime": "application/pdf",
  "content_hash": "sha256-abc…",
  "released_at": "2026-03-02T00:00:00Z",
  "url": "https://brightspace.cuny.edu/d2l/le/content/…",
  "chunk_ids": ["mth1003-8842-0", "mth1003-8842-1"]
}
```

This is what powers structural narrowing: filtering by course, date range, and module order is a linear scan over a few thousand entries — microseconds in Go. `chunk_ids` links documents to vectors, and each chunk carries `topic_id` in its chromem metadata so search results resolve back to their source document with title, URL, and page.

**Why files:** content-addressing gives free dedupe and idempotent ingest; no server, no schema migrations, no cgo (SQLite in Go means cgo unless you use `modernc.org/sqlite`). It's also debuggable — `cat` the extracted markdown to see exactly what the model reads.

**Where it breaks:**

- `manifest.json` loads wholly into memory. Fine at thousands of documents, wrong at 100k.
- Single-writer assumption — ingest writes, the MCP server only reads. Concurrent writes corrupt it.
- No range queries in the store or in chromem-go, so date filtering happens in Go.

**Migration path:** SQLite via `modernc.org/sqlite` (still cgo-free) when the manifest outgrows memory or needs concurrent writes. That same move unlocks **sqlite-vec**, collapsing vectors and metadata into one file with real SQL range filtering — so both limitations have one coherent fix rather than two. Not worth building now; worth knowing the exit exists.

**The real risk: two sources of truth.** The manifest and the vector index must stay in sync. When a document changes, ingest must re-extract, re-chunk, **delete the superseded chunks from chromem**, then add the new ones. Get the delete wrong and orphaned vectors point at text that no longer exists — searches return confident citations to stale content, which is worse than returning nothing. This needs its own test, separate from the re-embedding check.

## Automatic ingest

**Brightspace has no content-change webhooks.** Data Streams push xAPI *activity* events (interactions), not row-level CRUD, so object sync is polling-only. Polling is cheap here though, because `LastModifiedDate` is present on both Module and Topic — it gates the expensive work:

```
poll TOC (JSON metadata only, ~1 call/course)
     │
     ├─ LastModifiedDate == manifest?  → skip
     └─ changed / new                  → download → extract → chunk → embed
                                          (the only step that costs money)
```

No query parameter filters by modification date, so fetch the full TOC and diff locally. Courses have hundreds of topics, not millions.

**Triggers, matched to situation:**

| Trigger | When | Mechanism |
|---|---|---|
| **fsnotify watcher** | v1 — no API access | Watch the local course folder; ingest on change, ~2s debounce (editors emit several events per save) |
| **launchd timer** | Once the API works | macOS-native, survives reboots. One-shot `ingest` every few hours — content changes on the order of days |
| **`ingest --watch`** | Development | Self-contained `time.Ticker` daemon, no OS config |

The fsnotify path is what makes v1 usable today: drop a lecture PDF into the folder and it's queryable seconds later, with no credentials.

**Four rails unattended operation needs that manual runs don't:**

1. **Lockfile** — two overlapping ingests corrupt the manifest, and a timer firing while a large course is still processing is the realistic case. `flock` the manifest; exit early if held.
2. **Atomic manifest writes** — write `manifest.json.tmp`, then `os.Rename` (atomic on POSIX) so a reader never sees a half-written manifest. Load-bearing once writes happen unattended.
3. **Rate-limit backoff** — polling every course tightly will trip D2L's limits. Default the interval to hours.
4. **Failure visibility** — a silent failure is worse than a manual one, because stale data still looks fresh. Record `last_successful_run` and per-course `last_polled` in the manifest; surface both in `ingest --verify`.

One cost note: content-hash gating means unchanged files never re-extract, but a professor *re-exporting* the same PDF produces a new hash and pays for extraction again. Acceptable, and worth logging so surprise spend is traceable.

## MCP: specialists as tools

**Specialists are exposed as MCP tools, and `tools/list` is the specialist registry.** The orchestrator discovers what specialists exist rather than hardcoding them, and adding a specialist is a one-line registry entry that every client picks up automatically.

MCP itself is a tool-transport protocol — it has no agent or orchestrator primitive. But nothing stops a tool *handler* from running a full agent loop, and that's exactly what makes "agent as tool" work: each `ask_*_specialist` handler owns a Claude call with its own prompt, tool subset, and effort.

**This removes the router.** Claude's native tool selection *is* the routing — one turn picks the specialist and calls it, instead of a classify-then-dispatch round trip. Effort tuning moves into each specialist's own handler, where it belongs. Fewer components, one less failure mode, one less hop of latency.

```
Claude Desktop / Claude Code / Go chat app     ← any MCP client orchestrates
            │ MCP · tools/list = specialist registry
┌───────────▼─────────────────────────────┐
│  --expose=specialists                    │
│  ask_materials · ask_grades · ask_deadlines … │
│  each handler = prompt + tools + effort  │
└───────────┬─────────────────────────────┘
            │ in-process
┌───────────▼─────────────────────────────┐
│  --expose=data                           │
│  search_content · get_grades · list_deadlines … │
│  brightspace client · store · retrieve   │
└──────────────────────────────────────────┘
```

**One binary, a `--expose` flag** — `specialists` (default), `data`, or `both`:

| Mode | For | Why |
|---|---|---|
| `specialists` | Claude Desktop, Claude Code, Cursor | High-level and clean. The client can't ship a Go router, so specialist-shaped tools are the only way to get specialist behavior there. |
| `data` | The Go chat app | It runs specialists in-process, so it wants raw tools — streaming, shared context, one level of inference. |
| `both` | Debugging | See the whole surface at once. |

**A specialist is a config, not a process** — prompt + tool subset + effort. The same definition is invoked in-process by the Go app *or* wrapped in an MCP tool handler. One registry, two entry points, no duplicated logic:

```go
// Single source of truth. Generates the in-process registry AND the MCP tool list.
var Specialists = []Specialist{
    {Intent: "materials", Purpose: "Find, summarize, and explain course content", ...},
    {Intent: "grades",    Purpose: "Grades, instructor feedback, what-if projections", ...},
    ...
}
```

### What the tool boundary costs

Three real costs, worth designing around rather than discovering later:

1. **No streaming.** MCP tool results return atomically, so the user waits with no output while a specialist runs a multi-step loop. Mitigate with MCP progress notifications for liveness — but partial text won't stream into the parent's response. This is why the Go app keeps specialists in-process.
2. **Context isn't shared.** The handler sees only its tool input. Conversation history and earlier clarifications are lost, so a follow-up like *"what about the other one?"* breaks. **Fix it in the schema**: every specialist tool takes an optional `context` field carrying relevant prior turns, and the orchestrator is instructed to populate it. Cheap, and it removes the sharpest edge.
3. **Nested inference.** Orchestrator turn → specialist tool → specialist's own loop. Two levels of model calls, so latency and cost stack. Inherent to the pattern and bounded — but it means the server needs its own Anthropic API key, which inverts the usual "MCP servers are dumb data access" assumption. Worth stating in the README so it isn't a surprise.

### Prior art and hosting

**Read the existing servers first.** Three community D2L MCP servers exist — `RohanMuppa/brightspace-mcp-server` (TypeScript, broadest surface), `bencered/d2l-mcp-server`, `joshuasoup/d2l-mcp` (12 tools, file download with text extraction). All expose data tools, none do specialists or hybrid retrieval, and none solve the OAuth blocker — but they're free reference for tool naming and schema shape. Build in Go with the official `modelcontextprotocol/go-sdk` (stdio transport, JSON schema generated from Go structs).

**If you want the agents genuinely hosted**, that's **Managed Agents**, not MCP: agents become persisted, versioned server-side objects, and `multiagent: {type: "coordinator", agents: [...]}` is coordinator-plus-roster as a native API feature, each subagent in its own thread with its own model and tools. It declares MCP servers with vault credentials, so it sits above this layer rather than replacing it. Not for v1 — CMA needs the server reachable over a URL rather than stdio, so it has to be hosted, which is a lot of machinery for a project running on fixtures. The layering keeps it a later option, not a rewrite.

## Package layout

Packages are grouped by which side of the MCP boundary they sit on.

```
cuny-ai/
  cmd/
    brightspace-mcp/main.go   # MCP server, stdio — --expose=specialists|data|both
    ingest/main.go            # pull course data → store + index
                              #   --watch   fsnotify on local folder, debounced
                              #   --verify  orphan check + last-run freshness
    chat/main.go              # Go orchestrator: specialists in-process, data via MCP
  internal/
    # ── specialist layer: agent-as-tool ──
    specialist/
      registry.go             # []Specialist — single source of truth
      run.go                  # generic tool-runner loop (in-process entry point)
      mcp.go                  # wraps registry as MCP tools (MCP entry point)
      prompts/                # one scoped system prompt per specialist
    # ── data layer: no model calls ──
    mcptools/
      tools.go                # data-tool defs + handlers (go-sdk)
    brightspace/
      client.go               # Client interface — mirrors Valence routes 1:1
      types.go                # structs matching D2L response schemas
      mock.go                 # MockClient over testdata fixtures
      live.go                 # LiveClient — OAuth2, stubbed until credentials exist
    store/
      store.go                # content-addressed store: index + markdown bodies
      extract.go              # DOCX/PPTX/TXT extraction; PDF → Claude
      chunk.go                # structure-aware chunking + chunk metadata
    retrieve/
      index.go                # Index interface — Add / Query / Delete
      chromem.go              # chromem-go implementation
      voyage.go               # custom chromem.EmbeddingFunc over Voyage REST
      hybrid.go               # structural narrowing → vector search → merge
    # ── orchestration (Go chat app only) ──
    orchestrator/
      chat.go                 # conversation loop, streams, dispatches specialists
      mcpclient.go            # connects to brightspace-mcp --expose=data
  testdata/courses/           # MTH1003, MTH3005 fixtures
```

The `Client` interface tracks real routes so `LiveClient` is a transport swap, not a redesign:

```go
type Client interface {
    MyEnrollments(ctx) ([]MyOrgUnitInfo, error)   // carries the access window
    ContentRoot(ctx, orgUnitID) ([]Module, error)
    ModuleStructure(ctx, orgUnitID, moduleID) ([]ContentObject, error)
    TopicFile(ctx, orgUnitID, topicID) (io.ReadCloser, string, error)
    DropboxFolders(ctx, orgUnitID) ([]DropboxFolder, error)
    MyGradeValues(ctx, orgUnitID) ([]GradeValue, error)
    MyFinalGrade(ctx, orgUnitID) (*GradeValue, error)
    NewsItems(ctx, orgUnitID, since time.Time) ([]NewsItem, error)
    DiscussionForums(ctx, orgUnitID) ([]Forum, error)
    DiscussionPosts(ctx, orgUnitID, forumID, topicID) ([]Post, error)
    MyEvents(ctx, orgUnitIDs []int, start, end time.Time) ([]CalendarEvent, error)

    // v2 — declared now so submission is a fill-in, not a refactor.
    SubmitToDropbox(ctx, orgUnitID, folderID int, comment string, files []Upload) (*Submission, error)
}
```

Structs mirror D2L's real JSON field names (`TopicType`, `OrgUnitId`, `DueDate`, `IsRead`, `Comments`) so live responses unmarshal with no translation layer. **Fixtures must match the real schema** — that's what makes the mock load-bearing instead of decorative.

## Model configuration

- **`claude-opus-5`** throughout — `anthropic.Model` is a string alias, so pass the bare ID.
- **Specialists:** adaptive thinking (on by default on Opus 5), effort `low` for lookups (`deadlines`, `announcements`), `high` for synthesis (`materials`, `planning`, grade projections). Effort is set per specialist in its handler — there's no router to assign it.
- **Specialist tool descriptions** are the routing signal. Write each `Purpose` as a trigger condition, not a label; prescriptive "call this when…" descriptions measurably improve selection.
- **Streaming** (`NewToolRunnerStreaming`) — answers cite multiple documents and run long; streaming avoids HTTP timeouts and gives live REPL output. `max_tokens: 64000`.
- **Prompt caching** on the shared tool definitions and system-prompt preamble once past Opus 5's 512-token minimum. Keep the stable preamble first and the per-specialist section after it, so specialists share a cached prefix.
- **Embeddings: Voyage `voyage-4`** via a custom `chromem.EmbeddingFunc` — a separate provider and API key from Anthropic. Free within the 200M-token tier at this scale.
- Confirm the exact Go bindings for `output_config.effort` and `OutputConfig.Format` against the SDK rather than guessing.

## Build order

Sequenced so each layer is independently usable before the next is built — data tools work in Claude Desktop at step 6, specialists at step 8, and the Go app only at step 9.

1. **`brightspace`** — interface, D2L-shaped types, `MockClient`, `ErrNotImplemented` for live and submission.
2. **Fixtures** — MTH1003 (weekly modules, 2 exams, mixed formats, grades with feedback, announcements) and MTH3005 (assignment folders with due dates, discussion forum). Real D2L JSON shapes.
3. **`store` + `extract` + `chunk`** — content-addressed store; stdlib DOCX/PPTX; PDF→Claude with caching; structure-aware chunking with per-chunk metadata (course, module, topic ID, title, date, slide/page).
4. **`retrieve`** — `Index` interface, chromem-go backing, Voyage embedding func, hybrid narrow-then-search. Testable standalone against fixture chunks before any agent exists.
5. **`ingest` binary** — enrollments → TOC → download → extract → chunk → embed → store + index. Idempotent, re-embedding only changed chunks. Includes the unattended rails from the start (lockfile, atomic manifest rename, `--verify`), since retrofitting them after something corrupts a manifest is worse. Add `--watch` (fsnotify on the local folder, debounced) here — it's what makes v1 usable before any API access.
6. **`brightspace-mcp --expose=data`** — wrap the layers above as MCP tools with `modelcontextprotocol/go-sdk` over stdio. **First usable milestone:** register it in Claude Desktop and query your courses with zero agent code. Read the three community D2L servers first for tool naming.
7. **`specialist` registry + in-process runner** — the `[]Specialist` source of truth, scoped prompts, generic tool-runner loop. Build `materials`, `deadlines`, `grades` first: spans a retrieval-heavy intent, a lookup, and a precision-sensitive one, so the shared loop gets stressed in all three modes.
8. **`--expose=specialists`** — wrap the same registry as MCP tools. **Second milestone, and the payoff:** Claude Desktop now sees `ask_materials_specialist` et al. via `tools/list` and routes to them natively, with no router and no Go orchestrator.
9. **`chat` binary** — Go REPL running specialists in-process against `--expose=data`; streams output; prints selected specialist, retrieved chunk titles, and tool calls in dim text so the path is visible.
10. **Remaining specialists** — `assignments`, `announcements`, `discussions`, `logistics`, `planning`. Registry entries plus prompts; both entry points pick them up automatically.
11. **`context` threading** — add the optional `context` field to specialist tool schemas and populate it, so follow-ups survive the tool boundary.
12. **Real-content mode** — a flag pointing the store at a local folder, so the user can hand-download their own MTH1003 files and query real material. No API, no approvals, honest demo.

## Verification

```bash
go build ./... && go test ./...
```

- **Tool-selection table** — the most valuable test here. A table of ~40 questions (the list above, plus deliberate ambiguities) → expected specialist. Runs one orchestrator turn with `tool_choice: any` and asserts which `ask_*_specialist` gets selected, stubbing the handlers so nothing executes. Cheap, and it catches the failure mode this design trades for: selection accuracy now depends on how each `Purpose` description is written, so it regresses silently when a description is edited.
- **Retrieval quality set** — ~20 question→expected-chunk pairs over the fixtures, scored by recall@5. Runs against the index alone, no agent, so it isolates retrieval from generation. This is what tells you whether hybrid search is actually earning its complexity: include cases that need vectors ("which readings cover eigenvalues" where the module is titled differently) and cases that need structural filtering ("what's on exam 2"), and watch both pass.
- **Unit** — DOCX/PPTX extraction against fixture files; chunk boundaries land on slide/heading edges; `MockClient` returns schema-valid responses; store round-trips and dedupes by hash; changed content triggers re-embedding, unchanged doesn't.
- **Unattended ingest** — drop a file into the watched folder and assert it becomes queryable; modify it and assert the old chunks are replaced. Then the cases only automation hits: two concurrent `ingest` runs (second must exit on the lock, not interleave), and a kill mid-write (manifest must still parse, thanks to the tmp+rename).
- **Store/index consistency** — the failure mode that produces confident citations to stale text. Ingest a document, mutate it, re-ingest, then assert: superseded chunks are gone from chromem, no `chunk_id` in the manifest lacks a vector, and no vector lacks a manifest entry. Run the orphan check as a standalone `ingest --verify` command too, so it's usable outside tests.
- **Integration** — per specialist, one scripted question asserting the *tool-call sequence* rather than exact prose (e.g. `materials` → `list_deadlines` → `search_content` → `read_documents`). Catches retrieval-logic regressions without being flaky on wording.
- **MCP conformance** — for each `--expose` mode, start the server over stdio, assert `tools/list` returns the expected schemas, and exercise each tool once. Assert the registry drives both entry points: adding a `Specialist` makes it appear in `tools/list` *and* in the in-process dispatcher, with no second edit.
- **Claude Desktop check** — register `--expose=specialists` and ask one question per intent by hand. This is the real test of the design: if the orchestrator picks the right specialist with no router and no Go code, the tool boundary is correct.
- **Context threading** — ask a specialist a question, then a bare follow-up ("what about the other one?"), and assert the `context` field was populated and the answer resolves the referent. This is the sharpest edge of agent-as-tool; test it explicitly rather than hoping.
- **End-to-end, manual** — `go run ./cmd/ingest && go run ./cmd/chat`, then walk one question per intent, confirming the routed intent is printed and the answer is grounded in fixture data.
- **Negative cases** — questions the data can't answer ("what's my roommate's grade", "what's due in a course I'm not enrolled in") must produce a clean "I don't have that" rather than a fabrication. Worth an explicit test: grade and deadline hallucinations are the failure mode that would make this untrustworthy.

## If API access ever materializes

Unlock order: faculty sponsor → CUNY Third-Party Tools request → admin registers an OAuth client → student consents via authorization-code flow.

**Scopes are fixed at registration — request the full set up front**, since widening later means going back to the admin:

| Scope | Serves |
|---|---|
| `enrollment:own_enrollment:read` | course list |
| `enrollment:orgunit:read` | `classlist/paged/` — instructor/TA lookup for `logistics` |
| `content:modules:readonly` · `content:topics:readonly` · `content:file:read` | `materials`, `logistics` |
| `calendar:my_events:read` | `deadlines`, `planning` |
| `quizzing:quizzes:read` | quiz and exam dates for `deadlines` |
| `dropbox:folders:read` | `assignments`, `deadlines` |
| `grades:own_grades:read` | `grades` |
| `news:newsitems:read` | `announcements` |
| `discussions:forums:readonly` · `discussions:topics:readonly` · `discussions:posts:readonly` | `discussions` |
| `dropbox:folders:write` | v2 submission |

### Sources and verification status

Every endpoint and scope above is read from D2L's official Valence documentation (`docs.valence.desire2learn.com`) — `res/news.html`, `res/grade.html`, `res/calendar.html`, `res/discuss.html`, `res/content.html`, `res/dropbox.html`, `res/enroll.html`, `res/quiz.html`, `basic/oauth2.html`.

**None of it has been executed against a live tenant** — there are no credentials yet. Two gaps to close on day one of live access:

- **Version numbers are unpinned.** The docs write `(version)`; LP and LE version independently. Pin both against the tenant before writing `LiveClient`.
- **Response shapes are unvalidated.** The plan depends on fixtures matching real schemas — capture real responses on first connection and diff them against `testdata/` rather than assuming.

**Known org-configurable behavior:** `classlist` field visibility (email, username, role) is controlled by the Classlist tool's settings, so "who's my TA?" may return a name without contact details depending on how CUNY configured it. `logistics` should degrade gracefully rather than treat missing fields as an error.

Use the **authorization code grant**, not client credentials: submissions are attributed to the calling token, so it must be the student's own. When submission lands in v2, gate the write behind an explicit confirmation showing the resolved folder name and due date — a wrong-folder submission is a real, hard-to-undo consequence in someone's gradebook.
