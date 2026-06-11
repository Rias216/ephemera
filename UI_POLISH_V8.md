# UI polish v8 — full-canvas command inspector

This revision translates the approved visual mockup into the Bubble Tea v2 UI.

## Layout

- Every terminal row is allocated deliberately.
- With no command palette, the transcript receives the full flexible region.
- With the palette open, the flexible region is divided between the transcript
  and a responsive command inspector.
- The composer and footer retain fixed geometry, so filtering commands does not
  move the rest of the interface.

## Command inspector

The selected command now exposes:

- category, aliases, version, shell type, and permission scope;
- complete usage syntax;
- required and optional arguments;
- practical examples and keyboard guidance.

Wide terminals use three columns, medium terminals use two, and compact
terminals collapse to a concise stacked view.

## Texture and color

- Sparse grain, star-dust, and diagonal mesh marks give empty panel space depth.
- Texture is deterministic and static, so it is written once by the cell-diff
  renderer and does not add animation flicker.
- The rose ramp now uses nine closely spaced shades with eased interpolation.
- All panel outlines share a coordinated global color current; small offsets
  keep them organic without making adjacent panels clash.

## Motion

- 60 FPS renderer cap remains enabled.
- Animation position remains elapsed-time based.
- The ambient drift is slower and the knife glimmer is slightly calmer.
- The terminal cursor remains native and independent from UI animation.
