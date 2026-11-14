package agent

import (
	"context"
	"errors"
	"fmt"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/secrets"
)

// secretMaskModel masks credentials in every request to the wrapped model,
// so no caller has to opt in. It masks a copy of the prompt: text, tool
// results and the tool calls in history. Reasoning, files and
// provider-executed parts pass through untouched, since providers reject
// edited signed reasoning and rebuild server tool calls from metadata.
type secretMaskModel struct {
	fantasy.LanguageModel
	detector *secrets.Detector
}

// newSecretMaskModel wraps m. A nil detector (masking off) returns m
// unchanged.
func newSecretMaskModel(m fantasy.LanguageModel, detector *secrets.Detector) fantasy.LanguageModel {
	if m == nil || detector == nil {
		return m
	}
	return secretMaskModel{LanguageModel: m, detector: detector}
}

// Generate implements fantasy.LanguageModel.
func (m secretMaskModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	prompt, err := m.mask(ctx, call.Prompt)
	if err != nil {
		return nil, err
	}
	call.Prompt = prompt
	return m.LanguageModel.Generate(ctx, call)
}

// Stream implements fantasy.LanguageModel.
func (m secretMaskModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	prompt, err := m.mask(ctx, call.Prompt)
	if err != nil {
		return nil, err
	}
	call.Prompt = prompt
	return m.LanguageModel.Stream(ctx, call)
}

// GenerateObject implements fantasy.LanguageModel.
func (m secretMaskModel) GenerateObject(ctx context.Context, call fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	prompt, err := m.mask(ctx, call.Prompt)
	if err != nil {
		return nil, err
	}
	call.Prompt = prompt
	return m.LanguageModel.GenerateObject(ctx, call)
}

// StreamObject implements fantasy.LanguageModel.
func (m secretMaskModel) StreamObject(ctx context.Context, call fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	prompt, err := m.mask(ctx, call.Prompt)
	if err != nil {
		return nil, err
	}
	call.Prompt = prompt
	return m.LanguageModel.StreamObject(ctx, call)
}

// mask returns a masked copy of prompt. A panic while masking fails the
// request, so unmasked text is never sent.
func (m secretMaskModel) mask(ctx context.Context, prompt fantasy.Prompt) (masked fantasy.Prompt, err error) {
	defer func() {
		if r := recover(); r != nil {
			masked, err = nil, fmt.Errorf("mask secrets in request: %v", r)
		}
	}()
	masked = make(fantasy.Prompt, len(prompt))
	for i, msg := range prompt {
		content := make([]fantasy.MessagePart, len(msg.Content))
		for j, part := range msg.Content {
			// A first scan of a long history can take seconds, so a
			// cancelled request stops between parts.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			maskedPart, partErr := m.maskPart(part)
			if partErr != nil {
				return nil, fmt.Errorf("mask secrets in request: %w", partErr)
			}
			content[j] = maskedPart
		}
		msg.Content = content
		masked[i] = msg
	}
	return masked, nil
}

// maskPart masks one part. A part it doesn't know how to mask is an error,
// so a new part shape never goes out unmasked.
func (m secretMaskModel) maskPart(part fantasy.MessagePart) (fantasy.MessagePart, error) {
	switch part.GetType() {
	case fantasy.ContentTypeReasoning, fantasy.ContentTypeFile:
		return part, nil
	case fantasy.ContentTypeText:
		p, ok := fantasy.AsMessagePart[fantasy.TextPart](part)
		if !ok {
			return nil, fmt.Errorf("unexpected text part %T", part)
		}
		p.Text, _ = m.detector.Mask(p.Text)
		return p, nil
	case fantasy.ContentTypeToolCall:
		p, ok := fantasy.AsMessagePart[fantasy.ToolCallPart](part)
		if !ok {
			return nil, fmt.Errorf("unexpected tool call part %T", part)
		}
		if p.ProviderExecuted {
			return part, nil
		}
		p.Input, _ = m.detector.MaskJSON(p.Input)
		return p, nil
	case fantasy.ContentTypeToolResult:
		p, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
		if !ok {
			return nil, fmt.Errorf("unexpected tool result part %T", part)
		}
		if p.ProviderExecuted {
			return part, nil
		}
		output, err := m.maskOutput(p.Output)
		if err != nil {
			return nil, err
		}
		p.Output = output
		return p, nil
	}
	return nil, fmt.Errorf("unexpected part type %q", part.GetType())
}

// maskResultText masks a tool's text output. JSON output (from MCP tools,
// curl, gh api) is masked as JSON, so rules see the newlines its strings
// escape.
func (m secretMaskModel) maskResultText(text string) string {
	if secrets.IsJSON(text) {
		masked, _ := m.detector.MaskJSON(text)
		return masked
	}
	masked, _ := m.detector.Mask(text)
	return masked
}

func (m secretMaskModel) maskOutput(output fantasy.ToolResultOutputContent) (fantasy.ToolResultOutputContent, error) {
	if output == nil {
		return output, nil
	}
	unexpected := fmt.Errorf("unexpected tool result output %T", output)
	switch output.GetType() {
	case fantasy.ToolResultContentTypeText:
		o, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](output)
		if !ok {
			return nil, unexpected
		}
		o.Text = m.maskResultText(o.Text)
		return o, nil
	case fantasy.ToolResultContentTypeError:
		o, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](output)
		if !ok {
			return nil, unexpected
		}
		if o.Error != nil {
			text, _ := m.detector.Mask(o.Error.Error())
			o.Error = errors.New(text)
		}
		return o, nil
	case fantasy.ToolResultContentTypeMedia:
		o, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](output)
		if !ok {
			return nil, unexpected
		}
		o.Text, _ = m.detector.Mask(o.Text)
		return o, nil
	}
	return nil, unexpected
}
