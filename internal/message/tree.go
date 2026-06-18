package message

// FilterBranchPathForContext processes a root-to-leaf branch path into
// the message slice that should be sent to the LLM. It handles
// compaction boundaries, branch summaries, and filters out metadata
// entries.
func FilterBranchPathForContext(path []Message) []Message {
	// Find the most recent compaction message (nearest to leaf = last
	// occurrence in root→leaf order).
	compactionIdx := -1
	for i := len(path) - 1; i >= 0; i-- {
		if path[i].MessageType == MessageTypeCompaction {
			compactionIdx = i
			break
		}
	}

	var result []Message

	if compactionIdx >= 0 {
		compaction := path[compactionIdx]
		// Emit the compaction summary as a synthetic user message.
		var summaryText string
		for _, part := range compaction.Parts {
			if cc, ok := part.(CompactionContent); ok {
				summaryText = cc.Summary
				break
			}
		}
		if summaryText != "" {
			result = append(result, Message{
				Role: User,
				Parts: []ContentPart{
					TextContent{
						Text: "<summary>The conversation history before this point was compacted into the following summary:\n\n" + summaryText + "</summary>",
					},
				},
			})
		}

		// Emit "kept" messages: find firstKeptEntryId and emit from
		// there, plus all messages after the compaction entry.
		var firstKeptEntryID string
		for _, part := range compaction.Parts {
			if cc, ok := part.(CompactionContent); ok {
				firstKeptEntryID = cc.FirstKeptEntryID
				break
			}
		}

		// Messages between firstKeptEntryId and compaction entry
		// (inclusive of firstKeptEntryId, exclusive of compaction).
		if firstKeptEntryID != "" {
			inKeptRange := false
			for i, msg := range path {
				if msg.ID == firstKeptEntryID {
					inKeptRange = true
				}
				if i == compactionIdx {
					break
				}
				if inKeptRange {
					if filtered := FilterMetadataMessage(msg); filtered != nil {
						result = append(result, *filtered)
					}
				}
			}
		}

		// All messages after the compaction entry.
		for i := compactionIdx + 1; i < len(path); i++ {
			if filtered := FilterMetadataMessage(path[i]); filtered != nil {
				result = append(result, *filtered)
			}
		}
	} else {
		// No compaction — emit all messages, filtering metadata types.
		for _, msg := range path {
			if filtered := FilterMetadataMessage(msg); filtered != nil {
				result = append(result, *filtered)
			}
		}
	}

	return result
}

// FilterMetadataMessage returns nil for metadata-only message types
// that should not appear in LLM context. For branch_summary messages,
// it converts them to a synthetic user message with summary tags. For
// regular messages, it returns a pointer to the original.
func FilterMetadataMessage(msg Message) *Message {
	switch msg.MessageType {
	case MessageTypeLabel, MessageTypeModelChange, MessageTypeThinkingLevelChange, MessageTypeMCPToggle:
		return nil
	case MessageTypeCompaction:
		// Older compactions on the path are skipped — only the most
		// recent one is processed by the caller.
		return nil
	case MessageTypeBranchSummary:
		var summaryText string
		for _, part := range msg.Parts {
			if bs, ok := part.(BranchSummaryContent); ok {
				summaryText = bs.Summary
				break
			}
		}
		if summaryText == "" {
			return nil
		}
		return &Message{
			Role: User,
			Parts: []ContentPart{
				TextContent{
					Text: "<summary>The following is a summary of a branch that this conversation came back from:\n\n" + summaryText + "</summary>",
				},
			},
		}
	default:
		return &msg
	}
}

// ComputeFirstKeptEntryID walks messages from newest to oldest,
// accumulating estimated token counts. Once the accumulated tokens
// exceed the keepTokens threshold, it continues backward to find a
// semantically complete boundary (a user message or the first message
// after a complete assistant-to-tool exchange). Returns the ID of that
// boundary message.
func ComputeFirstKeptEntryID(msgs []Message, keepTokens int) string {
	if len(msgs) == 0 {
		return ""
	}

	accumulated := 0
	boundaryIdx := 0 // Default to the start of the conversation.
	for i := len(msgs) - 1; i >= 0; i-- {
		accumulated += EstimateMessageTokens(msgs[i])
		if accumulated >= keepTokens {
			// Continue backward to find a valid cut point.
			for j := i; j >= 0; j-- {
				if msgs[j].Role == User {
					boundaryIdx = j
					break
				}
			}
			break
		}
	}
	return msgs[boundaryIdx].ID
}

// EstimateMessageTokens provides a rough token estimate for a message.
// It uses ceil(chars/4) as a simple heuristic.
func EstimateMessageTokens(msg Message) int {
	total := 0
	for _, part := range msg.Parts {
		switch p := part.(type) {
		case TextContent:
			total += (len(p.Text) + 3) / 4
		case ReasoningContent:
			total += (len(p.Thinking) + 3) / 4
		case ToolCall:
			total += (len(p.Input) + 3) / 4
		case ToolResult:
			total += (len(p.Content) + 3) / 4
		}
	}
	if total == 0 {
		total = 1
	}
	return total
}
