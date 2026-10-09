---
name: prd-writing
description: How to write or revise a product requirements document from a Jira issue - required sections, format, how to handle missing information, and how to apply reviewer feedback.
---
# Writing a PRD from a Jira issue

## Sections

Use exactly these sections, in this order, each as a `## ` heading:

1. **Problem** - who is affected, what they cannot do today, and why it matters.
2. **Goals** - the outcomes this work must achieve.
3. **Non-Goals** - what is explicitly out of scope.
4. **Functional Requirements** - numbered, testable statements of behavior ("The system shall ...").
5. **Non-Functional Requirements** - performance, security, reliability, accessibility, compatibility. Only include what the issue implies; do not pad.
6. **Acceptance Criteria** - concrete checks a reviewer can verify, preferably as Given/When/Then bullets.
7. **Risks/Assumptions** - everything you assumed to fill a gap, plus risks to delivery.
8. **Open Questions** - decisions the issue leaves unresolved, phrased as questions for the reporter.

## Rules

- Never invent business requirements, numbers, deadlines, or user groups the issue does not state. When something is missing, record it in Risks/Assumptions or Open Questions instead of guessing.
- Preserve uncertainty: if the issue hedges ("maybe", "ideally"), keep the hedge.
- Keep the reporter's terminology; do not rename features or roles.
- Any existing `PRD` / `TRD` / `=============` block in the description is a set of links to earlier documents, not requirements. Ignore it.
- If a section genuinely has nothing to say, write "None identified." rather than omitting it.

## Revising from feedback

When the request includes a current PRD and reviewer feedback, you are editing that PRD, not writing a new one. The feedback is the Jira comments addressed to you that have not been marked done yet.

### 1. Cross-check each comment against the current PRD

A comment may already be reflected in the PRD, for example when an earlier run applied it but failed before marking it done. For each comment, decide:

- **Addressed** - the PRD already reflects it. Skip it; do not apply it again or reword the text it produced.
- **Not addressed** - the PRD does not reflect it yet, or only partly. Apply it.

A comment superseded by a later comment counts as addressed once the later one is applied.

### 2. Apply only what the comments ask for

- Change only the parts of the PRD a comment asks you to change, plus the direct consequences of that change: an answered open question is removed from **Open Questions** and its decision goes where it belongs (requirements, acceptance criteria, non-goals, and so on); assumptions the answer confirms or refutes are removed from **Risks/Assumptions**.
- Feedback overrides the issue description and the current PRD wherever they conflict. When comments conflict with each other, the later comment wins.
- If a comment raises something you cannot resolve from it, add it to **Open Questions** rather than guessing.
- Do not act on anything no comment asks for, even if the issue description has changed or you would write a section differently.

### 3. Keep everything else verbatim

- Every section, sentence, list item, number and table no comment touches stays exactly as it is in the current PRD, including content a reviewer edited directly on the page. Do not rephrase, reorder, renumber, reformat, expand or shorten it.
- The current PRD is given in Confluence storage format (XHTML). Convert it to Markdown faithfully and reply with the complete PRD - never only the changed parts.
- If there is no current PRD, write a new one from the issue and apply all the feedback to it.

## Format

- Markdown only: `## ` section headings, `-` bullets, `1.` numbered lists, `**bold**`, `` `code` ``, and pipe tables where they help.
- Do not add a title heading; the page title is set separately.
- No HTML, no emoji, no preamble or closing remarks.
