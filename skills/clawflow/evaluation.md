# Phase 3 — Evaluation Strategy and Comment Templates

## Step 0 — Load Project Context

Before evaluating any issue, the orchestrator **must** load the target repo's project context:

```bash
# Get the repo's local path
LOCAL_PATH=$(clawflow config show --repo {owner}/{repo} --field local_path)

# If CLAUDE.md doesn't exist, generate it first
if [ ! -f "$LOCAL_PATH/CLAUDE.md" ]; then
  cd "$LOCAL_PATH"
  # Execute Claude Code /init to generate CLAUDE.md
fi

# Read CLAUDE.md as project context for evaluation
cat "$LOCAL_PATH/CLAUDE.md"
```

This gives the evaluator:
- Project directory structure and module responsibilities
- Architecture conventions and design patterns
- Key dependencies and constraints
- Test strategy and coverage information

> All subsequent evaluation steps must reference this context. Do not guess file paths or architecture — verify against the actual project structure.

---

## Evaluation Strategy (by Issue Type)

Based on the issue's `labels` field, apply the corresponding evaluation strategy:

**Type determination rules (by priority):**
1. Labels contain `bug` → Bug type evaluation
2. Labels contain `enhancement` or `feat` → Feature type evaluation
3. Neither of the above → General evaluation (fallback)

---

### Bug Type Evaluation

For issues labeled `bug`, evaluate **reproducibility**:

| Dimension | Criteria | Score (1-10) |
|-----------|----------|--------------|
| **Reproducibility** | Can the issue be reproduced from the description? Are there clear reproduction steps? | Clear repro = high, cannot reproduce = low |
| **Root Cause** | Can the problem be traced to specific code? Is the root cause clear? | Located = high, vague = low |
| **Fix Complexity** | Is the fix simple and straightforward? Does it touch core logic? | Single-point fix = high, systemic changes = low |

**Bug evaluation requires code verification:**
- Root cause analysis must reference actual file paths and function names found via grep/read in the repo
- Do not guess file locations — verify they exist in the project structure
- Cross-reference with CLAUDE.md to understand which module the bug belongs to

**Bug evaluation output:**
- **Reproduction steps**: How to reproduce this bug?
- **Root cause analysis**: Where is the problem? Which file/function? (must be verified paths)
- **Fix suggestion**: How to fix it? What is the change scope?

### Feature Type Evaluation

For issues labeled `enhancement` or `feat`, evaluate **implementation plan and architecture alignment**:

| Dimension | Criteria | Score (1-10) |
|-----------|----------|--------------|
| **Requirements Clarity** | Are the feature requirements clear? Are inputs and outputs well defined? | Clear = high, vague = low |
| **Design Soundness** | Is the proposed design reasonable? Does it align with the overall project architecture? | Aligned = high, diverged = low |
| **Confirmation Necessity** | Does this implementation involve significant design decisions requiring owner confirmation? | No confirmation needed = high, needs confirmation = low |

**Feature evaluation requires project structure verification:**
- Change scope must be based on actual project structure (grep/glob the repo to find relevant files)
- Architecture alignment must reference conventions from CLAUDE.md, not generic assumptions
- If the feature introduces new files, specify which existing directory they belong to and why (based on project conventions)
- Technology choices must be consistent with existing dependencies (check go.mod / package.json / etc.)

**Feature evaluation output:**
- **Implementation plan**: How to implement this feature? Specific steps?
- **Technology choices**: What tech/libraries/APIs to use? (must be consistent with existing deps)
- **Change scope**: Which files/modules need to be modified? (must be verified paths from the repo)
- **Architecture alignment analysis**: Does the design follow the project's conventions per CLAUDE.md?
- **Owner confirmation flag**: Does the owner need to confirm at the design level? (Yes/No)

### General Evaluation (No Type Label Fallback)

For issues without `bug`, `enhancement`, or `feat` labels, infer the type first then evaluate:

1. **Type inference**: Based on title and body, determine if it's a bug (describes abnormal behavior/errors) or a feature (describes new functionality/improvements), and note the inferred type in the evaluation report
2. **Project context verification**: Same as Bug/Feature — grep the repo to verify relevant files exist before referencing them
3. **Evaluation dimensions**: Apply the corresponding Bug or Feature evaluation dimensions based on the inferred type
4. **Label suggestion**: In the evaluation comment, suggest the owner add the appropriate type label (`bug` or `enhancement`)

**Confidence Score = (Dimension1 + Dimension2 + Dimension3) / 3**

---

## Split Suggestion Evaluation

After completing the confidence evaluation, determine whether a split is recommended (any one condition triggers the suggestion):

