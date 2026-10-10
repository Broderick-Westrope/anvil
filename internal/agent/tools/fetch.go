package tools

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/PuerkitoBio/goquery"
)

const (
	FetchToolName = "fetch"
	MaxFetchSize  = 100 * 1024 // 100KB
)

//go:embed fetch.md.tpl
var fetchDescriptionTmpl []byte

var fetchDescriptionTpl = template.Must(
	template.New("fetchDescription").
		Parse(string(fetchDescriptionTmpl)),
)

type fetchDescriptionData struct {
	GhAvailable    bool
	MaxFetchSizeKB int
}

func fetchDescription() string {
	return renderTemplate(fetchDescriptionTpl, fetchDescriptionData{
		GhAvailable:    ghAvailable,
		MaxFetchSizeKB: MaxFetchSize / 1024,
	})
}

func NewFetchTool(permissions permission.Service, workingDir string, client *http.Client) fantasy.AgentTool {
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConns = 100
		transport.MaxIdleConnsPerHost = 10
		transport.IdleConnTimeout = 90 * time.Second

		client = &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		}
	}

	return fantasy.NewParallelAgentTool(
		FetchToolName,
		fetchDescription(),
		func(ctx context.Context, params FetchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.URL == "" {
				return fantasy.NewTextErrorResponse("URL parameter is required"), nil
			}

			convert, ok := fetchFormats[strings.ToLower(params.Format)]
			if !ok {
				return fantasy.NewTextErrorResponse("Format must be one of: text, markdown, html"), nil
			}

			if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
				return fantasy.NewTextErrorResponse("URL must start with http:// or https://"), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for creating a new file")
			}

			denied, granted, err := requestPermission(ctx, permissions,
				permission.CreatePermissionRequest{
					SessionID:   sessionID,
					Path:        workingDir,
					ToolCallID:  call.ID,
					ToolName:    FetchToolName,
					Action:      "fetch",
					Description: fmt.Sprintf("Fetch content from URL: %s", params.URL),
					Params:      FetchPermissionsParams(params),
					Input:       params.URL,
				},
			)
			if !granted {
				return denied, err
			}

			// maxFetchTimeoutSeconds is the maximum allowed timeout for fetch requests (2 minutes)
			const maxFetchTimeoutSeconds = 120

			// Handle timeout with context
			requestCtx := ctx
			if params.Timeout > 0 {
				timeout := min(params.Timeout, maxFetchTimeoutSeconds)
				var cancel context.CancelFunc
				requestCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
				defer cancel()
			}

			req, err := http.NewRequestWithContext(requestCtx, "GET", params.URL, nil)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("failed to create request: %w", err)
			}

			req.Header.Set("User-Agent", "anvil/1.0")

			resp, err := client.Do(req)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("failed to fetch URL: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Request failed with status code: %d", resp.StatusCode)), nil
			}

			body, err := io.ReadAll(io.LimitReader(resp.Body, MaxFetchSize))
			if err != nil {
				return fantasy.NewTextErrorResponse("Failed to read response body: " + err.Error()), nil
			}

			content := string(body)

			validUTF8 := utf8.ValidString(content)
			if !validUTF8 {
				return fantasy.NewTextErrorResponse("Response content is not valid UTF-8"), nil
			}
			contentType := resp.Header.Get("Content-Type")

			content, err = convert(content, strings.Contains(contentType, "text/html"))
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			// truncate content if it exceeds max read size
			if int64(len(content)) >= MaxFetchSize {
				content = content[:MaxFetchSize]
				content += fmt.Sprintf("\n\n[Content truncated to %d bytes]", MaxFetchSize)
			}

			return fantasy.NewTextResponse(content), nil
		})
}

// fetchFormats converts a fetched body into each format the tool returns.
// isHTML reports whether the server sent HTML.
var fetchFormats = map[string]func(content string, isHTML bool) (string, error){
	"text":     fetchAsText,
	"markdown": fetchAsMarkdown,
	"html":     fetchAsHTML,
}

// fetchFailure is a conversion failure the model sees as is.
type fetchFailure string

func (f fetchFailure) Error() string { return string(f) }

func fetchAsText(content string, isHTML bool) (string, error) {
	if !isHTML {
		return content, nil
	}
	text, err := extractTextFromHTML(content)
	if err != nil {
		return "", fetchFailure("Failed to extract text from HTML: " + err.Error())
	}
	return text, nil
}

func fetchAsMarkdown(content string, isHTML bool) (string, error) {
	if isHTML {
		markdown, err := convertHTMLToMarkdown(content)
		if err != nil {
			return "", fetchFailure("Failed to convert HTML to Markdown: " + err.Error())
		}
		content = markdown
	}
	return "```\n" + content + "\n```", nil
}

// fetchAsHTML returns only the body of an HTML document.
func fetchAsHTML(content string, isHTML bool) (string, error) {
	if !isHTML {
		return content, nil
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(content))
	if err != nil {
		return "", fetchFailure("Failed to parse HTML: " + err.Error())
	}
	body, err := doc.Find("body").Html()
	if err != nil {
		return "", fetchFailure("Failed to extract body from HTML: " + err.Error())
	}
	if body == "" {
		return "", fetchFailure("No body content found in HTML")
	}
	return "<html>\n<body>\n" + body + "\n</body>\n</html>", nil
}

func extractTextFromHTML(html string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}

	text := doc.Find("body").Text()
	text = strings.Join(strings.Fields(text), " ")

	return text, nil
}

func convertHTMLToMarkdown(html string) (string, error) {
	converter := md.NewConverter("", true, nil)

	markdown, err := converter.ConvertString(html)
	if err != nil {
		return "", err
	}

	return markdown, nil
}
