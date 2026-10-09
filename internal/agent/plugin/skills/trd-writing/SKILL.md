---
name: trd-writing
description: How to study a repository and write a technical requirements document (TRD) that plans the code changes for an approved PRD - exploration steps, required sections, format, and rules.
---
# Writing a TRD from a PRD and a repository

A TRD tells an engineer exactly how the codebase will change to deliver the PRD. It is a plan, not the implementation.

## 1. Understand the codebase first

Before reading the PRD closely, build a picture of the repository:

- Read the README and any design docs (for example under `docs/`).
- Read the package or build manifest (`package.json`, `go.mod`, `pyproject.toml`, ...) to learn the language, framework, scripts and test runner.
- Use Glob to see the directory layout, then read the entry points and the main modules.
- Note the conventions: how state and data are modeled, naming, styling approach, error handling, and how tests are written and where they live.

## 2. Map the PRD onto the code

- Read the PRD in full. The functional requirements, acceptance criteria and non-goals drive the plan; the problem and goals give context.
- For each requirement, use Grep and Glob to find every place it touches (data types, logic, UI, API, persistence, config, tests) and read those files completely, not just the matching lines.
- Read the existing tests for the code you will change.
- If the PRD still has open questions, do not resolve them yourself. Choose the least committal design, state the assumption, and carry the question into **Open Questions**.

## 3. Write the TRD

Use exactly these sections, in this order, each as a `## ` heading:

1. **Summary** - two to four sentences: what changes technically and why.
2. **Current State** - the parts of the codebase this work touches, as they are today: stack, relevant files (with repository-relative paths), key types and functions, and how data flows through them.
3. **Proposed Changes** - one `### ` subsection per file or component, in dependency order. For each: the path, whether it is new or modified, what changes, and why. Short snippets are allowed for new types, signatures or data shapes; do not write the full implementation.
4. **Data Model and Interface Changes** - new or changed types, schemas, stored data, props, APIs or configuration, and how existing data stays compatible. Write "None." if there are none.
5. **Implementation Plan** - numbered, ordered steps small enough for one commit each, each naming the files it touches.
6. **Testing Strategy** - using the repository's existing test framework and conventions: which test files to add or change and which cases they cover, plus any manual checks.
7. **Requirement Traceability** - a table mapping every PRD functional requirement and acceptance criterion to the proposed changes and tests that satisfy it.
8. **Risks and Alternatives** - technical risks, edge cases, migration or compatibility concerns, and alternatives you rejected with the reason.
9. **Open Questions** - technical decisions that need a human, plus any PRD open questions that affect the design.

## Revising from feedback

When the request includes a current TRD and reviewer feedback, you are editing that TRD, not writing a new one. The feedback is the Jira comments addressed to you that have not been marked done yet.

1. **Cross-check each comment against the current TRD.** A comment may already be reflected, for example when an earlier run applied it but failed before marking it done. Skip comments the TRD already addresses; do not apply them again or reword the text they produced. A comment superseded by a later one counts as addressed once the later one is applied.
2. **Check the code before changing the plan.** For each comment not yet addressed, read the files it concerns so the revised plan stays accurate to the repository. If a comment asks for something the code makes infeasible or risky, apply the closest safe version and explain it under **Risks and Alternatives**, or raise it under **Open Questions**.
3. **Apply only what the comments ask for.** Change the parts a comment asks you to change, plus their direct consequences elsewhere in the TRD: affected steps in **Implementation Plan**, tests in **Testing Strategy**, rows in **Requirement Traceability**, and answered items removed from **Open Questions**. Feedback overrides the current TRD; when comments conflict, the later comment wins.
4. **Keep everything else verbatim.** Every section, sentence, list item, number, table and snippet no comment touches stays exactly as it is, including content a reviewer edited directly on the page. Do not rephrase, reorder, renumber or re-plan it.

The current TRD is given in Confluence storage format (XHTML). Convert it to Markdown faithfully and reply with the complete TRD - never only the changed parts. If there is no current TRD, write a new one as described in sections 1 to 3 of this skill and apply all the feedback to it.

## Rules

- Only name files, functions and behaviors you have read. If something needed does not exist yet, say it is new.
- Follow the repository's existing patterns and libraries; do not introduce new dependencies unless the PRD cannot be met without one, and then justify it under **Risks and Alternatives**.
- Stay within the PRD's scope. Do not plan refactors or features it does not ask for.
- Every PRD functional requirement must appear in **Requirement Traceability**. If one cannot be met, say why.
- Any existing `PRD` / `TRD` / `=============` block in the issue description is a set of links to earlier documents, not requirements. Ignore it.

## Format

- Markdown only: `## ` section headings, `### ` subsections, `-` bullets, `1.` numbered lists, `**bold**`, `` `code` ``, fenced code blocks and pipe tables.
- Do not add a title heading; the page title is set separately.
- No HTML, no emoji, no preamble or closing remarks.
