package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	"github.com/Broderick-Westrope/anvil/internal/secrets"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

const (
	testStripeKey = "sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn"
	testPEMBody   = "MIIEpA" + "IBAAKC" + "AQEA"
)

// promptRecorder is a [fantasy.LanguageModel] that records the prompt of
// every request it receives.
type promptRecorder struct {
	prompts []fantasy.Prompt
}

func (r *promptRecorder) Generate(_ context.Context, call fantasy.Call) (*fantasy.Response, error) {
	r.prompts = append(r.prompts, call.Prompt)
	return &fantasy.Response{}, nil
}

func (r *promptRecorder) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	r.prompts = append(r.prompts, call.Prompt)
	return func(func(fantasy.StreamPart) bool) {}, nil
}

func (r *promptRecorder) GenerateObject(_ context.Context, call fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	r.prompts = append(r.prompts, call.Prompt)
	return &fantasy.ObjectResponse{}, nil
}

func (r *promptRecorder) StreamObject(_ context.Context, call fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	r.prompts = append(r.prompts, call.Prompt)
	return nil, nil
}

func (r *promptRecorder) Provider() string { return "fake" }
func (r *promptRecorder) Model() string    { return "fake-model" }

func newTestDetector() *secrets.Detector {
	return secrets.New(nil, []byte("012345"+"6789ab"+"cdef01"+"234567"+"89abcd"+"ef"))
}

func TestSecretMaskModel_MasksEveryRequestMethod(t *testing.T) {
	t.Parallel()

	prompt := fantasy.Prompt{fantasy.NewUserMessage("my key is " + testStripeKey)}
	calls := map[string]func(fantasy.LanguageModel) error{
		"generate": func(m fantasy.LanguageModel) error {
			_, err := m.Generate(t.Context(), fantasy.Call{Prompt: prompt})
			return err
		},
		"stream": func(m fantasy.LanguageModel) error {
			_, err := m.Stream(t.Context(), fantasy.Call{Prompt: prompt})
			return err
		},
		"generate object": func(m fantasy.LanguageModel) error {
			_, err := m.GenerateObject(t.Context(), fantasy.ObjectCall{Prompt: prompt})
			return err
		},
		"stream object": func(m fantasy.LanguageModel) error {
			_, err := m.StreamObject(t.Context(), fantasy.ObjectCall{Prompt: prompt})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inner := &promptRecorder{}
			require.NoError(t, call(newSecretMaskModel(inner, newTestDetector())))
			require.Len(t, inner.prompts, 1)
			sent, _ := json.Marshal(inner.prompts[0])
			require.NotContains(t, string(sent), testStripeKey)
			require.Contains(t, string(sent), "[SECRET:"+"stripe_key:")
		})
	}
	require.Contains(t, prompt[0].Content[0].(fantasy.TextPart).Text, testStripeKey, "the caller's prompt must not be changed")
}

func TestSecretMaskModel_MasksEveryPartThatCarriesText(t *testing.T) {
	t.Parallel()

	input, _ := json.Marshal(map[string]any{
		"content": "-----BEGIN RSA PRIVATE KEY-----\n" + testPEMBody + "\nabc",
		"count":   json.Number("12345678901234567890"),
	})
	prompt := fantasy.Prompt{
		{Role: fantasy.MessageRoleSystem, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "system " + testStripeKey}}},
		{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{
			fantasy.TextPart{Text: "I saw " + testStripeKey},
			&fantasy.ToolCallPart{ToolCallID: "1", ToolName: "write", Input: string(input)},
		}},
		{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{
			fantasy.ToolResultPart{ToolCallID: "1", Output: fantasy.ToolResultOutputContentText{Text: "STRIPE=" + testStripeKey}},
			fantasy.ToolResultPart{ToolCallID: "2", Output: fantasy.ToolResultOutputContentError{Error: errors.New("auth failed for " + testStripeKey)}},
			fantasy.ToolResultPart{ToolCallID: "3", Output: fantasy.ToolResultOutputContentMedia{Data: "aGVsbG8=", MediaType: "image/png", Text: "caption " + testStripeKey}},
		}},
	}
	inner := &promptRecorder{}
	_, err := newSecretMaskModel(inner, newTestDetector()).Generate(t.Context(), fantasy.Call{Prompt: prompt})
	require.NoError(t, err)

	sent := inner.prompts[0]
	require.Contains(t, sent[0].Content[0].(fantasy.TextPart).Text, "[SECRET:"+"stripe_key:")
	require.Contains(t, sent[1].Content[0].(fantasy.TextPart).Text, "[SECRET:"+"stripe_key:")

	call := sent[1].Content[1].(fantasy.ToolCallPart)
	require.NotContains(t, call.Input, testPEMBody)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(call.Input), &decoded), "tool input must stay valid JSON")
	require.Contains(t, call.Input, "12345678901234567890", "large numbers must survive the round trip")

	text, _ := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](sent[2].Content[0].(fantasy.ToolResultPart).Output)
	require.NotContains(t, text.Text, testStripeKey)
	errOut, _ := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](sent[2].Content[1].(fantasy.ToolResultPart).Output)
	require.NotContains(t, errOut.Error.Error(), testStripeKey)
	media, _ := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](sent[2].Content[2].(fantasy.ToolResultPart).Output)
	require.NotContains(t, media.Text, testStripeKey)
	require.Equal(t, "aGVsbG8=", media.Data)
}

