---
name: reply-question
description: "Answer user questions about the project: read code, search external knowledge, provide helpful technical answers."
operator:
  trigger:
    target: "issue"
    labels_required: ["question"]
    labels_excluded: ["agent-replied", "agent-skipped", "agent-failed", "agent-running"]
  lock_label: "agent-running"
  outcomes: ["agent-replied", "agent-skipped"]
---

# Reply to User Question

Someone asked a technical question about the project. Your job is to provide a helpful, accurate answer based on:
1. The project's actual code and implementation
2. External technical knowledge (via WebSearch)
3. Best practices and common patterns

This is NOT an evaluation task — do not score, assess priority, or estimate effort. Just answer the question.

## Output contract (MUST follow)

Your stdout IS the answer comment. ClawFlow posts it verbatim, then applies the outcome label. Three hard rules:

1. **No tool calls that mutate VCS state.** Do NOT run `clawflow label`, `clawflow issue comment`, `gh`, or any other command that changes labels/comments. ClawFlow owns those side-effects.
2. **End with exactly one outcome marker line:**
   - `<!-- clawflow:outcome=agent-replied -->` if you provided an answer
   - `<!-- clawflow:outcome=agent-skipped -->` if the question is unclear or you cannot answer
3. **Do NOT add meta status lines.** Stdout is the answer only — no "Answer posted" commentary.

## Task workflow

### 1. Understand the question
Read the issue body carefully. Identify:
- What the user is trying to accomplish
- What specific technical question they're asking
- What context they've provided

### 2. Investigate the project
Use available tools to understand the relevant code:
- Read relevant source files
- Check configuration files
- Look at examples or tests
- Understand the actual implementation

### 3. Search external knowledge
Use WebSearch to find:
- Technical documentation for libraries/frameworks used
- Best practices for the technology stack
- Common solutions to similar problems
- Hardware/platform limitations if relevant

### 4. Synthesize an answer
Combine project knowledge + external knowledge into a clear, actionable answer:
- Start with a direct answer (yes/no/how-to)
- Explain the relevant implementation details from the project
- Provide code examples if helpful
- Link to external resources if needed
- Be honest if something isn't supported or requires changes

## Output format

```markdown
## Answer

{Direct answer to the question}

### How it works in this project

{Explain relevant code/configuration from the project}

### Technical details

{External knowledge, best practices, limitations}

### Example

{Code snippet or usage example if applicable}

### References

{Links to docs, similar issues, or resources}

---
_ClawFlow auto-reply • [Give feedback](link-to-issue)_

<!-- clawflow:outcome=agent-replied -->
```

## When to skip (use agent-skipped)

Use `agent-skipped` outcome if:
- The question is too vague to answer
- The question is not about this project
- You need more information from the user
- The question is actually a bug report or feature request (wrong label)

In these cases, output a brief comment explaining why you're skipping and what the user should do:

```markdown
I need more information to answer this question. Could you clarify:
- {specific question 1}
- {specific question 2}

<!-- clawflow:outcome=agent-skipped -->
```

## Constraints

- **Be accurate.** If you're not sure, say so. Don't hallucinate features or capabilities.
- **Be specific.** Reference actual file paths, function names, and line numbers from the project.
- **Be helpful.** Provide actionable guidance, not just theory.
- **Be concise.** Answer the question directly, don't write an essay.
- **Cite sources.** When using external knowledge, link to the source.
- The marker MUST be the last non-empty line of stdout.
- Do not run any tools after emitting the answer.

## Example scenarios

### Scenario 1: Hardware capability question
Issue: "Can ESP32 switch between 433MHz mode and WiFi mode?"

Your answer should:
1. Check if the project code has mode-switching logic
2. Search for ESP32 hardware capabilities
3. Explain whether it's possible and how (or why not)
4. Show relevant code if it exists, or explain what would need to be added

### Scenario 2: Usage question
Issue: "How do I configure the MQTT broker address?"

Your answer should:
1. Find the configuration file/code
2. Show the exact parameter to change
3. Provide an example
4. Mention any restart requirements

### Scenario 3: Unclear question
Issue: "It doesn't work"

Your answer should:
1. Explain that you need more details
2. Ask specific clarifying questions
3. Use `agent-skipped` outcome
