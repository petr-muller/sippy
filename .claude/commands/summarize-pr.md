---
name: summarize-pr
description: Summarize a Sippy pull request with context, intent, observable impact, and risks.
parameters:
  - name: pr
    description: "PR number or URL (e.g., 3200 or https://github.com/openshift/sippy/pull/3200)"
    required: true
---

You are helping a Sippy stakeholder understand the high-level impact of a pull request to the openshift/sippy repository. The reader does not intend to review the code in detail but needs to understand what the PR does, why, and what risks it carries.

## Context: Sippy

Sippy (CIPI - Continuous Integration Private Investigator) is a tool used within the OpenShift engineering organization to analyze CI job results. Key areas include:
- **Go API backend**: serves data about job/test pass rates, regressions, component readiness, and release health
- **React/Material-UI frontend** (`sippy-ng/`): dashboards for CI health, component readiness, release overview, and job/test detail views
- **Component Readiness**: statistical analysis comparing test pass rates between a base and sample release to detect regressions
- **Data pipeline**: ingests data from BigQuery and stores in PostgreSQL; includes periodic data refresh jobs
- **Release health monitoring**: tracks payload acceptance rates, blocking jobs, and infrastructure health
- **API endpoints**: serve job runs, test results, component readiness reports, release health data, and bug/Jira associations

Changes here affect how OpenShift engineering understands CI health, makes release decisions, and triages regressions.

## Your Task

Given PR: "{{pr}}"

1. If the input is a full GitHub URL, extract the PR number. If it's just a number, use it directly against the `openshift/sippy` repository.
2. Use `gh pr view <number> --repo openshift/sippy --json title,body,state,author,labels,files` to fetch PR metadata.
3. Use `gh pr diff <number> --repo openshift/sippy` to fetch the code diff.
4. If the diff is large or touches areas that need more context to understand, read the relevant source files in this repository to understand what the changed code does. Focus on understanding the component's role in Sippy.
5. Write a summary with the following structure:

### Output Format

**Context & Intent** (2-3 sentences): What component or area of Sippy is being changed, and why. Identify the specific area affected (API, frontend, data pipeline, component readiness, release health, etc.) and the motivation (bug fix, new feature, optimization, configuration change, etc.).

**Value Delivered** (1-2 sentences): What concrete improvement or fix does this PR provide? What problem does it solve or what capability does it add?

**Observable Changes** (2-3 sentences): What would a Sippy user (release engineer, TRT member, developer checking CI health) notice after this PR merges? Think about changes to dashboards, reports, API responses, data accuracy, performance, or alerting.

**Risks** (2-3 sentences): What could go wrong? Consider: blast radius (cosmetic UI change vs. data pipeline logic), backwards compatibility (API changes, database migrations), failure modes (what happens if the new code has a bug), data correctness implications, and whether the change is tested adequately. If the risk is minimal, say so and explain why.

## Guidelines

- Keep each section concise. The entire summary should be quickly scannable.
- Use plain language accessible to someone who knows Sippy's purpose but hasn't been reading the code recently.
- Don't list individual files changed or quote code. Summarize at the conceptual level.
- When assessing risk, be calibrated: a change to component readiness statistical logic or data pipeline is higher risk than a UI tweak. A database migration is higher risk than a new API endpoint. A change to how pass rates are calculated is higher risk than adding a column to a table.
- If the PR is trivial (typo fix, dependency bump, test-only change), say so briefly without forcing full sections.
- If the PR touches test code, mention what it tests and whether test coverage appears adequate for the production changes.
