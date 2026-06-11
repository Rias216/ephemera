package tui

const (
	headerRows          = 2
	composerOuterRows   = 3
	footerOuterRows     = 3
	panelBorderRows     = 2
	minViewportRows     = 4 // viewport content rows, excluding its border
	minPaletteOuterRows = 7
	maxPaletteOuterRows = 25
)

type layoutMetrics struct {
	viewportInnerHeight int
	viewportOuterHeight int
	paletteOuterHeight  int
}

func (m Model) layoutMetrics() layoutMetrics {
	var metrics layoutMetrics
	if m.height <= 0 {
		return metrics
	}

	if !m.suggestionPaletteActive() {
		// Panels sit directly beside one another. Their borders already provide
		// enough visual separation, so decorative spacer rows would only consume
		// useful transcript space.
		fixed := headerRows + composerOuterRows + footerOuterRows
		metrics.viewportOuterHeight = max(panelBorderRows+3, m.height-fixed)
		metrics.viewportInnerHeight = max(3, metrics.viewportOuterHeight-panelBorderRows)
		return metrics
	}

	// The flexible region is shared by the complete viewport and palette panels.
	// No rows are reserved for ambient texture between them.
	fixed := headerRows + composerOuterRows + footerOuterRows
	available := m.height - fixed
	minViewportOuter := minViewportRows + panelBorderRows
	if available < minViewportOuter+minPaletteOuterRows {
		// On short terminals, hide the inspector rather than crush both panels.
		fixedWithoutPalette := headerRows + composerOuterRows + footerOuterRows
		metrics.viewportOuterHeight = max(panelBorderRows+3, m.height-fixedWithoutPalette)
		metrics.viewportInnerHeight = max(3, metrics.viewportOuterHeight-panelBorderRows)
		return metrics
	}

	// Connection setup benefits from a taller chooser: it needs enough rows to
	// show several providers at once plus a compact field inspector. General
	// command completion keeps slightly more room for the transcript.
	share := 0.58
	if m.connect != nil {
		share = 0.66
	}
	target := int(float64(available)*share + 0.5)
	maxAllowed := available - minViewportOuter
	metrics.paletteOuterHeight = clampInt(target, minPaletteOuterRows, min(maxPaletteOuterRows, maxAllowed))
	if metrics.paletteOuterHeight < minPaletteOuterRows {
		metrics.paletteOuterHeight = max(0, maxAllowed)
	}
	metrics.viewportOuterHeight = max(panelBorderRows+3, available-metrics.paletteOuterHeight)
	metrics.viewportInnerHeight = max(3, metrics.viewportOuterHeight-panelBorderRows)
	return metrics
}

func clampInt(value, low, high int) int {
	if high < low {
		return high
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