| Trigger Condition | Description |
|-------------------|-------------|
| Involves 2+ independent modules/features | Each feature can be implemented and tested independently |
| Estimated changes exceed 5 files | Change scope is too large for a reviewable PR |
| Issue body contains multiple independent TODO items | Each TODO can stand alone as a separate issue |

**Note: Sub-issues do not trigger split suggestions (split depth is limited to 1 level).**
Detection: check if the issue body contains `Parent Issue: #` — if so, it's a sub-issue; skip split suggestion.

### Split Suggestion Comment Template (appended to evaluation report)

When split conditions are met, append to the end of the evaluation comment:

```
---

### 🔀 Split Suggestion

This issue is recommended to be split into the following sub-tasks:

1. **Sub-task 1**: {title} — {one-line description}
2. **Sub-task 2**: {title} — {one-line description}

**Reason for split:** {trigger condition description}

If you agree to split, add the `ready-for-agent` label to trigger automatic sub-issue creation.
If you prefer not to split, please leave a comment explaining and then add `ready-for-agent`.
```

---

## High Confidence Handling (Fix Recommended)

For issues with confidence >= threshold:

```bash
clawflow label add --repo {owner}/{repo} --issue {number} --label agent-evaluated
clawflow issue comment --repo {owner}/{repo} --issue {number} --body "<evaluation_body>"
```

### Bug Type Comment Template

```
## 🔍 ClawFlow Evaluation Report

**Issue Type:** Bug
**Confidence:** {score}/10 ✅ (above threshold {threshold})

---

### Reproducibility Analysis

**Reproducibility:** {reproducibility}/10 — {repro_reason}
**Root Cause:** {root_cause}/10 — {root_reason}
**Fix Complexity:** {fix_difficulty}/10 — {fix_reason}

**Reproduction Steps:**
{repro_steps}

**⚠️ Reproduction Verification Status:** {reproduction_verified} ⚠️
- **Verification Result:** {verify_result}
- **Verification Details:** {verify_details}

**Root Cause Analysis:**
{root_cause_analysis}

**Fix Suggestion:**
{fix_suggestion}

---

👉 **If you agree with this plan, please manually add the `ready-for-agent` label to trigger auto-fix.**

⚠️ Note: The agent will not add this label automatically — it requires owner confirmation.

> If the repo has `auto_fix: true` enabled and confidence >= 7.0, ClawFlow has already added `ready-for-agent` automatically — no manual action needed.

---
🤖 Powered by [ClawFlow](https://github.com/zhoushoujianwork/clawflow) — automated issue → fix → PR pipeline
```

### Feature Type Comment Template

```
## 🔍 ClawFlow Evaluation Report

**Issue Type:** Feature
**Confidence:** {score}/10 ✅ (above threshold {threshold})

---

### Implementation Plan Analysis

**Requirements Clarity:** {clarity}/10 — {clarity_reason}
**Design Soundness:** {design}/10 — {design_reason}
**Confirmation Necessity:** {confirmation}/10 — {confirm_reason}

**Implementation Plan:**
{implementation_plan}

**Technology Choices:**
{tech_choice}

**Change Scope:**
{change_scope}

---

### 🏗️ Architecture Alignment Analysis

**Architecture Consistency:** {arch_alignment} — {arch_reason}

> {architecture_notes}

**Owner Confirmation Flag:** {owner_confirmation_flag} ⚠️
- **Confirmation Required:** {need_owner_confirmation}
- **Reason:** {confirmation_reason}

---

👉 **If you agree with this plan, please manually add the `ready-for-agent` label to trigger auto-fix.**

⚠️ Note: The agent will not add this label automatically — it requires owner confirmation.

> If the repo has `auto_fix: true` enabled and confidence >= 7.0, ClawFlow has already added `ready-for-agent` automatically — no manual action needed.

---
🤖 Powered by [ClawFlow](https://github.com/zhoushoujianwork/clawflow) — automated issue → fix → PR pipeline
```

---

## Low Confidence Handling (Additional Information Required)

For issues with confidence < threshold:

```bash
clawflow label add --repo {owner}/{repo} --issue {number} --label agent-evaluated
clawflow label add --repo {owner}/{repo} --issue {number} --label agent-skipped
clawflow issue comment --repo {owner}/{repo} --issue {number} --body "<missing_info_body>"
```

### Low Confidence Comment Template

```
## 🔍 ClawFlow Evaluation Report

**Issue Type:** {type}
**Confidence:** {score}/10 ⚠️ (below threshold {threshold})

---

### Evaluation Details

{evaluation_details}

**Information needed:**
{missing_info}

---

💡 Please provide the above information, then remove the `agent-skipped` label and add `ready-for-agent` to re-trigger evaluation.

---
🤖 Powered by [ClawFlow](https://github.com/zhoushoujianwork/clawflow) — automated issue → fix → PR pipeline
```
