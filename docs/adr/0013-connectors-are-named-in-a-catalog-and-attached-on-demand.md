# 13. Connectors are named in a catalog and attached on demand

This decision records which MCP servers a session gets. It amends ADR 0011, which gave every session the repo's whole set.

## Context

ADR 0011 keeps a session's context to what diatom passes in, and configures MCP servers per repo. Every session then gets every server. A server's tool schemas sit in the system prompt whether the session calls the server or not, so a repo with a dozen connectors pays for all twelve on every session.

Most sessions need none of them. A goal of 28 tasks may need Datadog for three and GitHub for one. Denying the tools in a profile does not help: the schemas still load, and only the calls are blocked.

The need is also not fixed per profile. Grilling needs the ticket tracker for one goal and the metrics for the next. A server a profile always carried would be back to paying for context the session never uses.

The candidates for choosing were:

- **Per profile, in config**: free and predictable, but too coarse, and wrong for planning, whose need changes per goal.
- **Per goal**: still spends 28 tasks' context on what 3 tasks need.
- **Rules over a task's text and paths**: free, but a guess, and brittle across a dozen servers.
- **A router call on the cheapest profile**: a guess too, made before the session reads anything.
- **The session asks**: exact, because the agent is inside the task, but it costs a restart.

## Decision

**Config names the connectors. A session is given the ones its work declares, and may ask for more.**

- **The catalog is the repo's own `.mcp.json`.** Claude Code's project file is where a repo already declares its servers, so diatom reads it and nothing has to be declared twice. `[mcpServers.<name>]` in diatom's config merges on top of it, by name, which is how a server gets a purpose or how a server diatom alone should see is added.
- **Every server may have a purpose.** `purpose` is one line saying what the server is for. The names and purposes are the catalog. It is optional, because `.mcp.json` has no such field and most names say enough on their own.
- **The catalog is in every prompt; the schemas are not.** A dozen names with one line each costs little, and the agent cannot ask for a server it does not know about. The tool schemas, which are the real cost, load only for the servers a session is given.
- **A task declares its connectors.** A plan's task and a queue task carry `mcpServers`, a list of catalog names. Grilling writes them, because it has just done the research and knows which tasks need live data. A task that declares none gets none, which is almost all of them.
- **Grilling harvests rather than defers.** What a planning session reads from a connector belongs in the plan summary and the task bodies as plain text. A task declares a connector only when the data is live or unbounded, such as a current error rate, not when the lookup could have been done once.
- **A session may ask for a connector.** The task tool takes a request naming a catalog entry and why. The session ends, and the harness runs the batch again with the server attached, as it already does for a task handed back unstarted (ADR 0004). The cost is one prompt write: changing the set of servers changes the system prompt, so the cached prefix is lost whether the session resumes or not. The tasks themselves are untouched.
- **An attached server's tools are allowed.** Sessions run in Claude Code's don't-ask mode, where nothing prompts, so a tool that is not pre-approved is a tool denied. Attaching a server and then denying its calls is the worst of both: the session pays for the schemas and cannot use them. The decision of whether the agent may reach a server is the attaching, which the catalog and the request path already govern, not a second list of tool names.
- **A batch takes the union of its tasks' connectors**, capped. Splitting a batch by connector would cost more sessions than the context it saves. The cap stops a batch of five tasks loading five sets of schemas, which is the problem this decision starts from.
- **A planning session carries the tracker.** When `tickets` names a server in the catalog, triage and grilling get it without asking, first in the list so the cap never drops it. Every goal they hand in needs a ticket (ADR 0014), so the alternative is a session spent asking for the tracker on the way to every goal, or a question to the human for a ticket they only have to paste back.
- **Planning sessions may be routed.** Triage and grilling start from free-form text, where the signal is legible and a restart part-way through a round is expensive. A call on the cheapest profile, like the one that writes commit messages (ADR 0005), reads that text and the catalog and returns names. Implementation sessions are not routed: the plan's declaration is written with more knowledge than such a call has.

The request path is what makes the rest safe. Every other step is allowed to give a session nothing, because a session that needs a connector can say so, and being wrong costs one prompt write instead of a failed task. ADR 0011's property holds: no server reaches a session unless the repo names it, and a plan, a routing call or the agent itself asks for it by name. Reading `.mcp.json` does not weaken it: the file is the repo's, committed and reviewed, not the user's own machine-wide set, and a server in it still reaches no session until something asks.
