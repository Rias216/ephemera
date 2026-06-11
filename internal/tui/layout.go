package tui

const (
	headerRows          = 2
	blockGapRows        = 1
	composerOuterRows   = 3
	footerOuterRows     = 3
	panelBorderRows     = 2
	minViewportRows     = 4 // viewport content rows, excluding its border
	minPaletteOuterRows = 7
	maxPaletteOuterRows = 23
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
		// Header + three one-row separators + composer + footer. Lip Gloss Width
		// and Height include borders, so the remaining value is the complete
		// viewport panel height.
		fixed := headerRows + 3*blockGapRows + composerOuterRows + footerOuterRows
		metrics.viewportOuterHeight = max(panelBorderRows+3, m.height-fixed)
		metrics.viewportInnerHeight = max(3, metrics.viewportOuterHeight-panelBorderRows)
		return metrics
	}

	// Header + four separators + composer + footer. The flexible region is
	// shared by the complete viewport panel and the complete palette panel.
	fixed := headerRows + 4*blockGapRows + composerOuterRows + footerOuterRows
	available := m.height - fixed
	minViewportOuter := minViewportRows + panelBorderRows
	if available < minViewportOuter+minPaletteOuterRows {
		// On short terminals, hide the inspector rather than crush both panels.
		fixedWithoutPalette := headerRows + 3*blockGapRows + composerOuterRows + footerOuterRows
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
