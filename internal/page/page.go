package page

import (
	"code/internal/fetcher"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type BrokenLink struct {
	URL         string `json:"url" binding:"required"`
	StatusCode  int    `json:"status_code" binding:"required"`
	CustomError error  `json:"-"`
}

// https://medium.com/picus-security-engineering/custom-json-marshaller-in-go-and-common-pitfalls-c43fa774db05
func (f *BrokenLink) MarshalJSON() ([]byte, error) {
	type Alias BrokenLink

	if f.CustomError == nil {
		return json.Marshal(&struct {
			*Alias
		}{
			Alias: (*Alias)(f),
		})
	}

	return json.Marshal(&struct {
		Error string `json:"error"`
		*Alias
	}{
		Error: f.CustomError.Error(),
		Alias: (*Alias)(f),
	})
}

type Page struct {
	URL          string       `json:"url" binding:"required"`
	Depth        int          `json:"depth" binding:"required"`
	HTTPStatus   int          `json:"http_status" binding:"required"`
	Status       string       `json:"status" binding:"required"`
	CustomError  error        `json:"-"`
	BrokenLinks  []BrokenLink `json:"broken_links" binding:"required"`
	SEO          SEO          `json:"seo" binding:"required"`
	Assets       []Asset      `json:"assets" binding:"required"`
	DiscoveredAt string       `json:"discovered_at" binding:"required"`
}

func (f *Page) MarshalJSON() ([]byte, error) {
	type Alias Page

	if f.CustomError == nil {
		return json.Marshal(&struct {
			*Alias
		}{
			Alias: (*Alias)(f),
		})
	}
	return json.Marshal(&struct {
		Error string `json:"error"`
		*Alias
	}{
		Error: f.CustomError.Error(),
		Alias: (*Alias)(f),
	})
}

type SEO struct {
	HasTitle       bool   `json:"has_title" binding:"required"`
	Title          string `json:"title" binding:"required"`
	HasDescription bool   `json:"has_description" binding:"required"`
	Description    string `json:"description" binding:"required"`
	HasH1          bool   `json:"has_h1" binding:"required"`
}

type LinkOptions struct {
	PageURL *url.URL
	LinkURL string
	Depth   int
	Type    string
}

type PageOptions struct {
	PageURL *url.URL
	Depth   int
}

type PageResult struct {
	IsUnsupportedPage bool
	PageOutput        Page
	Links             []LinkOptions
}

type Asset struct {
	URL         string `json:"url" binding:"required"`
	Type        string `json:"type" binding:"required"`
	StatusCode  int    `json:"status_code" binding:"required"`
	SizeBytes   int64  `json:"size_bytes" binding:"required"`
	CustomError error  `json:"-"`
}

func (f *Asset) MarshalJSON() ([]byte, error) {
	type Alias Asset

	if f.CustomError == nil {
		return json.Marshal(&struct {
			*Alias
		}{
			Alias: (*Alias)(f),
		})
	}
	return json.Marshal(&struct {
		Error string `json:"error"`
		*Alias
	}{
		Error: f.CustomError.Error(),
		Alias: (*Alias)(f),
	})
}

const (
	PageLinkType   = "page"
	StyleLinkType  = "style"
	ScriptLinkType = "script"
	ImgLinkType    = "image"
	OtherLinkType  = "other"
)

func AnalyzePage(ctx context.Context,
	pageOpts PageOptions,
	httpFetcher fetcher.HTTPFetch,
) PageResult {
	ctx, cancel := context.WithTimeout(ctx, httpFetcher.Timeout)
	defer cancel()

	// 1. Получаем тело страницы
	resp, err := httpFetcher.MakeRequest(ctx, pageOpts.PageURL.String(), http.MethodGet)
	isNetworkError := fetcher.IsNetworkError(err)
	isServerError := resp != nil && resp.StatusCode >= http.StatusBadRequest

	if err != nil && isNetworkError {
		return PageResult{
			PageOutput: Page{
				URL:          pageOpts.PageURL.String(),
				Depth:        pageOpts.Depth,
				CustomError:  err,
				DiscoveredAt: time.Now().UTC().Format(time.RFC3339),
				Status:       "error",
			}}
	}
	if err != nil && !isNetworkError {
		return PageResult{
			IsUnsupportedPage: true,
		}
	}
	if isServerError {
		return PageResult{
			PageOutput: Page{
				URL:          pageOpts.PageURL.String(),
				Depth:        pageOpts.Depth,
				CustomError:  err,
				DiscoveredAt: time.Now().UTC().Format(time.RFC3339),
				Status:       "error",
				HTTPStatus:   resp.StatusCode,
			}}
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			panic("не удалось закрыть тело ответа")
		}
	}()

	// 2. Получаем SEO-данные
	doc, err := goquery.NewDocumentFromReader(resp.Body)

	if err != nil {
		return PageResult{
			PageOutput: Page{
				URL:          pageOpts.PageURL.String(),
				Depth:        pageOpts.Depth,
				CustomError:  err,
				DiscoveredAt: time.Now().UTC().Format(time.RFC3339),
				Status:       "error",
			}}
	}

	SEOData := SEO{}

	if doc.Find("h1").Length() > 0 {
		SEOData.HasH1 = true
	}
	if doc.Find("title").Length() > 0 {
		SEOData.HasTitle = true
		SEOData.Title = doc.Find("title").First().Text()
	}
	if doc.Find(`meta[name="description"]`).Length() > 0 {
		SEOData.HasDescription = true
		SEOData.Description = doc.Find(`meta[name="description"]`).First().AttrOr("content", "")
	}

	links := []LinkOptions{}

	// 3. Ищем битые и рабочие ссылки
	doc.Find("a[href]").Each(func(i int, s *goquery.Selection) {
		rawURL, ok := s.Attr("href")
		if !ok {
			return
		}

		links = append(links, LinkOptions{
			PageURL: pageOpts.PageURL,
			LinkURL: rawURL,
			Depth:   pageOpts.Depth,
			Type:    PageLinkType,
		})
	})

	doc.Find("img[src]").Each(func(i int, s *goquery.Selection) {
		rawURL, ok := s.Attr("src")
		if !ok {
			return
		}

		links = append(links, LinkOptions{
			PageURL: pageOpts.PageURL,
			LinkURL: rawURL,
			Depth:   pageOpts.Depth,
			Type:    ImgLinkType,
		})
	})

	doc.Find("script[src]").Each(func(i int, s *goquery.Selection) {
		rawURL, ok := s.Attr("src")
		if !ok {
			return
		}

		links = append(links, LinkOptions{
			PageURL: pageOpts.PageURL,
			LinkURL: rawURL,
			Depth:   pageOpts.Depth,
			Type:    ScriptLinkType,
		})
	})

	doc.Find(`link[rel="stylesheet"][href]`).Each(func(i int, s *goquery.Selection) {
		rawURL, ok := s.Attr("href")
		if !ok {
			return
		}

		links = append(links, LinkOptions{
			PageURL: pageOpts.PageURL,
			LinkURL: rawURL,
			Depth:   pageOpts.Depth,
			Type:    StyleLinkType,
		})
	})

	doc.Find("audio[src] video[src]").Each(func(i int, s *goquery.Selection) {
		rawURL, ok := s.Attr("src")
		if !ok {
			return
		}

		links = append(links, LinkOptions{
			PageURL: pageOpts.PageURL,
			LinkURL: rawURL,
			Depth:   pageOpts.Depth,
			Type:    OtherLinkType,
		})
	})

	// 4. Формируем отчет
	return PageResult{
		PageOutput: Page{
			URL:          pageOpts.PageURL.String(),
			Depth:        pageOpts.Depth,
			HTTPStatus:   resp.StatusCode,
			Status:       "ok",
			SEO:          SEOData,
			DiscoveredAt: time.Now().UTC().Format(time.RFC3339),
			BrokenLinks:  []BrokenLink{},
			Assets:       []Asset{},
		},
		Links: links,
	}
}
