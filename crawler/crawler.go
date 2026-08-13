package crawler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Options struct {
	URL         string
	Depth       int
	Retries     int
	Delay       string
	Timeout     string
	UserAgent   string
	Concurrency int
	// IndentJSON,
	HTTPClient HTTPClient
}

type Seo struct {
	HasTitle       bool   `json:"has_title" binding:"required"`
	Title          string `json:"title" binding:"required"`
	HasDescription bool   `json:"has_description" binding:"required"`
	Description    string `json:"description" binding:"required"`
	HasH1          bool   `json:"has_h1" binding:"required"`
}

type Page struct {
	URL         string       `json:"url" binding:"required"`
	Depth       int          `json:"depth" binding:"required"`
	HTTPStatus  int          `json:"http_status" binding:"required"`
	Status      string       `json:"status" binding:"required"`
	Error       error        `json:"error" binding:"required"`
	BrokenLinks []BrokenLink `json:"broken_links" binding:"required"`
	Seo         Seo          `json:"seo" binding:"required"`
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

type LinkOptions struct {
	pageURL string
	pageIdx int64
	linkURl string
	Depth   int
}

type LinkAnalizeOutput struct {
	Url        string
	pageIdx    int64
	StatusCode int
	Error      error
	pageURL    string
}

type PageOptions struct {
	PageURL string
	Depth   int
	Idx     int64
}

var counter atomic.Int64

type VisitedURLs struct {
	mu sync.RWMutex
	m  map[string]struct{}
}

func (v *VisitedURLs) Get(key string) (struct{}, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	val, ok := v.m[key]
	return val, ok
}

func (c *VisitedURLs) Add(value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[value] = struct{}{}
}

var visitedUrls VisitedURLs

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

func AnalyzeLink(ctx context.Context, linkOptions LinkOptions, linkAnalyzeOutputs chan<- LinkAnalizeOutput, analyzePageJobs chan<- PageOptions, opts Options, wg *sync.WaitGroup) {
	defer wg.Done()

	if linkOptions.linkURl == "" {
		return
	}

	parsedURL, err := url.Parse(linkOptions.linkURl)
	if err != nil {
		return
	}

	url := linkOptions.linkURl

	if parsedURL.IsAbs() {
		if !validateLink(linkOptions.linkURl) {
			return
		}
	} else {
		normalizedURL, err := normalizeLink(linkOptions.linkURl, linkOptions.pageURL)
		if err != nil {
			return
		}
		url = normalizedURL.String()
	}

	resp, err := makeRequest(ctx, opts, url, http.MethodHead)
	if err != nil {
		linkAnalyzeOutputs <- LinkAnalizeOutput{
			Url:     url,
			Error:   err,
			pageIdx: linkOptions.pageIdx,
			pageURL: linkOptions.pageURL,
		}
		return
	}

	if resp.StatusCode == 405 {
		resp, err = makeRequest(ctx, opts, url, http.MethodGet)
		if err != nil {
			linkAnalyzeOutputs <- LinkAnalizeOutput{
				Url:     url,
				Error:   err,
				pageIdx: linkOptions.pageIdx,
				pageURL: linkOptions.pageURL,
			}
			return
		}
	}

	if resp.StatusCode != 200 {
		linkAnalyzeOutputs <- LinkAnalizeOutput{
			Url:        url,
			StatusCode: resp.StatusCode,
			pageIdx:    linkOptions.pageIdx,
			pageURL:    linkOptions.pageURL,
		}
		return
	}

	if _, ok := visitedUrls.Get(url); !ok &&
		!parsedURL.IsAbs() &&
		linkOptions.Depth+1 <= opts.Depth {
		visitedUrls.Add(url)
		wg.Add(1)
		analyzePageJobs <- PageOptions{
			PageURL: url,
			Depth:   linkOptions.Depth + 1,
			Idx:     counter.Add(1),
		}
	}
}
func AnalyzePage(ctx context.Context, pageOpts PageOptions, pageAnalyzeOutputs chan<- Page, analyzeLinksJobs chan<- LinkOptions, opts Options, wg *sync.WaitGroup) {
	defer wg.Done()

	// 1. Получаем тело страницы
	resp, err := makeRequest(ctx, opts, pageOpts.PageURL, http.MethodGet)

	if err != nil {
		pageAnalyzeOutputs <- Page{
			BrokenLinks: []BrokenLink{},
			URL:         pageOpts.PageURL,
			Depth:       pageOpts.Depth,
			Error:       err,
		}
		return
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	// 2. Получаем SEO-данные
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		pageAnalyzeOutputs <- Page{
			BrokenLinks: []BrokenLink{},
			URL:         pageOpts.PageURL,
			Depth:       pageOpts.Depth,
			Error:       err,
		}
		return
	}

	seoData := Seo{}

	if doc.Find("h1").Length() > 0 {
		seoData.HasH1 = true
	}
	if doc.Find("title").Length() > 0 {
		seoData.HasTitle = true
		seoData.Title = doc.Find("title").Text()
	}
	if doc.Find(`meta[name="description"]`).Length() > 0 {
		seoData.HasDescription = true
		seoData.Description = doc.Find(`meta[name="description"]`).AttrOr("content", "")
	}

	// 3. Ищем битые и рабочие ссылки
	doc.Find("a").
		Each(func(i int, s *goquery.Selection) {
			linkURL := s.AttrOr("href", "")
			wg.Add(1)

			analyzeLinksJobs <- LinkOptions{
				pageURL: pageOpts.PageURL,
				pageIdx: pageOpts.Idx,
				linkURl: linkURL,
				Depth:   pageOpts.Depth,
			}
		})

		// 4. Формируем отчет

	pageAnalyzeOutputs <- Page{
		URL:         pageOpts.PageURL,
		Depth:       pageOpts.Depth,
		HTTPStatus:  resp.StatusCode,
		Status:      resp.Status,
		Error:       nil,
		BrokenLinks: []BrokenLink{},
		Seo:         seoData,
	}
}

func finishAnalyze(opts Options, pages map[string]Page, brokenLinks []LinkAnalizeOutput) AnalyzeOutput {
	for _, link := range brokenLinks {
		currentPage, ok := pages[link.pageURL]

		if !ok {
			continue
		}

		currentPage.BrokenLinks = append(
			currentPage.BrokenLinks, BrokenLink{
				Url:        link.Url,
				StatusCode: link.StatusCode,
				Error:      link.Error,
			},
		)

		pages[link.pageURL] = currentPage
	}

	output := AnalyzeOutput{
		RootURL:     opts.URL,
		Depth:       opts.Depth,
		GeneratedAt: time.Now().Format(time.RFC3339),
		Pages:       slices.Collect(maps.Values(pages)),
	}

	return output
}

func worker(
	ctx context.Context,
	analyzePageJobs chan PageOptions,
	pageAnalyzeOutputs chan Page,
	analyzeLinksJobs chan LinkOptions,
	linkAnalyzeOutputs chan LinkAnalizeOutput,
	opts Options,
	wg *sync.WaitGroup) {
	for {
		select {
		case pageOpts := <-analyzePageJobs:
			AnalyzePage(ctx, pageOpts, pageAnalyzeOutputs, analyzeLinksJobs, opts, wg)
		case linkOpts := <-analyzeLinksJobs:
			AnalyzeLink(ctx, linkOpts, linkAnalyzeOutputs, analyzePageJobs, opts, wg)
		}
	}
}
func Analyze(ctx context.Context, opts Options) AnalyzeOutput {
	visitedUrls = VisitedURLs{
		mu: sync.RWMutex{},
		m:  make(map[string]struct{}),
	}

	var wg sync.WaitGroup

	done := make(chan struct{})

	analyzePageJobs := make(chan PageOptions)
	analyzeLinksJobs := make(chan LinkOptions)

	pageAnalyzeOutputs := make(chan Page)
	linkAnalyzeOutputs := make(chan LinkAnalizeOutput)

	go func() {
		for w := 0; w <= opts.Concurrency; w++ {
			go worker(ctx, analyzePageJobs, pageAnalyzeOutputs, analyzeLinksJobs, linkAnalyzeOutputs, opts, &wg)
		}
	}()

	visitedUrls.Add(opts.URL)
	wg.Add(1)
	analyzePageJobs <- PageOptions{
		PageURL: opts.URL,
		Depth:   0,
		Idx:     counter.Add(1),
	}

	go func() {
		wg.Wait()
		close(done)
	}()

	pages := map[string]Page{}
	brokenLinks := []LinkAnalizeOutput{}

	for {
		select {
		case data := <-pageAnalyzeOutputs:
			pages[data.URL] = data
		case link := <-linkAnalyzeOutputs:
			brokenLinks = append(brokenLinks, link)
		case <-done:
			return finishAnalyze(opts, pages, brokenLinks)
		case <-ctx.Done():
			return finishAnalyze(opts, pages, brokenLinks)
		}
	}

}

func (output AnalyzeOutput) Format() []byte {
	fmtOutput, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		panic("ошибка форматирования результата")
	}
	return fmtOutput
}
