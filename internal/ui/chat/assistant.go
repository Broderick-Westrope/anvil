package chat

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/anim"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
)

// assistantMessageTruncateFormat is the text shown when an assistant message is
// truncated in the collapsed state.
const assistantMessageTruncateFormat = "… (%d lines hidden) [click or space to expand]"

// Default copy for a provider-refusal banner. The agent persists only
// the FinishReasonContentFilter reason; the TUI owns this text and
// fills it in when the finish part carries no message/details (the
// normal live path, and restored sessions). Kept here as the single
// source of truth so the render path and tests cannot drift apart.
const (
	refusalTagLabel = "REFUSED"
	refusalTitle    = "Model refused to continue"
	refusalDetails  = "The provider's safety classifier stopped this response before any usable content was produced. Rephrase the request, start a fresh session, or try a different model."
)

// assistantSection is a per-section render cache for AssistantMessageItem.
// Each section (thinking, content, error) carries its own keys so that
// streaming a section does not invalidate a different — often more
// expensive — section's cached render. srcHash is an FNV-64 of the
// section's source text; extra captures any other state that changes
// the rendered output (e.g. thinkingExpanded, the thinking footer
// inputs). valid disambiguates a real cache hit from the zero value
// when both source text and extras hash to zero. aux carries any
// per-section side data that the caller needs to recover on a hit
// (e.g. the thinking box height for click detection).
type assistantSection struct {
	width   int
	srcHash uint64
	extra   uint64
	out     string
	h       int
	aux     int
	valid   bool
}

// hit reports whether the cache entry matches the requested key.
func (s *assistantSection) hit(width int, srcHash, extra uint64) bool {
	return s.valid && s.width == width && s.srcHash == srcHash && s.extra == extra
}

// store records the rendered output under the given key.
func (s *assistantSection) store(width int, srcHash, extra uint64, out string, aux int) {
	s.width = width
	s.srcHash = srcHash
	s.extra = extra
	s.out = out
	s.h = lipgloss.Height(out)
	s.aux = aux
	s.valid = true
}

// reset drops the cached output.
func (s *assistantSection) reset() {
	*s = assistantSection{}
}

// fnv64 hashes a single string with FNV-64.
func fnv64(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// fnvFields hashes a list of byte fields with length-prefix framing
// so that no concatenation collision can occur between distinct
// field tuples (a NUL inside one field cannot impersonate a
// boundary between two fields). Each field is preceded by its
// length encoded as 8 bytes little-endian.
func fnvFields(fields ...[]byte) uint64 {
	h := fnv.New64a()
	var lenBuf [8]byte
	for _, f := range fields {
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(f)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write(f)
	}
	return h.Sum64()
}

// AssistantMessageItem represents an assistant message in the chat UI.
//
// This item includes thinking, and the content but does not include the tool calls.
type AssistantMessageItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	message *message.Message
	sty     *styles.Styles
	anim    *anim.Anim
	// spinnerLabel is the label last set on anim, so it's only re-rendered
	// when it changes.
	spinnerLabel string

	// thinkingFooterHeight is the rendered height of the "Thought for"
	// line, which is the click target for drilling into the thinking.
	thinkingFooterHeight int

	// Per-section render caches. Splitting these out means content
	// streaming does not invalidate the (often expensive) thinking
	// render, and vice versa.
	thinkingSec assistantSection
	contentSec  assistantSection
	errorSec    assistantSection

	// streamingContent caches a "stable prefix" glamour render of
	// the assistant content body so each streaming flush only
	// re-renders the trailing partial. F8 of
	// docs/notes/2026-05-12-chat-rendering-perf.md. See
	// streaming_markdown.go for the full algorithm.
	streamingContent streamingMarkdown
}

// NewAssistantMessageItem creates a new AssistantMessageItem.
func NewAssistantMessageItem(sty *styles.Styles, message *message.Message) MessageItem {
	v := list.NewVersioned()
	a := &AssistantMessageItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     newFocusableMessageItem(v),
		message:                  message,
		sty:                      sty,
	}

	a.anim = anim.New(anim.Settings{
		ID:          a.ID(),
		Size:        15,
		GradColorA:  sty.WorkingGradFromColor,
		GradColorB:  sty.WorkingGradToColor,
		LabelColor:  sty.WorkingLabelColor,
		CycleColors: true,
	})
	return a
}

