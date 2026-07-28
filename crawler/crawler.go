package crawler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Options struct {
	URL       string
	Depth     int
	Retries   int
	Delay     string
	Timeout   string
	UserAgent string
	// Concurrency,
	// IndentJSON,
	HTTPClient HTTPClient
}

type Page struct {
	URL        string `json:"url" binding:"required"`
	Depth      int    `json:"depth" binding:"required"`
	HTTPStatus int    `json:"http_status" binding:"required"`
	Status     string `json:"status" binding:"required"`
	Error      string `json:"error" binding:"required"`
}

type AnalyzeOutput struct {
	RootURL     string `json:"root_url" binding:"required"`
	Depth       int    `json:"depth" binding:"required"`
	GeneratedAt string `json:"generated_at" binding:"required"`
	Pages       []Page `json:"pages" binding:"required"`
}

func Analyze(ctx context.Context, opts Options) ([]byte, error) {

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		opts.URL,
		nil,
	)

	if err != nil {
		return nil, err
	}

	if opts.UserAgent != "" {
		req.Header.Set("User-Agent", opts.UserAgent)
	}

	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	errorText := ""

	if resp.StatusCode != 200 {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}

		errorText = string(body)
	}

	output := AnalyzeOutput{
		RootURL:     opts.URL,
		Depth:       opts.Depth,
		GeneratedAt: time.Now().Format(time.RFC3339),
		Pages: []Page{
			{
				URL:        opts.URL,
				Depth:      0,
				HTTPStatus: resp.StatusCode,
				Status:     resp.Status,
				Error:      errorText,
			},
		},
	}

	fmtOutput, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return nil, err
	}

	return fmtOutput, nil
}
