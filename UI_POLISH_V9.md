# UI polish v9 — guided workflow and quality-of-life pass

This revision focuses on making the interface easier to operate, especially the
provider connection flow, while preserving Bubble Tea v2's stable cell-diff
rendering and full-canvas layout.

## Connection workflow

- Setup is now a five-stage flow: provider, details, credentials, model, review.
- The active route is not changed until the review step is confirmed.
- Shift+Tab (or Backspace on an empty field) returns to the previous step and
  restores the value already entered there.
- A live route preview shows provider, endpoint, credential source, and model.
- Provider rows identify local, cloud, preset, and custom routes.
- Detected environment credentials are surfaced without revealing their value.
- API keys remain runtime-only and no raw credential is retained in model-cache
  keys.
- Current-field validation, defaults, and requirements are shown inline.

## Palette navigation

- More choices remain visible during provider and model selection.
- The selected row stays near the center of long result windows.
- Ctrl+N/Ctrl+P, Home/End, and PgUp/PgDn provide faster navigation.
- Esc closes the normal command palette and Ctrl+L clears the active field.
- Headers show position, overflow direction, current setup step, and readiness.

## Visual refinement

- Connection setup receives a larger share of the flexible canvas.
- The composer prompt names the current field and hides secret length metadata.
- Header, footer, and palette all surface consistent setup progress.
- Status messages use clearer success, warning, busy, and paused indicators.
- The brightest outline shade remains pink rather than drifting toward white.
- Background texture is quieter and no longer uses slash glyphs that can look
  like broken panel borders.