// File bytes pass untouched: text attachments reach the model inlined as
// text parts (message.PromptWithTextAttachments), which are masked.
func TestSecretMaskModel_LeavesSignedAndProviderPartsAlone(t *testing.T) {
	t.Parallel()

	reasoning := fantasy.ReasoningPart{Text: "thinking about " + testStripeKey}
	serverCall := fantasy.ToolCallPart{ToolCallID: "s", ToolName: "web_search", Input: `{"q":"` + testStripeKey + `"}`, ProviderExecuted: true}
	file := fantasy.FilePart{Data: []byte(testStripeKey), MediaType: "text/plain"}
	prompt := fantasy.Prompt{{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{reasoning, serverCall, file}}}

	inner := &promptRecorder{}
	_, err := newSecretMaskModel(inner, newTestDetector()).Generate(t.Context(), fantasy.Call{Prompt: prompt})
	require.NoError(t, err)
	require.Equal(t, prompt, inner.prompts[0])
}

func TestNewSecretMaskModel_NilDetectorDisables(t *testing.T) {
	t.Parallel()

	inner := &promptRecorder{}
	require.Same(t, fantasy.LanguageModel(inner), newSecretMaskModel(inner, nil))
}

func TestBuildAgentModels_MasksWithTheCoordinatorsDetector(t *testing.T) {
	t.Parallel()

	c := newTimeoutTestCoordinator(t, nil)
	apiKey := "provider-key-" + "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"
	p, _ := c.cfg.Config().Providers.Get("openai")
	p.APIKey = apiKey
	c.cfg.Config().Providers.Set("openai", p)
	c.secrets = newTestDetector()

	for _, agentCfg := range []config.Agent{
		{ID: config.AgentOrchestrator},
		{ID: "specialist", Model: "openai/gpt-4o-mini"},
	} {
		large, small, err := c.buildAgentModels(t.Context(), agentCfg)
		require.NoError(t, err)
		for kind, m := range map[string]fantasy.LanguageModel{"large": large.Model, "small": small.Model} {
			wrapped, ok := m.(secretMaskModel)
			require.True(t, ok, "%s %s model is not masked", agentCfg.ID, kind)
			_, ok = wrapped.LanguageModel.(requestTimeoutModel)
			require.True(t, ok, "masking must wrap the request timeout, so scanning doesn't count against it")
		}
	}

	masked, _ := c.secrets.Mask("echo " + apiKey)
	require.NotContains(t, masked, apiKey, "the provider's API key must be a known value")
}

func TestResolveSecretOnce_AddsTheResolvedValue(t *testing.T) {
	t.Parallel()

	c := newTimeoutTestCoordinator(t, nil)
	c.secrets = newTestDetector()
	token := "fetch-token-" + "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"
	resolve := c.resolveSecretOnce("github_token", token)

	masked, _ := c.secrets.Mask(token)
	require.Equal(t, token, masked, "nothing is known before the tool first needs it")

	got, err := resolve()
	require.NoError(t, err)
	require.Equal(t, token, got)
	masked, _ = c.secrets.Mask(token)
	require.Contains(t, masked, "[SECRET:"+"github_token:")

	require.Nil(t, c.resolveSecretOnce("github_token", ""))
}

