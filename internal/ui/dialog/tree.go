package dialog

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	uv "github.com/charmbracelet/ultraviolet"
)

// Navigation dialog sizing constants shared by the tree and branch dialogs.
const (
	navDialogMaxWidth = 100
	navDialogHeight   = 30
)

// treeNode represents a single node in the in-memory message tree.
type treeNode struct {
	msg            message.Message
	children       []*treeNode
	depth          int
	isOnActivePath bool
	isBranchHead   bool // direct child of a node with 2+ children
}

// Tree is a dialog that displays an ASCII tree of all session messages.
type Tree struct {
	com                *common.Common
	input              textinput.Model
	list               *list.FilterableList
	roots              []*treeNode
	nodeMap            map[string]*treeNode
	expanded           map[string]bool
	leafMessageID      string
	needsInitialScroll bool

	keyMap struct {
		Select   key.Binding
		Next     key.Binding
		Previous key.Binding
		Expand   key.Binding
		Collapse key.Binding
		Close    key.Binding
	}
}

var _ Dialog = (*Tree)(nil)

// hiddenMessageTypes are message types filtered out of the tree display.
var hiddenMessageTypes = map[message.MessageType]bool{
	message.MessageTypeModelChange:         true,
	message.MessageTypeThinkingLevelChange: true,
	message.MessageTypeCompaction:          true,
	message.MessageTypeBranchSummary:       true,
}