// StartAnimation starts the assistant message animation if it should be spinning.
func (a *AssistantMessageItem) StartAnimation() tea.Cmd {
	if !a.isSpinning() {
		return nil
	}
	return a.anim.Start()
}

// Animate progresses the assistant message animation if it should be spinning.
func (a *AssistantMessageItem) Animate(msg anim.StepMsg) tea.Cmd {
	if !a.isSpinning() {
		return nil
	}
	// Bump the F6 list-cache version so the next draw re-renders
	// this item: a spinner tick mutates anim's internal frame
	// counter, which changes the rendered output but is invisible
	// to the per-section content hashes. Without the bump the
	// list cache would serve the previously rendered frame
	// indefinitely and the spinner would appear frozen.
	a.Bump()
	return a.anim.Animate(msg)
}

// ID implements MessageItem.
func (a *AssistantMessageItem) ID() string {
	return a.message.ID
}

func (a *AssistantMessageItem) SourceMessage() message.Message {
	return a.message.Clone()
}

// RawRender implements [MessageItem].
func (a *AssistantMessageItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)

	var spinner string
	if a.isSpinning() {
		spinner = a.renderSpinning()
	}

	content, height := a.renderMessageContent(cappedWidth)
	highlightedContent := a.renderHighlighted(content, cappedWidth, height)
	if spinner != "" {
		if highlightedContent != "" {
			highlightedContent += "\n\n"
		}
		return highlightedContent + spinner
	}

	return highlightedContent
}

// SelectionSource implements [list.SourceSelectable]. It returns the raw
// markdown of the content. Thinking text isn't shown here, so it isn't
// selectable either.
func (a *AssistantMessageItem) SelectionSource() string {
	return strings.TrimSpace(a.message.Content().Text)
}

// Render implements MessageItem.
func (a *AssistantMessageItem) Render(width int) string {
	// XXX: Here, we're manually applying the focused/blurred styles because
	// using lipgloss.Render can degrade performance for long messages due to
	// it's wrapping logic.
	// We already know that the content is wrapped to the correct width in
	// RawRender, so we can just apply the styles directly to each line.
	//
	// The split + per-line prefix loop is O(L); cache the result keyed
	// by (width, focused, sectionsFingerprint) so steady-state Render
	// becomes a pointer return. The sectionsFingerprint folds in the
	// per-section srcHash/extra so that any sub-cache change
	// invalidates this prefix cache without requiring an explicit
	// drop. Bypass the cache while spinning (RawRender's spinner
	// suffix changes every animation frame) or while a highlight
	// range is active (selection drag).
	useCache := !a.isSpinning() && !a.isHighlighted()
	cappedWidth := cappedMessageWidth(width)
	key := a.prefixCacheKey(cappedWidth)
	if useCache {
		if cached, ok := a.getCachedPrefixedRender(width, key); ok {
			return cached
		}
	}
	focused := a.sty.Messages.AssistantFocused.Render()
	blurred := a.sty.Messages.AssistantBlurred.Render()
	rendered := a.RawRender(width)
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		if a.focused {
			lines[i] = focused + line
		} else {
			lines[i] = blurred + line
		}
	}
	out := strings.Join(lines, "\n")
	if useCache {
		a.setCachedPrefixedRender(out, width, key)
	}
	return out
}

// prefixCacheKey builds the F3 prefixed-render cache key. We pack the
// focus bit into bit 0 and a fingerprint of the section caches into
// the upper bits, so any change to a sub-section's source text or
// extras forces the prefix cache to miss without needing an explicit
// drop. cappedWidth is included so a cached prefix never survives a
// section-cache miss caused by a width change. The finish reason is
// folded in too because it controls the composition of
// renderMessageContent (e.g. appending the constant "Canceled"
// string) — that decision lives outside any section's own hash.
func (a *AssistantMessageItem) prefixCacheKey(cappedWidth int) uint64 {
	thinkSrc, thinkExtra := a.thinkingKey()
	contentSrc, contentExtra := a.contentKey()
	errSrc, errExtra := a.errorKey()
	h := fnv.New64a()
	var buf [8]byte
	writeU64 := func(v uint64) {
		for i := range 8 {
			buf[i] = byte(v >> (8 * i))
		}
		_, _ = h.Write(buf[:])
	}
	writeU64(uint64(cappedWidth))
	writeU64(thinkSrc)
	writeU64(thinkExtra)
	writeU64(contentSrc)
	writeU64(contentExtra)
	writeU64(errSrc)
	writeU64(errExtra)
	writeU64(a.compositionKey())
	fingerprint := h.Sum64()
	var focusBit uint64
	if a.focused {
		focusBit = 1
	}
	return (fingerprint &^ 1) | focusBit
}