func TestBuildAgentModels_LearnsProviderCredentials(t *testing.T) {
	t.Parallel()

	c := newTimeoutTestCoordinator(t, nil)
	access := "access-" + "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"
	refresh := "refresh-" + "Qw3Er5" + "Ty7Ui1" + "Op2AsZ" + "x9"
	header := "header-" + "Er5Ty7" + "Ui1Op2" + "AsZx9Q" + "w3"
	p, _ := c.cfg.Config().Providers.Get("openai")
	p.APIKey = "Bearer " + access
	p.OAuthToken = &oauth.Token{AccessToken: access, RefreshToken: refresh}
	p.ExtraHeaders = map[string]string{"X-Api-Key": header, "Accept": "application/json"}
	c.cfg.Config().Providers.Set("openai", p)
	c.secrets = newTestDetector()

	_, _, err := c.buildAgentModels(t.Context(), config.Agent{ID: config.AgentOrchestrator})
	require.NoError(t, err)

	for _, value := range []string{access, refresh, header} {
		masked, _ := c.secrets.Mask("x " + value)
		require.NotContains(t, masked, value)
	}
	masked, _ := c.secrets.Mask("Accept: application/json")
	require.Equal(t, "Accept: application/json", masked)
}

func TestSecretMaskModel_MasksToolCallInputAsJSON(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input  string
		secret string
	}{
		"multi-line string": {input: `{"command":"cat > .env <<EOF\nDB_PASSWORD=` + `Qw8rTy` + `6uIo4p` + `As2d\nEOF"}`, secret: "Qw8rTy" + "6uIo4p" + "As2d"},
		"key gives context": {input: `{"token":"` + `Ab3dEf` + `6hIj9k` + `Lm2nOp` + `5q"}`, secret: "Ab3dEf" + "6hIj9k" + "Lm2nOp" + "5q"},
		"vendor shape":      {input: `{"note":"key ` + testStripeKey + `"}`, secret: testStripeKey},
		"not json":          {input: `not json ` + testStripeKey, secret: testStripeKey},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			prompt := fantasy.Prompt{{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{
				fantasy.ToolCallPart{ToolCallID: "1", ToolName: "bash", Input: tc.input},
			}}}
			inner := &promptRecorder{}
			_, err := newSecretMaskModel(inner, newTestDetector()).Generate(t.Context(), fantasy.Call{Prompt: prompt})
			require.NoError(t, err)
			got := inner.prompts[0][0].Content[0].(fantasy.ToolCallPart).Input
			require.NotContains(t, got, tc.secret)
			require.Equal(t, json.Valid([]byte(tc.input)), json.Valid([]byte(got)))
		})
	}
}

// maskingTurn is a real coordinator with a detector, talking to a
// provider that records each request.
type maskingTurn struct {
	coord   Coordinator
	server  *recordingAnthropicServer
	env     fakeEnv
	session session.Session
}

func newMaskingTurn(t *testing.T) maskingTurn {
	t.Helper()
	env := testEnv(t)
	server := newRecordingAnthropicServer(t)
	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)
	cfg.Config().Providers.Set("stable-anthropic", config.ProviderConfig{
		ID: "stable-anthropic", Type: catwalk.TypeAnthropic, APIKey: "dummy-api-key", BaseURL: server.URL,
		Models: []catwalk.Model{
			{ID: stableLargeModel, ContextWindow: 200000, DefaultMaxTokens: 1024},
			{ID: stableSmallModel, ContextWindow: 200000, DefaultMaxTokens: 1024},
		},
	})
	cfg.Config().Models = map[config.SelectedModelType]config.SelectedModel{
		config.SelectedModelTypeLarge: {Provider: "stable-anthropic", Model: stableLargeModel},
		config.SelectedModelTypeSmall: {Provider: "stable-anthropic", Model: stableSmallModel},
	}
	cfg.Config().Options.ContextPaths = nil
	sess, err := env.sessions.Create(t.Context(), "Secrets", env.workingDir)
	require.NoError(t, err)
	coord, err := NewCoordinator(t.Context(), cfg, env.sessions, env.messages, env.permissions,
		*env.filetracker, nil, nil, nil, nil, nil, nil, nil, WithSecrets(newTestDetector()))
	require.NoError(t, err)
	t.Cleanup(coord.(*coordinator).WaitBackgroundJobs)
	return maskingTurn{coord: coord, server: server, env: env, session: sess}
}

