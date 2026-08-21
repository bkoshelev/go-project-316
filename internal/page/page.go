package page

import (
	httpclient "code/internal/http_client"
	"context"
	"log/slog"
	"net/http"

	"github.com/PuerkitoBio/goquery"
)

type BrokenLink struct {
	URL        string `json:"url" binding:"required"`
	StatusCode int    `json:"status_code" binding:"required"`
	Error      error  `json:"error" binding:"required"`
}

type Page struct {
	URL         string       `json:"url" binding:"required"`
	Depth       int          `json:"depth" binding:"required"`
	HTTPStatus  int          `json:"http_status" binding:"required"`
	Status      string       `json:"status" binding:"required"`
	Error       error        `json:"error" binding:"required"`
	BrokenLinks []BrokenLink `json:"broken_links" binding:"required"`
	SEO         SEO          `json:"seo" binding:"required"`
	Assets      []Asset      `json:"assets" binding:"required"`
}

type SEO struct {
	HasTitle       bool   `json:"has_title" binding:"required"`
	Title          string `json:"title" binding:"required"`
	HasDescription bool   `json:"has_description" binding:"required"`
	Description    string `json:"description" binding:"required"`
	HasH1          bool   `json:"has_h1" binding:"required"`
}

type LinkOptions struct {
	PageURL string
	LinkURL string
	Depth   int
}

type PageOptions struct {
	PageURL string
	Depth   int
}

type PageResult struct {
	PageOutput Page
	Links      []LinkOptions
}

type Asset struct {
	URL        string `json:"url" binding:"required"`
	Type       string `json:"type" binding:"required"`
	StatusCode int    `json:"status_code" binding:"required"`
	SizeBytes  int64  `json:"size_bytes" binding:"required"`
	Error      error  `json:"error" binding:"required"`
}

func AnalyzePage(ctx context.Context,
	pageOpts PageOptions,
	httpFetcher httpclient.HTTPFetch,
) PageResult {
	// 1. Получаем тело страницы
	resp, err := httpFetcher.MakeRequest(ctx, pageOpts.PageURL, http.MethodGet)

	if err != nil {
		return PageResult{
			PageOutput: Page{
				BrokenLinks: []BrokenLink{},
				URL:         pageOpts.PageURL,
				Depth:       pageOpts.Depth,
				Error:       err,
			}}
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return PageResult{
			PageOutput: Page{
				BrokenLinks: []BrokenLink{},
				URL:         pageOpts.PageURL,
				Depth:       pageOpts.Depth,
				Error:       err,
			}}
	}

	// 2. Получаем SEO-данные
	SEOData := SEO{}

	if doc.Find("h1").Length() > 0 {
		SEOData.HasH1 = true
	}
	if doc.Find("title").Length() > 0 {
		SEOData.HasTitle = true
		SEOData.Title = doc.Find("title").Text()
	}
	if doc.Find(`meta[name="description"]`).Length() > 0 {
		SEOData.HasDescription = true
		SEOData.Description = doc.Find(`meta[name="description"]`).AttrOr("content", "")
	}

	links := []LinkOptions{}

	// 3. Ищем битые и рабочие ссылки
	doc.Find("a").
		Each(func(i int, s *goquery.Selection) {
			LinkURL := s.AttrOr("href", "")

			links = append(links, LinkOptions{
				PageURL: pageOpts.PageURL,
				LinkURL: LinkURL,
				Depth:   pageOpts.Depth,
			})
		})
	slog.Info("Анализ страницы завершен - 1")

	// 4. Формируем отчет
	return PageResult{
		PageOutput: Page{
			URL:         pageOpts.PageURL,
			Depth:       pageOpts.Depth,
			HTTPStatus:  resp.StatusCode,
			Status:      resp.Status,
			Error:       nil,
			BrokenLinks: []BrokenLink{},
			SEO:         SEOData,
		},
		Links: links,
	}
}