// compositionKey hashes the inputs to renderMessageContent's structural
// decisions (which sections to include, whether to append the
// constant "Canceled" footer) so that flipping IsFinished or the
// finish reason invalidates the prefix cache even when no section's
// own source text changed.
func (a *AssistantMessageItem) compositionKey() uint64 {
	var finishedFlag byte
	var reason string
	if a.message.IsFinished() {
		finishedFlag = 1
		reason = string(a.message.FinishReason())
	}
	// Length-prefixed framing keeps the finished flag and the reason
	// string from blending into one another.
	return fnvFields([]byte{finishedFlag}, []byte(reason))
}

// renderMessageContent renders the message content including thinking, main
// content, and finish reason. Each section is served from its own cache;
// only the section whose source text or extras changed since the last
// render is recomputed.
func (a *AssistantMessageItem) renderMessageContent(width int) (string, int) {
	var messageParts []string
	thinking := strings.TrimSpace(a.message.ReasoningContent().Thinking)
	content := strings.TrimSpace(a.message.Content().Text)

	var footer string
	if thinking != "" {
		footer = a.cachedThinkingFooter(width)
	}
	if footer != "" {
		messageParts = append(messageParts, footer)
	}

	if content != "" {
		if footer != "" {
			messageParts = append(messageParts, "")
		}
		messageParts = append(messageParts, a.cachedContent(width))
	}

	if a.message.IsFinished() {
		switch {
		case a.message.FinishReason() == message.FinishReasonCanceled:
			messageParts = append(messageParts, a.sty.Messages.AssistantCanceled.Render("Canceled"))
		case a.message.IsErrorLike():
			messageParts = append(messageParts, a.cachedError(width))
		}
	}

	out := strings.Join(messageParts, "\n")
	return out, lipgloss.Height(out)
}

// thinkingKey returns the (srcHash, extra) cache key components for the
// thinking section. The section only shows the "Thought for" footer, so
// the key covers whether thinking is done and its duration, not the
// thinking text.
func (a *AssistantMessageItem) thinkingKey() (uint64, uint64) {
	var done byte
	var durationStr string
	if a.thinkingFinished() {
		done = 1
	}
	if a.message.ReasoningContent().FinishedAt != 0 {
		durationStr = a.message.ThinkingDuration().String()
	}
	return 0, fnvFields([]byte{done}, []byte(durationStr))
}

// contentKey returns the (srcHash, extra) cache key components for the
// main content section. The finished bit is folded into extra so the
// section re-renders once when the stream completes — that render
// goes through RenderFinal, which replaces any force-advanced (seamed)
// streaming output with a clean monolithic render.
func (a *AssistantMessageItem) contentKey() (uint64, uint64) {
	var fin uint64
	if a.message.IsFinished() {
		fin = 1
	}
	return fnv64(a.message.Content().Text), fin
}

// errorKey returns the (srcHash, extra) cache key components for the
// error / refusal section. Returns (0, 0) when no error-like finish
// is present so the cache stays a no-op for normal messages.
func (a *AssistantMessageItem) errorKey() (uint64, uint64) {
	if !a.message.IsFinished() || !a.message.IsErrorLike() {
		return 0, 0
	}
	finishPart := a.message.FinishPart()
	if finishPart == nil {
		return 0, 0
	}
	// Length-prefixed framing prevents Message+Details collisions
	// between distinct (Message, Details) tuples that would
	// otherwise concatenate to the same byte sequence. Fold the
	// reason in so ERROR vs REFUSED banners never share a cache slot.
	return fnvFields([]byte(finishPart.Reason), []byte(finishPart.Message), []byte(finishPart.Details)), 0
}