// The key a prompt carries never reaches the provider, the customer ID
// does, and the next turn repeats the masked history exactly, so the
// prompt cache still hits.
func TestCoordinatorRun_MasksSecretsSentToTheProvider(t *testing.T) {
	turn := newMaskingTurn(t)
	_, err := turn.coord.Run(t.Context(), turn.session.ID, "STRIPE_SECRET_KEY="+testStripeKey+"\nSTRIPE_CUSTOMER=cus_NffrFe"+"UfNV2H"+"ib")
	require.NoError(t, err)
	_, err = turn.coord.Run(t.Context(), turn.session.ID, "and again")
	require.NoError(t, err)

	bodies := turn.server.recorded()
	require.Len(t, bodies, 2)
	for _, body := range bodies {
		require.NotContains(t, string(body), testStripeKey)
		require.Contains(t, string(body), "[SECRET:"+"stripe_key:")
		require.Contains(t, string(body), "cus_NffrFe"+"UfNV2H"+"ib")
	}
	first, second := parseCacheRequest(t, bodies[0]), parseCacheRequest(t, bodies[1])
	require.Equal(t, first.Messages, second.Messages[:len(first.Messages)], "the masked history must repeat exactly")
	require.Contains(t, first.System, "stands for a credential Anvil hid from you")
}

