package tui

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ephemera-ai/ephemera/internal/history"
)

type transcriptSectionKind int

const (
	sectionUserMessage transcriptSectionKind = iota
	sectionAssistantMessage
	sectionAgentEvent
	sectionLiveAgent
	sectionNotice
)

type transcriptSection struct {
	kind       transcriptSectionKind
	message    *history.Message
	event      *history.Event
	eventIndex int
	createdAt  time.Time
}

// buildTranscriptSections merges persisted messages and agent events by time so
// tool calls, results, plans, and the final response appear where they happened
// instead of being detached into a second transcript at the bottom.
func (m Model) buildTranscriptSections() []transcriptSection {
	sections := make([]transcriptSection, 0, len(m.session.Messages)+len(m.session.Events)+2)
	for index := range m.session.Messages {
		message := m.session.Messages[index]
		kind := sectionUserMessage
		if message.Role == "assistant" {
			kind = sectionAssistantMessage
		} else if message.Role != "user" {
			continue
		}
		copy := message
		sections = append(sections, transcriptSection{kind: kind, message: &copy, createdAt: message.CreatedAt})
	}
	visible := m.visibleAgentEvents()
	for index := range visible {
		event := visible[index]
		copy := event
		sections = append(sections, transcriptSection{kind: sectionAgentEvent, event: &copy, eventIndex: index, createdAt: event.CreatedAt})
	}
	if m.notice != "" {
		sections = append(sections, transcriptSection{kind: sectionNotice, createdAt: time.Now()})
	}
	if m.liveAgent.Active {
		sections = append(sections, transcriptSection{kind: sectionLiveAgent, createdAt: time.Now().Add(time.Nanosecond)})
	}
	sort.SliceStable(sections, func(i, j int) bool {
		left, right := sections[i].createdAt, sections[j].createdAt
		if left.IsZero() && right.IsZero() {
			return i < j
		}
		if left.IsZero() {
			return true
		}
		if right.IsZero() {
			return false
		}
		return left.Before(right)
	})
	return sections
}

func (m *Model) renderTranscriptSection(section transcriptSection, renderer cliRenderer, selected int) []string {
	if section.kind == sectionLiveAgent {
		return m.renderTranscriptSectionUncached(section, renderer, selected)
	}
	key := m.transcriptSectionCacheKey(section, renderer.width, selected)
	if m.sectionRenderCache == nil {
		m.sectionRenderCache = make(map[string][]string)
	}
	if cached, ok := m.sectionRenderCache[key]; ok {
		return append([]string(nil), cached...)
	}
	rows := m.renderTranscriptSectionUncached(section, renderer, selected)
	if len(m.sectionRenderCache) >= 1024 {
		m.sectionRenderCache = make(map[string][]string)
	}
	m.sectionRenderCache[key] = append([]string(nil), rows...)
	return rows
}

func (m Model) renderTranscriptSectionUncached(section transcriptSection, renderer cliRenderer, selected int) []string {
	switch section.kind {
	case sectionUserMessage, sectionAssistantMessage:
		if section.message == nil {
			return nil
		}
		label := "you"
		style := m.styles.UserLabel
		if section.kind == sectionAssistantMessage {
			label, style = "ephemera", m.styles.AssistantLabel
		}
		return append([]string{m.transcriptLine(style, label)}, strings.Split(renderer.Render(section.message.Content), "\n")...)
	case sectionAgentEvent:
		if section.event == nil {
			return nil
		}
		return m.renderAgentEvent(*section.event, renderer, section.eventIndex == selected, m.eventExpanded(*section.event, section.eventIndex), section.eventIndex)
	case sectionLiveAgent:
		return m.renderLiveAgent(renderer)
	case sectionNotice:
		return append([]string{m.transcriptLine(m.styles.NoticeLabel, "signal")}, strings.Split(renderer.Render(m.notice), "\n")...)
	default:
		return nil
	}
}

func (m Model) transcriptSectionCacheKey(section transcriptSection, width, selected int) string {
	payload := struct {
		Kind        transcriptSectionKind
		Message     *history.Message
		Event       *history.Event
		EventIndex  int
		Notice      string
		Width       int
		Selected    bool
		Expanded    bool
		Compact     bool
		ToolDetails bool
		Theme       string
	}{
		Kind:        section.kind,
		Message:     section.message,
		Event:       section.event,
		EventIndex:  section.eventIndex,
		Width:       width,
		Selected:    section.kind == sectionAgentEvent && section.eventIndex == selected,
		Compact:     m.compactView,
		ToolDetails: m.cfg.ToolDetails,
		Theme:       m.cfg.Theme,
	}
	if section.kind == sectionNotice {
		payload.Notice = m.notice
	}
	if section.event != nil {
		payload.Expanded = m.eventExpanded(*section.event, section.eventIndex)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("uncacheable:%d:%d:%d", section.kind, section.eventIndex, time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func sectionNeedsGap(previous, current transcriptSectionKind) bool {
	if previous == current && current == sectionAgentEvent {
		return false
	}
	return true
}

func (m Model) renderInlineAgentHeader() string {
	label := m.agentTimelineLabel()
	if m.compactView {
		label += " · compact"
	}
	return m.transcriptLine(m.styles.NoticeLabel, label)
}