// cachedThinkingFooter returns the rendered thinking footer, computing
// and caching it on miss. The footer height (the drill-in click target)
// is preserved across hits via assistantSection.aux.
func (a *AssistantMessageItem) cachedThinkingFooter(width int) string {
	srcHash, extra := a.thinkingKey()
	if a.thinkingSec.hit(width, srcHash, extra) {
		a.thinkingFooterHeight = a.thinkingSec.aux
		return a.thinkingSec.out
	}
	out := a.renderThinkingFooter()
	a.thinkingFooterHeight = 0
	if out != "" {
		a.thinkingFooterHeight = lipgloss.Height(out)
	}
	a.thinkingSec.store(width, srcHash, extra, out, a.thinkingFooterHeight)
	return out
}

// cachedContent returns the rendered content section.
func (a *AssistantMessageItem) cachedContent(width int) string {
	srcHash, extra := a.contentKey()
	if a.contentSec.hit(width, srcHash, extra) {
		return a.contentSec.out
	}
	out := a.renderMarkdown(a.message.Content().Text, width)
	a.contentSec.store(width, srcHash, extra, out, 0)
	return out
}

// cachedError returns the rendered error section.
func (a *AssistantMessageItem) cachedError(width int) string {
	srcHash, extra := a.errorKey()
	if a.errorSec.hit(width, srcHash, extra) {
		return a.errorSec.out
	}
	out := a.renderError(width)
	a.errorSec.store(width, srcHash, extra, out, 0)
	return out
}

// renderThinkingFooter renders the line that stands in for the thinking
// text once thinking is done: "Thought for Xs", or "Thought" when it took
// under a second. While the model is still thinking it renders nothing
// and the spinner shows instead.
func (a *AssistantMessageItem) renderThinkingFooter() string {
	if !a.thinkingFinished() {
		return ""
	}
	// Without an end time the duration would run to now, so it's only
	// shown once the model marked the thinking done.
	duration := a.message.ThinkingDuration()
	if a.message.ReasoningContent().FinishedAt == 0 || duration.String() == "0s" {
		return a.sty.Messages.ThinkingFooterTitle.Render("Thought")
	}
	return a.sty.Messages.ThinkingFooterTitle.Render("Thought for ") +
		a.sty.Messages.ThinkingFooterDuration.Render(duration.String())
}

// thinkingFinished reports whether the thinking is complete: the model
// marked it done, or the turn ended. The footer, the spinner label and
// the thinking drill-in all use this one rule.
func (a *AssistantMessageItem) thinkingFinished() bool {
	return a.message.ReasoningContent().FinishedAt != 0 || a.message.IsFinished()
}

// renderMarkdown renders content as markdown. F8 routes the call
// through streamingContent, which caches the glamour render of a
// "stable prefix" so each streaming flush only re-renders the
// trailing partial. The streaming cache invalidates itself on
// width change and on any content that is not a prefix-extension
// of the previously rendered content (e.g. user retried the
// turn), and falls back to a full render whenever boundary
// detection has the slightest doubt — see
// findSafeMarkdownBoundary.
func (a *AssistantMessageItem) renderMarkdown(content string, width int) string {
	renderer := common.MarkdownRenderer(a.sty, width)
	if a.message.IsFinished() {
		// Stream complete: take the definitive render so any
		// force-advanced (seamed) streaming output is replaced.
		return a.streamingContent.RenderFinal(content, width, renderer)
	}
	return a.streamingContent.Render(content, width, renderer)
}

func (a *AssistantMessageItem) renderSpinning() string {
	var label string
	if a.message.MessageType == message.MessageTypeCompaction {
		label = "Summarizing"
	} else if strings.TrimSpace(a.message.ReasoningContent().Thinking) != "" && !a.thinkingFinished() {
		label = "Thinking"
	}
	if label != a.spinnerLabel {
		a.spinnerLabel = label
		a.anim.SetLabel(label)
	}
	return a.anim.Render()
}

