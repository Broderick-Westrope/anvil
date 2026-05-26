package message

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/google/uuid"
)

type CreateMessageParams struct {
	Role            MessageRole
	Parts           []ContentPart
	Model           string
	Provider        string
	ParentMessageID string
	MessageType     MessageType
}

type Service interface {
	pubsub.Subscriber[Message]
	Create(ctx context.Context, sessionID string, params CreateMessageParams) (Message, error)
	Update(ctx context.Context, message Message) error
	Get(ctx context.Context, id string) (Message, error)
	List(ctx context.Context, sessionID string) ([]Message, error)
	ListUserMessages(ctx context.Context, sessionID string) ([]Message, error)
	ListAllUserMessages(ctx context.Context) ([]Message, error)
	Delete(ctx context.Context, id string) error
	DeleteSessionMessages(ctx context.Context, sessionID string) error
	GetBranchPath(ctx context.Context, leafMessageID string) ([]Message, error)
	GetChildren(ctx context.Context, messageID string) ([]Message, error)
	GetAllSessionMessages(ctx context.Context, sessionID string) ([]Message, error)
}

type service struct {
	*pubsub.Broker[Message]
	conn *sql.DB
	q    *db.Queries
}

// NewService creates a new message service. The conn parameter is used
// for transaction support (atomic create + leaf advance).
func NewService(q *db.Queries, conn *sql.DB) Service {
	if conn == nil {
		panic("message.NewService: conn must not be nil")
	}
	return &service{
		Broker: pubsub.NewBroker[Message](),
		conn:   conn,
		q:      q,
	}
}

func (s *service) Delete(ctx context.Context, id string) error {
	message, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	err = s.q.DeleteMessage(ctx, message.ID)
	if err != nil {
		return err
	}
	// Clone the message before publishing to avoid race conditions with
	// concurrent modifications to the Parts slice.
	s.Publish(pubsub.DeletedEvent, message.Clone())
	return nil
}

func (s *service) Create(ctx context.Context, sessionID string, params CreateMessageParams) (Message, error) {
	if params.Role != Assistant && (params.MessageType == "" || params.MessageType == MessageTypeMessage) {
		params.Parts = append(params.Parts, Finish{
			Reason: "stop",
		})
	}
	partsJSON, err := marshalParts(params.Parts)
	if err != nil {
		return Message{}, err
	}
	msgType := string(params.MessageType)
	if msgType == "" {
		msgType = string(MessageTypeMessage)
	}

	msgID := uuid.New().String()
	createParams := db.CreateMessageParams{
		ID:              msgID,
		SessionID:       sessionID,
		Role:            string(params.Role),
		Parts:           string(partsJSON),
		Model:           sql.NullString{String: string(params.Model), Valid: true},
		Provider:        sql.NullString{String: params.Provider, Valid: params.Provider != ""},
		ParentMessageID: sql.NullString{String: params.ParentMessageID, Valid: params.ParentMessageID != ""},
		MessageType:     msgType,
	}

	// When a ParentMessageID is provided, atomically create the message
	// and advance the session's leaf pointer within a transaction.
	var dbMessage db.Message
	if params.ParentMessageID != "" {
		tx, txErr := s.conn.BeginTx(ctx, nil)
		if txErr != nil {
			return Message{}, fmt.Errorf("beginning transaction: %w", txErr)
		}
		defer tx.Rollback() //nolint:errcheck

		qtx := s.q.WithTx(tx)
		dbMessage, err = qtx.CreateMessage(ctx, createParams)
		if err != nil {
			return Message{}, err
		}
		err = qtx.UpdateSessionLeaf(ctx, db.UpdateSessionLeafParams{
			ID:     sessionID,
			LeafID: sql.NullString{String: dbMessage.ID, Valid: true},
		})
		if err != nil {
			return Message{}, fmt.Errorf("advancing leaf: %w", err)
		}
		if err = tx.Commit(); err != nil {
			return Message{}, fmt.Errorf("committing transaction: %w", err)
		}
	} else {
		dbMessage, err = s.q.CreateMessage(ctx, createParams)
		if err != nil {
			return Message{}, err
		}
	}

	message, err := s.fromDBItem(dbMessage)
	if err != nil {
		return Message{}, err
	}
	// Clone the message before publishing to avoid race conditions with
	// concurrent modifications to the Parts slice.
	s.Publish(pubsub.CreatedEvent, message.Clone())
	return message, nil
}