// NewTree creates a new Tree dialog for the given session.
func NewTree(com *common.Common, sessionID string, leafMessageID string) (*Tree, error) {
	msgs, err := com.Workspace.GetAllSessionMessages(context.TODO(), sessionID)
	if err != nil {
		return nil, err
	}

	t := &Tree{
		com:           com,
		nodeMap:       make(map[string]*treeNode),
		expanded:      make(map[string]bool),
		leafMessageID: leafMessageID,
	}

	// Index all messages by ID so we can walk parent chains through
	// filtered-out nodes.
	allByID := make(map[string]message.Message, len(msgs))
	for _, msg := range msgs {
		allByID[msg.ID] = msg
	}

	// Build the in-memory tree, filtering out hidden message types and
	// non-conversation roles (tool messages add noise without value).
	for _, msg := range msgs {
		if hiddenMessageTypes[msg.MessageType] {
			continue
		}
		if msg.Role != message.User && msg.Role != message.Assistant {
			continue
		}
		// Skip assistant messages with no text content (tool-call-only
		// or error-only messages add noise without value).
		if msg.Role == message.Assistant && messageTextContent(msg) == "" {
			continue
		}
		t.nodeMap[msg.ID] = &treeNode{msg: msg}
	}

	// Link children to parents and collect roots. When a node's direct
	// parent was filtered out, walk up the original message chain to
	// find the nearest visible ancestor.
	for _, node := range t.nodeMap {
		parentID := node.msg.ParentMessageID
		// Walk through filtered-out ancestors to find a visible parent.
		for parentID != "" {
			if _, ok := t.nodeMap[parentID]; ok {
				break
			}
			if orig, ok := allByID[parentID]; ok {
				parentID = orig.ParentMessageID
			} else {
				parentID = ""
			}
		}
		if parentID == "" {
			t.roots = append(t.roots, node)
		} else {
			t.nodeMap[parentID].children = append(t.nodeMap[parentID].children, node)
		}
	}

	// Sort roots and children by creation time so the tree order is
	// deterministic (map iteration order in Go is random).
	sortNodes(t.roots)
	for _, node := range t.nodeMap {
		sortNodes(node.children)
	}

	// Collapse assistant→assistant single-child chains so each assistant
	// turn is shown as one entry (the last text response). Walk the tree
	// and whenever an assistant node has exactly one child that is also
	// assistant, remove the parent and re-link the child in its place.
	var collapseAssistantChains func(nodes []*treeNode) []*treeNode
	collapseAssistantChains = func(nodes []*treeNode) []*treeNode {
		var result []*treeNode
		for _, node := range nodes {
			// Walk down assistant→assistant single-child chains,
			// keeping the last node.
			for node.msg.Role == message.Assistant &&
				len(node.children) == 1 &&
				node.children[0].msg.Role == message.Assistant {
				child := node.children[0]
				delete(t.nodeMap, node.msg.ID)
				node = child
			}
			node.children = collapseAssistantChains(node.children)
			result = append(result, node)
		}
		return result
	}
	t.roots = collapseAssistantChains(t.roots)

	// Compute depths and mark branch heads.
	//
	// Indentation rules:
	//   - Linear chains (0-1 children): stay flat at the same depth.
	//   - Branch point children: indent +1, marked as branch heads.
	//   - Immediate children of branch heads: indent +1 more (so the
	//     branch head's subtree is visually grouped under it).
	//   - After that first generation, depth stays flat again until the
	//     next branch point.
	var setDepth func(nodes []*treeNode, depth int, parentIsBranchHead bool)
	setDepth = func(nodes []*treeNode, depth int, parentIsBranchHead bool) {
		for _, n := range nodes {
			n.depth = depth
			isBranchPoint := len(n.children) > 1

			if isBranchPoint {
				// Children of a branch point indent +1 and are
				// marked as branch heads.
				for _, child := range n.children {
					child.isBranchHead = true
				}
				setDepth(n.children, depth+1, false)
			} else if n.isBranchHead {
				// Immediate children of a branch head indent +1
				// more, then go flat.
				setDepth(n.children, depth+1, true)
			} else if parentIsBranchHead {
				// We just indented for the branch head's first
				// generation — go flat from here.
				setDepth(n.children, depth, false)
			} else {
				// Linear — stay flat.
				setDepth(n.children, depth, false)
			}
		}
	}
	setDepth(t.roots, 0, false)

	// Compute active path by walking from leaf to root through the
	// *original* message chain (allByID) so filtered-out tool messages
	// don't break the walk. Only visible nodes get marked.
	activePath := make(map[string]bool)
	cur := leafMessageID
	for cur != "" {
		if _, ok := t.nodeMap[cur]; ok {
			activePath[cur] = true
		}
		if orig, ok := allByID[cur]; ok {
			cur = orig.ParentMessageID
		} else {
			break
		}
	}
	for id, node := range t.nodeMap {
		node.isOnActivePath = activePath[id]
	}

	// Initial expand/collapse: nodes on active path are expanded.
	for id := range activePath {
		t.expanded[id] = true
	}

	// Build the filterable list and pre-select the current leaf.
	items := t.rebuildItems()
	t.list = list.NewFilterableList(items...)
	t.list.Focus()
	for i, item := range items {
		if ti, ok := item.(*TreeItem); ok && ti.isLeaf {
			t.list.SetSelected(i)
			break
		}
	}
	t.needsInitialScroll = true

	// Set up text input for filtering.
	t.input = textinput.New()
	t.input.SetVirtualCursor(false)
	t.input.Placeholder = "Filter messages..."
	t.input.SetStyles(com.Styles.TextInput)
	t.input.Focus()

	// Key bindings.
	t.keyMap.Select = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "select"),
	)
	t.keyMap.Next = key.NewBinding(
		key.WithKeys("down", "ctrl+n"),
		key.WithHelp("↓", "next"),
	)
	t.keyMap.Previous = key.NewBinding(
		key.WithKeys("up", "ctrl+p"),
		key.WithHelp("↑", "previous"),
	)
	t.keyMap.Expand = key.NewBinding(
		key.WithKeys("right"),
		key.WithHelp("→", "expand"),
	)
	t.keyMap.Collapse = key.NewBinding(
		key.WithKeys("left"),
		key.WithHelp("←", "collapse"),
	)
	t.keyMap.Close = CloseKey

	return t, nil
}

// ID implements [Dialog].
func (t *Tree) ID() string {
	return TreeID
}

// HandleMsg implements [Dialog].
func (t *Tree) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, t.keyMap.Close):
			return ActionClose{}

		case key.Matches(msg, t.keyMap.Select):
			if item := t.selectedTreeItem(); item != nil {
				return ActionNavigateTree{
					MessageID:       item.node.msg.ID,
					ParentMessageID: item.node.msg.ParentMessageID,
					Role:            item.node.msg.Role,
					Content:         messageTextContent(item.node.msg),
				}
			}

		case key.Matches(msg, t.keyMap.Collapse):
			if item := t.selectedTreeItem(); item != nil {
				if item.isCollapsible && t.expanded[item.node.msg.ID] {
					t.expanded[item.node.msg.ID] = false
					t.list.SetItems(t.rebuildItems()...)
				}
			}

		case key.Matches(msg, t.keyMap.Expand):
			if item := t.selectedTreeItem(); item != nil {
				if item.isCollapsible && !t.expanded[item.node.msg.ID] {
					t.expanded[item.node.msg.ID] = true
					t.list.SetItems(t.rebuildItems()...)
				}
			}

		case key.Matches(msg, t.keyMap.Previous):
			t.list.Focus()
			if t.list.IsSelectedFirst() {
				t.list.SelectLast()
			} else {
				t.list.SelectPrev()
			}
			t.list.ScrollToSelected()

		case key.Matches(msg, t.keyMap.Next):
			t.list.Focus()
			if t.list.IsSelectedLast() {
				t.list.SelectFirst()
			} else {
				t.list.SelectNext()
			}
			t.list.ScrollToSelected()

		default:
			var cmd tea.Cmd
			t.input, cmd = t.input.Update(msg)
			value := t.input.Value()
			t.list.SetFilter(value)
			t.list.ScrollToTop()
			t.list.SetSelected(0)
			return ActionCmd{cmd}
		}
	}
	return nil
}

