package model

import (
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
)

func composerFromMessage(msg message.Message, lookup func(string) *skills.Skill, customCommands []commands.CustomCommand) composerSnapshot {
	content, text := skills.ParseContentXML(msg.Content().Text)
	collapsed := commands.CollapseExpansionXML(text)
	state := composerSnapshot{text: collapsed}
	if collapsed != text {
		content = commandAttachedSkills(content, collapsed, customCommands)
		slices.Reverse(content)
	}
	for _, skill := range content {
		att := attachments.SkillAttachment{Name: skill.Name, Instructions: skill.Instructions}
		if lookup != nil {
			if active := lookup(skill.Name); active != nil {
				att.Source = active.Source
			}
		}
		state.skills = append(state.skills, att)
	}
	for _, binary := range msg.BinaryContent() {
		state.attachments = append(state.attachments, message.Attachment{
			FilePath: binary.Path,
			FileName: composerAttachmentName(binary.Path),
			MimeType: binary.MIMEType,
			Content:  binary.Data,
		})
	}
	return state
}

func commandAttachedSkills(content []skills.ContentXML, line string, customCommands []commands.CustomCommand) []skills.ContentXML {
	line, ok := strings.CutPrefix(line, "/")
	if !ok {
		return nil
	}
	name, _, _ := strings.Cut(line, " ")
	for _, cmd := range customCommands {
		commandName := cmd.ItemName()
		if cmd.DisplayName != "" {
			commandName = cmd.DisplayName
		}
		if name != commandName {
			continue
		}
		end := len(content)
		for i := len(cmd.Skills) - 1; i >= 0 && end > 0; i-- {
			if content[end-1].Name == cmd.Skills[i] {
				end--
			}
		}
		return content[:end]
	}
	return nil
}

func composerAttachmentName(filePath string) string {
	name := filepath.Base(filePath)
	if strings.Contains(filePath, "://") {
		if uri, err := url.Parse(filePath); err == nil {
			name = path.Base(uri.Path)
			if name == "." || name == "/" {
				name = uri.Hostname()
			}
		}
	}
	if name == "" || name == "." || name == "/" {
		return "attachment"
	}
	return name
}

func (m *UI) captureComposer() composerSnapshot {
	return composerSnapshot{
		text:        m.textarea.Value(),
		attachments: slices.Clone(m.attachments.List()),
		skills:      slices.Clone(m.attachments.SkillList()),
	}
}

func (m *UI) restoreComposer(state composerSnapshot) {
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
