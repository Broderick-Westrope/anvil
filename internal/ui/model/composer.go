package model

import (
	"path/filepath"
	"slices"

	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
)

func composerFromMessage(msg message.Message, lookup func(string) *skills.Skill) composerSnapshot {
	content, text := skills.ParseContentXML(msg.Content().Text)
	collapsed := commands.CollapseExpansionXML(text)
	state := composerSnapshot{text: collapsed}
	if collapsed == text {
		for _, skill := range content {
			att := attachments.SkillAttachment{Name: skill.Name, Instructions: skill.Instructions}
			if lookup != nil {
				if active := lookup(skill.Name); active != nil {
					att.Source = active.Source
				}
			}
			state.skills = append(state.skills, att)
		}
	}
	for _, binary := range msg.BinaryContent() {
		state.attachments = append(state.attachments, message.Attachment{
			FilePath: binary.Path,
			FileName: filepath.Base(binary.Path),
			MimeType: binary.MIMEType,
			Content:  slices.Clone(binary.Data),
		})
	}
	return state
}

func (c composerSnapshot) clone() composerSnapshot {
	c.attachments = slices.Clone(c.attachments)
	for i := range c.attachments {
		c.attachments[i].Content = slices.Clone(c.attachments[i].Content)
	}
	c.skills = slices.Clone(c.skills)
	return c
}

func (m *UI) captureComposer() composerSnapshot {
	return (composerSnapshot{
		text: m.textarea.Value(), attachments: m.attachments.List(), skills: m.attachments.SkillList(),
	}).clone()
}

func (m *UI) restoreComposer(state composerSnapshot) {
	state = state.clone()
	m.textarea.SetValue(state.text)
	m.textarea.MoveToEnd()
	m.attachments.Reset()
	for _, att := range state.attachments {
		m.attachments.Update(att)
	}
	for _, skill := range state.skills {
		m.attachments.Update(skill)
	}
	m.updateLayoutAndSize()
}
