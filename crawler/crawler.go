package crawler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Error      error  `json:"error" binding:"required"`
}

type AnalyzeOutput struct {
	RootURL     string `json:"root_url" binding:"required"`
	Depth       int    `json:"depth" binding:"required"`
	GeneratedAt string `json:"generated_at" binding:"required"`
	Pages       []Page `json:"pages" binding:"required"`
}

func makeRequest(ctx context.Context, opts Options, URL, method string) (*http.Response, error) {
	if method != "HEAD" && method != "GET" {
		return nil, errors.New("неверный тип запроса")
	}
	req, err := http.NewRequestWithContext(
		ctx,
		method,
		URL,
		nil,
	)

	if err != nil {
		return nil, fmt.Errorf("ошибка создания запроса: %w", err)
	}

	if opts.UserAgent != "" {
		req.Header.Set("User-Agent", opts.UserAgent)
	}

	resp, err := opts.HTTPClient.Do(req)

	if err != nil {
		return nil, fmt.Errorf("ошибка выполнения запроса: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	return resp, nil
}

type ValidateOutput struct {
	URL        string
	InvalidURL bool
	BrokenLink bool
	Error      error
	StatusCode int
}

func AnalyzeURL(ctx context.Context, opts Options) (AnalyzeOutput, error) {

	// 1. Получаем тело страницы
	resp, err := makeRequest(ctx, opts, opts.URL, http.MethodGet)

	if err != nil {
		return AnalyzeOutput{
			RootURL:     opts.URL,
			Depth:       opts.Depth,
			GeneratedAt: time.Now().Format(time.RFC3339),
			Pages: []Page{
				{
					URL:   opts.URL,
					Depth: 0,
					Error: err,
				},
			},
		}, nil
	}

	// 2. Формируем отчет
	return AnalyzeOutput{
		RootURL:     opts.URL,
		Depth:       opts.Depth,
		GeneratedAt: time.Now().Format(time.RFC3339),
		Pages: []Page{
			{
				URL:        opts.URL,
				Depth:      0,
				HTTPStatus: resp.StatusCode,
				Status:     resp.Status,
				Error:      nil,
			},
		},
	}, nil
}

func Analyze(ctx context.Context, opts Options) ([]byte, error) {
	output, err := AnalyzeURL(ctx, opts)
	if err != nil {
		return nil, err
	}

	return output.format(), nil
}

func (output AnalyzeOutput) format() []byte {
	fmtOutput, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		panic("ошибка форматирования результата")
	}
	return fmtOutput
}
