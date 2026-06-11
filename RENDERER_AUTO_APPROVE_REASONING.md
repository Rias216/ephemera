# Renderer, auto-approve, and Beneath the Surface upgrade

## Right-edge artifact fix

The transcript no longer delegates visible-row padding to `viewport.View()`.
Ephemera now selects the viewport's visible content directly, clips it with an
ANSI-aware cell cutter, and paints every missing cell with the panel background.
This removes the one-cell black strip that could appear after nested ANSI resets.

The renderer also targets the complete viewport width instead of leaving two
cells for Bubbles to fill with unstyled spaces.

## Auto-approve

Use either command:

```text
/agent auto
/approval auto
```

Every supported tool request is executed immediately without an approval prompt.
Use `/agent safe` or `/approval safe` to restore approval prompts for writes,
shell commands, and tests.

Auto-approve removes the confirmation gate; normal workspace path validation,
unknown-tool handling, and destructive-command checks still apply.

## Beneath the Surface

Use:

```text
/thinking on
```

The agent timeline will show a concise structured decision trace:

- goal;
- material assumptions;
- approach;
- tool rationale;
- verification method.

This is an intentionally compact, user-facing rationale rather than raw private
chain-of-thought. Use `/thinking off` to hide it or `/thinking toggle` to switch
its state.
