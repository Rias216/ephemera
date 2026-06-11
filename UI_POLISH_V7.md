# Ephemera UI polish v7

This revision keeps the Bubble Tea v2 renderer and fixed layout introduced in v6.2, then adds a layered visual system without returning to full-screen clears.

## Motion

- 60 FPS elapsed-time animation, independent of dropped frames.
- Organic glimmer velocity with a small sinusoidal drift instead of mechanical cell stepping.
- Two low-frequency pink waves animate the resting outline.
- A longer rose trail and tighter pale-pink knife edge move above the ambient gradient.
- Small, isolated pulses animate the logo, activity state, context meter, prompt glyph, and selected command.

## Layout and hierarchy

- Raised composer surface with animated prompt glyph.
- Stable mode/character metadata inside the composer.
- Command palette header, result counter, numbered entries, and animated selection marker.
- Fixed command palette height remains intact while filtering.
- Refined route chips, activity badge, context rail, scroll percentage, and contextual key hints.
- Conversation role dividers and a more useful empty-session launch screen.

## Stability

- No animation changes component geometry.
- No opacity or background animation across the full terminal.
- The real terminal cursor remains independent from content animation.
- Focus loss pauses animation and network spinner updates.