// renderError renders an error or provider-refusal banner.
func (a *AssistantMessageItem) renderError(width int) string {
	finishPart := a.message.FinishPart()
	tagLabel := "ERROR"
	titleText := finishPart.Message
	detailsText := finishPart.Details
	if finishPart.Reason == message.FinishReasonContentFilter {
		tagLabel = refusalTagLabel
		titleText = cmp.Or(titleText, refusalTitle)
		detailsText = cmp.Or(detailsText, refusalDetails)
	}
	errTag := a.sty.Messages.ErrorTag.Render(tagLabel)
	truncated := ansi.Truncate(titleText, width-2-lipgloss.Width(errTag), "...")
	title := fmt.Sprintf("%s %s", errTag, a.sty.Messages.ErrorTitle.Render(truncated))
	if detailsText == "" {
		return title
	}
	details := a.sty.Messages.ErrorDetails.Width(width - 2).Render(detailsText)
	return fmt.Sprintf("%s\n\n%s", title, details)
}

// isSpinning returns true if the assistant message is still generating.
func (a *AssistantMessageItem) isSpinning() bool {
	isThinking := a.message.IsThinking()
	isFinished := a.message.IsFinished()
	hasContent := strings.TrimSpace(a.message.Content().Text) != ""
	hasToolCalls := len(a.message.ToolCalls()) > 0
	return (isThinking || !isFinished) && !hasContent && !hasToolCalls
}

// SetMessage is used to update the underlying message. Only the
// sub-section caches whose source text or extras changed are
// invalidated; the others survive and serve cache hits on the next
// RawRender.
func (a *AssistantMessageItem) SetMessage(msg *message.Message) tea.Cmd {
	wasSpinning := a.isSpinning()
	a.message = msg
	// Bump the F6 version even if the underlying *message.Message
	// pointer is identical: callers may have mutated the message in
	// place (delta append) and we cannot tell from here. The
	// per-section caches dedupe identical content via FNV-64 hashes,
	// so a redundant bump only costs one list-cache repopulation.
	a.Bump()
	// The prefix cache is keyed by a fingerprint that includes every
	// section's source hash, so an unchanged section keeps its prefix
	// cache valid while a changed section forces a miss naturally.
	// Section caches themselves are content-keyed, so they do not
	// need an explicit drop here either.
	if !wasSpinning && a.isSpinning() {
		return a.StartAnimation()
	}
	return nil
}

// Finished implements list.Item. The assistant message is freezable
// once the message reports IsFinished() and is no longer spinning
// (no animation tick remains pending). Streaming tail animation is
// caught by isSpinning, so freezing only kicks in once the turn is
// fully terminal. The list cache invalidates the entry on the next
// version bump if anything (focus, highlight, expansion) changes.
func (a *AssistantMessageItem) Finished() bool {
	return a.message.IsFinished() && !a.isSpinning()
}

// clearCache drops every cached render for this item, including the
// per-section caches. Shadows the embedded cachedMessageItem.clearCache
// so ClearItemCaches (style change) wipes the section caches too.
// F8: also drop the streaming-markdown stable-prefix cache because
// the cached glamour render embeds the OLD style's ANSI sequences
// and is no longer visually consistent with the new style.
func (a *AssistantMessageItem) clearCache() {
	a.cachedMessageItem.clearCache()
	a.thinkingSec.reset()
	a.contentSec.reset()
	a.errorSec.reset()
	a.streamingContent.Reset()
}

// HandleMouseClick implements MouseClickable. It reports whether a left
// click landed on the "Thought for" line, which the caller turns into a
// thinking drill-in. The rest of the message isn't clickable.
func (a *AssistantMessageItem) HandleMouseClick(btn ansi.MouseButton, x, y int) bool {
	if btn != ansi.MouseLeft {
		return false
	}
	return y < a.thinkingFooterHeight
}

// ThinkingDrillIn returns a command that opens the thinking text in a
// drill-in view, or nil when the message has no thinking.
func (a *AssistantMessageItem) ThinkingDrillIn() tea.Cmd {
	if strings.TrimSpace(a.message.ReasoningContent().Thinking) == "" {
		return nil
	}
	return func() tea.Msg {
		return util.ThinkingDrillInMsg{Source: a}
	}
}

// HandleKeyEvent implements KeyEventHandler.
func (a *AssistantMessageItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "c", "y":
		text := a.message.Content().Text
		return true, common.CopyToClipboard(text, "Message copied to clipboard")
	case "right", "l":
		// Drill into the thinking, matching the tool and agent →/l pattern.
		if cmd := a.ThinkingDrillIn(); cmd != nil {
			return true, cmd
		}
	}
	return false, nil
}