// A tool round in history, as after viewing .env: the result and the call's
// JSON input reach the provider masked, and the input stays valid JSON.
func TestCoordinatorRun_MasksToolRoundsInHistory(t *testing.T) {
	turn := newMaskingTurn(t)
	ctx, id := t.Context(), turn.session.ID
	_, err := turn.env.messages.AppendToLeaf(ctx, id, message.CreateMessageParams{
		Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "what's in .env?"}},
	})
	require.NoError(t, err)
	_, err = turn.env.messages.AppendToLeaf(ctx, id, message.CreateMessageParams{
		Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "call_1", Name: "bash", Input: `{"command":"grep -q ` + testStripeKey + ` .env && cat .env"}`, Finished: true},
		},
	})
	require.NoError(t, err)
	_, err = turn.env.messages.AppendToLeaf(ctx, id, message.CreateMessageParams{
		Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call_1", Name: "bash", Content: "<file>\n     1|STRIPE_SECRET_KEY=" + testStripeKey + "\n     2|STRIPE_CUSTOMER=cus_NffrFe" + "UfNV2H" + "ib\n     3|DB_PASSWORD=" + "Pr0dS3" + "cretVa" + "lue\n</file>"},
		},
	})
	require.NoError(t, err)

	_, err = turn.coord.Run(ctx, id, "thanks")
	require.NoError(t, err)

	bodies := turn.server.recorded()
	require.Len(t, bodies, 1)
	body := string(bodies[0])
	require.NotContains(t, body, testStripeKey)
	require.NotContains(t, body, "Pr0dS3"+"cretVa"+"lue", "a view-numbered .env line is masked")
	require.Contains(t, body, "cus_NffrFe"+"UfNV2H"+"ib")
	var req struct {
		Messages []struct {
			Content []struct {
				Type  string          `json:"type"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(bodies[0], &req))
	var inputs int
	for _, m := range req.Messages {
		for _, c := range m.Content {
			if c.Type == "tool_use" {
				inputs++
				require.Contains(t, string(c.Input), "[SECRET:"+"stripe_key:")
			}
		}
	}
	require.Equal(t, 1, inputs)
}

// oddPart claims to be text but isn't a fantasy.TextPart, as a part type a
// fantasy upgrade adds might.
type oddPart struct{ fantasy.TextPart }

func TestSecretMaskModel_RefusesPartsItCantMask(t *testing.T) {
	t.Parallel()

	prompts := map[string]fantasy.Prompt{
		"unknown text shape": {{Role: fantasy.MessageRoleUser, Content: []fantasy.MessagePart{oddPart{fantasy.TextPart{Text: testStripeKey}}}}},
		"unknown output":     {{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.ToolResultPart{ToolCallID: "1", Output: oddOutput{}}}}},
		"odd text output":    {{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.ToolResultPart{ToolCallID: "1", Output: oddOutputOf{fantasy.ToolResultContentTypeText}}}}},
		"odd error output":   {{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.ToolResultPart{ToolCallID: "1", Output: oddOutputOf{fantasy.ToolResultContentTypeError}}}}},
		"odd media output":   {{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.ToolResultPart{ToolCallID: "1", Output: oddOutputOf{fantasy.ToolResultContentTypeMedia}}}}},
		"odd tool call":      {{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{oddPartOf{kind: fantasy.ContentTypeToolCall}}}},
		"odd tool result":    {{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{oddPartOf{kind: fantasy.ContentTypeToolResult}}}},
		"unknown part type":  {{Role: fantasy.MessageRoleUser, Content: []fantasy.MessagePart{unknownPart{}}}},
		"panicking part":     {{Role: fantasy.MessageRoleUser, Content: []fantasy.MessagePart{panickingPart{}}}},
	}
	calls := map[string]func(fantasy.LanguageModel, fantasy.Prompt) error{
		"generate": func(m fantasy.LanguageModel, p fantasy.Prompt) error {
			_, err := m.Generate(t.Context(), fantasy.Call{Prompt: p})
			return err
		},
		"stream": func(m fantasy.LanguageModel, p fantasy.Prompt) error {
			_, err := m.Stream(t.Context(), fantasy.Call{Prompt: p})
			return err
		},
		"generate object": func(m fantasy.LanguageModel, p fantasy.Prompt) error {
			_, err := m.GenerateObject(t.Context(), fantasy.ObjectCall{Prompt: p})
			return err
		},
		"stream object": func(m fantasy.LanguageModel, p fantasy.Prompt) error {
			_, err := m.StreamObject(t.Context(), fantasy.ObjectCall{Prompt: p})
			return err
		},
	}
	for name, prompt := range prompts {
		for callName, call := range calls {
			t.Run(name+"/"+callName, func(t *testing.T) {
				t.Parallel()
				inner := &promptRecorder{}
				err := call(newSecretMaskModel(inner, newTestDetector()), prompt)
				require.ErrorContains(t, err, "mask secrets")
				require.Empty(t, inner.prompts, "nothing unmasked is sent")
			})
		}
	}
}

// unknownPart is a part type fantasy doesn't define today.
type unknownPart struct{ fantasy.TextPart }

func (unknownPart) GetType() fantasy.ContentType { return "hologram" }

// panickingPart stands for a bug anywhere in masking.
type panickingPart struct{ fantasy.TextPart }

func (panickingPart) GetType() fantasy.ContentType { panic("boom") }

// oddOutputOf claims an output type but isn't fantasy's struct for it.
type oddOutputOf struct{ kind fantasy.ToolResultContentType }

func (o oddOutputOf) GetType() fantasy.ToolResultContentType { return o.kind }

// oddPartOf claims a part type but isn't fantasy's struct for it.
type oddPartOf struct {
	fantasy.TextPart
	kind fantasy.ContentType
}

func (p oddPartOf) GetType() fantasy.ContentType { return p.kind }

type oddOutput struct{}

func (oddOutput) GetType() fantasy.ToolResultContentType { return "odd" }

// MCP and API tools return raw JSON, where a key follows an escaped
// newline that rules reading the text as written can't see past.
func TestSecretMaskModel_MasksEscapedJSONToolOutput(t *testing.T) {
	t.Parallel()

	out := `{"text":"Your new token:\nghp_` + `aBcDeF` + `gHiJkL` + `mNoPqR` + `sTuVwX` + `yZ0123` + `456789","env":"A=1\nDB_PASSWORD=` + `Zx9Qw3` + `Er5Ty7` + `Ui1Op2` + `As\nB=2"}`
	prompt := fantasy.Prompt{{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{
		fantasy.ToolResultPart{ToolCallID: "1", Output: fantasy.ToolResultOutputContentText{Text: out}},
	}}}
	inner := &promptRecorder{}
	_, err := newSecretMaskModel(inner, newTestDetector()).Generate(t.Context(), fantasy.Call{Prompt: prompt})
	require.NoError(t, err)
	got, _ := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](inner.prompts[0][0].Content[0].(fantasy.ToolResultPart).Output)
	require.NotContains(t, got.Text, "aBcDeF"+"gHiJkL"+"m")
	require.NotContains(t, got.Text, "Zx9Qw3"+"Er5Ty7"+"Ui1Op2"+"As")
	require.True(t, json.Valid([]byte(got.Text)))
}

func TestSecretMaskModel_StopsWhenCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	inner := &promptRecorder{}
	_, err := newSecretMaskModel(inner, newTestDetector()).Generate(ctx, fantasy.Call{Prompt: fantasy.Prompt{fantasy.NewUserMessage("hi")}})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, inner.prompts)
}
