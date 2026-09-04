---
name: project-development-workflow
description: Plan and implement features in the vacancies-scrapper project using its documentation journal, tests-first workflow, backlog, and final TЗ review. Use for project feature work; not for one-off general questions.
---

# Project development workflow

Read [AGENTS.md](../../../AGENTS.md) before changing the project. It is the
authoritative local process; this skill only routes you to the relevant
artifacts.

1. Read [_docs/backlog.md](../../../_docs/backlog.md) and the TЗ sections
   relevant to the request. Preserve unrelated uncommitted work.
2. Before code, create a dated record in `_docs/implementation/` from
   [_docs/templates/implementation-record.md](../../../_docs/templates/implementation-record.md)
   and mark its backlog item `в работе`.
3. Add tests before implementation whenever behaviour can be isolated. For
   documentation-only changes, state why tests do not apply in the record.
4. Keep non-obvious invariants and transactions commented. After implementation
   run the checks required by `AGENTS.md`.
5. Re-read the applicable TЗ, record actual results, constraints and next step;
   set the record and backlog item to `выполнено` only after successful checks.

## Routing

* Schema, repositories, vacancies, run history: read
  [_docs/02-data-model.md](../../../_docs/02-data-model.md).
* YAML, browser scraping, dry run: read
  [_docs/03-scraping-configuration.md](../../../_docs/03-scraping-configuration.md).
* HTTP API and screens: read [_docs/04-api-ui.md](../../../_docs/04-api-ui.md).
* Execution order and completion criteria: read
  [_docs/05-implementation-plan.md](../../../_docs/05-implementation-plan.md).
