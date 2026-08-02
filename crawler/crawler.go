package crawler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/html"
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
	URL         string       `json:"url" binding:"required"`
	Depth       int          `json:"depth" binding:"required"`
	HTTPStatus  int          `json:"http_status" binding:"required"`
	Status      string       `json:"status" binding:"required"`
	Error       error        `json:"error" binding:"required"`
	BrokenLinks []BrokenLink `json:"broken_links" binding:"required"`
}

type BrokenLink struct {
	Url        string `json:"url" binding:"required"`
	StatusCode int    `json:"status_code" binding:"required"`
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

	return resp, nil
}

type ValidateOutput struct {
	URL        string
	InvalidURL bool
	BrokenLink bool
	Error      error
	StatusCode int
}

func ValidateURL(ctx context.Context, opts Options, URL string) ValidateOutput {
	if URL == "" {
		return ValidateOutput{URL: URL, InvalidURL: true}
	}

	parsedURL, err := url.Parse(URL)
	if err != nil {
		return ValidateOutput{URL: URL, InvalidURL: true}
	}

	if parsedURL.IsAbs() {
		if !validateLink(URL) {
			return ValidateOutput{URL: URL, InvalidURL: true}
		}
	} else {
		parsedURL, err = normalizeLink(URL, opts.URL)
		if err != nil {
			return ValidateOutput{URL: URL, InvalidURL: true}
		}
	}

	resp, err := makeRequest(ctx, opts, parsedURL.String(), http.MethodHead)

	if err != nil {

		unwrapErr := err
		for unwrapErr != nil {
			fmt.Printf("%T %v \n", unwrapErr, unwrapErr)
			unwrapErr = errors.Unwrap(unwrapErr)
		}

		return ValidateOutput{
			URL:        parsedURL.String(),
			BrokenLink: true,
			Error:      err,
		}
	}

	if resp.StatusCode == 405 {
		resp, err = makeRequest(ctx, opts, parsedURL.String(), http.MethodGet)

		if err != nil {
			return ValidateOutput{
				URL:        parsedURL.String(),
				BrokenLink: true,
				Error:      err,
			}
		}
	}

	if resp.StatusCode != 200 {
		return ValidateOutput{
			URL:        parsedURL.String(),
			BrokenLink: true,
			StatusCode: resp.StatusCode,
		}
	}

	return ValidateOutput{URL: URL}
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
					BrokenLinks: []BrokenLink{},
					URL:         opts.URL,
					Depth:       0,
					Error:       err,
				},
			},
		}, nil
	}

	// 2. Ищем битые ссылки
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return AnalyzeOutput{
			RootURL:     opts.URL,
			Depth:       opts.Depth,
			GeneratedAt: time.Now().Format(time.RFC3339),
			Pages: []Page{
				{
					BrokenLinks: []BrokenLink{},
					URL:         opts.URL,
					Depth:       0,
					Error:       err,
				},
			},
		}, fmt.Errorf("ошибка чтения html страницы: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	brokenLinks := []BrokenLink{}
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Data == "a" {
			pageURL := ""
			for _, a := range n.Attr {
				if a.Key == "href" {
					pageURL = a.Val
					break
				}
			}

			validationOutput := ValidateURL(ctx, opts, pageURL)

			if validationOutput.InvalidURL {
				continue
			}

			if !validationOutput.BrokenLink {
				continue
			}

			brokenLinks = append(brokenLinks, BrokenLink{
				Url:        validationOutput.URL,
				Error:      validationOutput.Error,
				StatusCode: validationOutput.StatusCode,
			})
		}
	}

	// 3. Формируем отчет
	return AnalyzeOutput{
		RootURL:     opts.URL,
		Depth:       opts.Depth,
		GeneratedAt: time.Now().Format(time.RFC3339),
		Pages: []Page{
			{
				URL:         opts.URL,
				Depth:       0,
				HTTPStatus:  resp.StatusCode,
				Status:      resp.Status,
				Error:       nil,
				BrokenLinks: brokenLinks,
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

//	type Seo struct {
//		HasTitle       bool   `json:"has_title" binding:"required"`
//		Title          string `json:"title" binding:"required"`
//		HasDescription bool   `json:"has_description" binding:"required"`
//		Description    string `json:"description" binding:"required"`
//		HasH1          bool   `json:"has_h1" binding:"required"`
//	}

// Seo         Seo