func (s *service) DeleteSessionMessages(ctx context.Context, sessionID string) error {
	messages, err := s.List(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, message := range messages {
		if message.SessionID == sessionID {
			err = s.Delete(ctx, message.ID)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *service) Update(ctx context.Context, message Message) error {
	parts, err := marshalParts(message.Parts)
	if err != nil {
		return err
	}
	finishedAt := sql.NullInt64{}
	if f := message.FinishPart(); f != nil {
		finishedAt.Int64 = f.Time
		finishedAt.Valid = true
	}
	err = s.q.UpdateMessage(ctx, db.UpdateMessageParams{
		ID:         message.ID,
		Parts:      string(parts),
		FinishedAt: finishedAt,
	})
	if err != nil {
		return err
	}
	message.UpdatedAt = time.Now().Unix()
	// Clone the message before publishing to avoid race conditions with
	// concurrent modifications to the Parts slice.
	s.Publish(pubsub.UpdatedEvent, message.Clone())
	return nil
}

func (s *service) Get(ctx context.Context, id string) (Message, error) {
	dbMessage, err := s.q.GetMessage(ctx, id)
	if err != nil {
		return Message{}, err
	}
	return s.fromDBItem(dbMessage)
}

func (s *service) List(ctx context.Context, sessionID string) ([]Message, error) {
	dbMessages, err := s.q.ListMessagesBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func (s *service) ListUserMessages(ctx context.Context, sessionID string) ([]Message, error) {
	dbMessages, err := s.q.ListUserMessagesBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func (s *service) ListAllUserMessages(ctx context.Context) ([]Message, error) {
	dbMessages, err := s.q.ListAllUserMessages(ctx)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func (s *service) GetBranchPath(ctx context.Context, leafMessageID string) ([]Message, error) {
	dbMessages, err := s.q.GetBranchPath(ctx, leafMessageID)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(db.Message{
			ID:              dbMessage.ID,
			SessionID:       dbMessage.SessionID,
			Role:            dbMessage.Role,
			Parts:           dbMessage.Parts,
			Model:           dbMessage.Model,
			Provider:        dbMessage.Provider,
			FinishedAt:      dbMessage.FinishedAt,
			ParentMessageID: dbMessage.ParentMessageID,
			MessageType:     dbMessage.MessageType,
			CreatedAt:       dbMessage.CreatedAt,
			UpdatedAt:       dbMessage.UpdatedAt,
		})
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func (s *service) GetChildren(ctx context.Context, messageID string) ([]Message, error) {
	dbMessages, err := s.q.GetMessageChildren(ctx, sql.NullString{String: messageID, Valid: messageID != ""})
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func (s *service) GetAllSessionMessages(ctx context.Context, sessionID string) ([]Message, error) {
	dbMessages, err := s.q.GetAllSessionMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func (s *service) fromDBItem(item db.Message) (Message, error) {
	parts, err := unmarshalParts([]byte(item.Parts))
	if err != nil {
		return Message{}, err
	}
	return Message{
		ID:              item.ID,
		SessionID:       item.SessionID,
		Role:            MessageRole(item.Role),
		Parts:           parts,
		Model:           item.Model.String,
		Provider:        item.Provider.String,
		ParentMessageID: item.ParentMessageID.String,
		MessageType:     MessageType(item.MessageType),
		CreatedAt:       item.CreatedAt,
		UpdatedAt:       item.UpdatedAt,
	}, nil
}

type partType string

const (
	reasoningType           partType = "reasoning"
	textType                partType = "text"
	imageURLType            partType = "image_url"
	binaryType              partType = "binary"
	toolCallType            partType = "tool_call"
	toolResultType          partType = "tool_result"
	finishType              partType = "finish"
	compactionType          partType = "compaction"
	branchSummaryType       partType = "branch_summary"
	labelType               partType = "label"
	modelChangeType         partType = "model_change"
	thinkingLevelChangeType partType = "thinking_level_change"
)

type partWrapper struct {
	Type partType    `json:"type"`
	Data ContentPart `json:"data"`
}

func marshalParts(parts []ContentPart) ([]byte, error) {
	wrappedParts := make([]partWrapper, len(parts))

	for i, part := range parts {
		var typ partType

		switch part.(type) {
		case ReasoningContent:
			typ = reasoningType
		case TextContent:
			typ = textType
		case ImageURLContent:
			typ = imageURLType
		case BinaryContent:
			typ = binaryType
		case ToolCall:
			typ = toolCallType
		case ToolResult:
			typ = toolResultType
		case Finish:
			typ = finishType
		case CompactionContent:
			typ = compactionType
		case BranchSummaryContent:
			typ = branchSummaryType
		case LabelContent:
			typ = labelType
		case ModelChangeContent:
			typ = modelChangeType
		case ThinkingLevelChangeContent:
			typ = thinkingLevelChangeType
		default:
			return nil, fmt.Errorf("unknown part type: %T", part)
		}

		wrappedParts[i] = partWrapper{
			Type: typ,
			Data: part,
		}
	}
	return json.Marshal(wrappedParts)
}

func unmarshalParts(data []byte) ([]ContentPart, error) {
	temp := []json.RawMessage{}

	if err := json.Unmarshal(data, &temp); err != nil {
		return nil, err
	}

	parts := make([]ContentPart, 0)

	for _, rawPart := range temp {
		var wrapper struct {
			Type partType        `json:"type"`
			Data json.RawMessage `json:"data"`
		}

		if err := json.Unmarshal(rawPart, &wrapper); err != nil {
			return nil, err
		}

		switch wrapper.Type {
		case reasoningType:
			part := ReasoningContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case textType:
			part := TextContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case imageURLType:
			part := ImageURLContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case binaryType:
			part := BinaryContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case toolCallType:
			part := ToolCall{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case toolResultType:
			part := ToolResult{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case finishType:
			part := Finish{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case compactionType:
			part := CompactionContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case branchSummaryType:
			part := BranchSummaryContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case labelType:
			part := LabelContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case modelChangeType:
			part := ModelChangeContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case thinkingLevelChangeType:
			part := ThinkingLevelChangeContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		default:
			return nil, fmt.Errorf("unknown part type: %s", wrapper.Type)
		}
	}

	return parts, nil
}