// Cursor returns the cursor position relative to the dialog.
func (t *Tree) Cursor() *tea.Cursor {
	return InputCursor(t.com.Styles, t.input.Cursor())
}

// Draw implements [Dialog].
func (t *Tree) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	sty := t.com.Styles
	width := max(0, min(navDialogMaxWidth, area.Dx()-sty.Dialog.View.GetHorizontalBorderSize()))
	height := max(0, min(navDialogHeight, area.Dy()-sty.Dialog.View.GetVerticalBorderSize()))
	innerWidth := width - sty.Dialog.View.GetHorizontalFrameSize()
	heightOffset := sty.Dialog.Title.GetVerticalFrameSize() + titleContentHeight +
		sty.Dialog.InputPrompt.GetVerticalFrameSize() + inputContentHeight +
		sty.Dialog.View.GetVerticalFrameSize()
	t.input.SetWidth(max(0, innerWidth-sty.Dialog.InputPrompt.GetHorizontalFrameSize()-1))
	t.list.SetSize(innerWidth, height-heightOffset)
	if t.needsInitialScroll {
		t.needsInitialScroll = false
		t.list.ScrollToSelected()
	}

	rc := NewRenderContext(sty, width)
	rc.Title = "Session Tree"

	inputView := sty.Dialog.InputPrompt.Render(t.input.View())
	cur := t.Cursor()
	rc.AddPart(inputView)

	listView := sty.Dialog.List.Height(t.list.Height()).Render(t.list.Render())
	rc.AddPart(listView)

	view := rc.Render()
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// selectedTreeItem returns the currently selected TreeItem, or nil.
func (t *Tree) selectedTreeItem() *TreeItem {
	if item := t.list.SelectedItem(); item != nil {
		if ti, ok := item.(*TreeItem); ok {
			return ti
		}
	}
	return nil
}

// rebuildItems flattens the tree into a list of FilterableItems using DFS,
// respecting expand/collapse state.
func (t *Tree) rebuildItems() []list.FilterableItem {
	var items []list.FilterableItem
	var walk func(nodes []*treeNode)
	walk = func(nodes []*treeNode) {
		for _, node := range nodes {
			isBranchPoint := len(node.children) > 1
			isCollapsible := isBranchPoint || node.isBranchHead
			isExpanded := t.expanded[node.msg.ID]
			isLeaf := node.msg.ID == t.leafMessageID
			label := messageTextContent(node.msg)

			items = append(items, NewTreeItem(
				t.com.Styles,
				node,
				node.depth,
				isCollapsible,
				isExpanded,
				isLeaf,
				node.isOnActivePath,
				label,
			))

			// Collapsible nodes (branch points and branch heads)
			// respect expand/collapse state. All other nodes always
			// show their children.
			if isCollapsible {
				if isExpanded {
					walk(node.children)
				}
			} else {
				walk(node.children)
			}
		}
	}
	walk(t.roots)
	return items
}

// messageTextContent returns the first text content from a message, with
// whitespace collapsed to single spaces for single-line display.
func messageTextContent(msg message.Message) string {
	for _, part := range msg.Parts {
		if tc, ok := part.(message.TextContent); ok {
			return strings.Join(strings.Fields(tc.Text), " ")
		}
	}
	return ""
}

// sortNodes sorts a slice of tree nodes by message creation time so the
// tree display order is deterministic.
func sortNodes(nodes []*treeNode) {
	slices.SortFunc(nodes, func(a, b *treeNode) int {
		return cmp.Compare(a.msg.CreatedAt, b.msg.CreatedAt)
	})
}


