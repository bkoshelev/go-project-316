package link

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"code/internal/fetcher"
	"code/internal/page"
)

type LinkOptions struct {
	PageURL *url.URL
	LinkURL *url.URL
	Depth   int
	Type    string
}

type LinkAnalyzeOutput struct {
	URL         string
	StatusCode  int
	CustomError error
}

type PageOptions struct {
	PageURL *url.URL
	Depth   int
}

type LinkAnalyzeResult struct {
	URL                *url.URL
	PageURL            string
	LinkAnalyzeOutput  LinkAnalyzeOutput
	AssetAnalyzeOutput AssetAnalyzeOutput
	PageOptions        page.PageOptions
	IsBrokenLink       bool
	IsPage             bool
	IsExternalHost     bool
	IsAsset            bool
}

type AssetAnalyzeOutput struct {
	URL         string
	Type        string
	StatusCode  int
	SizeBytes   int64
	CustomError error
}

func AnalyzeLink(
	ctx context.Context,
	linkOptions LinkOptions,
	httpFetcher fetcher.HTTPFetch,
) LinkAnalyzeResult {
	ctx, cancel := context.WithTimeout(ctx, httpFetcher.Timeout)
	defer cancel()

	pageURL := linkOptions.PageURL.String()
	linkURL := linkOptions.LinkURL.String()

	resp, err := httpFetcher.MakeRequest(ctx, linkURL, http.MethodHead)
	if err != nil {
		return LinkAnalyzeResult{
			URL:          linkOptions.LinkURL,
			PageURL:      pageURL,
			IsBrokenLink: true,
			LinkAnalyzeOutput: LinkAnalyzeOutput{
				URL:         linkURL,
				CustomError: err,
			}}

	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			panic("не удалось закрыть тело ответа")
		}
	}()

	bodySizeReturned := resp.ContentLength != fetcher.BodySizeIsUnknown

	if resp.StatusCode == http.StatusMethodNotAllowed || !bodySizeReturned {
		resp, err := httpFetcher.MakeRequest(ctx, linkURL, http.MethodGet)
		if err != nil {
			if linkOptions.Type == "page" {
				return LinkAnalyzeResult{
					URL:          linkOptions.LinkURL,
					PageURL:      pageURL,
					IsBrokenLink: true,
					LinkAnalyzeOutput: LinkAnalyzeOutput{
						URL:         linkURL,
						CustomError: err,
					},
				}
			}

			return LinkAnalyzeResult{
				URL:     linkOptions.LinkURL,
				PageURL: pageURL,
				IsAsset: true,
				AssetAnalyzeOutput: AssetAnalyzeOutput{
					URL:         linkURL,
					Type:        linkOptions.Type,
					CustomError: err,
				},
			}
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				panic("не удалось закрыть тело ответа")
			}
		}()
	}

	if resp.StatusCode >= http.StatusBadRequest {
		if linkOptions.Type == "page" {
			return LinkAnalyzeResult{
				URL:          linkOptions.LinkURL,
				PageURL:      pageURL,
				IsBrokenLink: true,
				LinkAnalyzeOutput: LinkAnalyzeOutput{
					URL:         linkURL,
					StatusCode:  resp.StatusCode,
					CustomError: errors.New(http.StatusText(resp.StatusCode)),
				}}
		}

		return LinkAnalyzeResult{
			URL:     linkOptions.LinkURL,
			PageURL: pageURL,
			IsAsset: true,
			AssetAnalyzeOutput: AssetAnalyzeOutput{
				URL:         linkURL,
				Type:        linkOptions.Type,
				StatusCode:  resp.StatusCode,
				CustomError: errors.New(http.StatusText(resp.StatusCode)),
			},
		}
	}

	if linkOptions.Type == "page" {
		return LinkAnalyzeResult{
			URL:            linkOptions.LinkURL,
			IsPage:         true,
			IsExternalHost: linkOptions.LinkURL.Host != linkOptions.PageURL.Host,
			PageOptions: page.PageOptions{
				Depth:   linkOptions.Depth + 1,
				PageURL: linkOptions.LinkURL,
			},
		}
	}

	var size int64
	bodySizeReturned = resp.ContentLength != fetcher.BodySizeIsUnknown

	if bodySizeReturned {
		size = resp.ContentLength
	} else {
		size, err = io.Copy(io.Discard, resp.Body)
		if err != nil {
			return LinkAnalyzeResult{
				PageURL: pageURL,
				URL:     linkOptions.LinkURL,
				IsAsset: true,
				AssetAnalyzeOutput: AssetAnalyzeOutput{
					URL:         linkURL,
					StatusCode:  resp.StatusCode,
					Type:        linkOptions.Type,
					CustomError: err,
				},
			}
		}
	}

	return LinkAnalyzeResult{
		URL:     linkOptions.LinkURL,
		PageURL: pageURL,
		IsAsset: true,
		AssetAnalyzeOutput: AssetAnalyzeOutput{
			URL:        linkURL,
			StatusCode: resp.StatusCode,
			Type:       linkOptions.Type,
			SizeBytes:  size,
		},
	}
}

func NormalizeLink(rawLink, pageURL string) (*url.URL, error) {
	link, err := url.Parse(rawLink)
	if err != nil {
		return nil, err
	}

	base, err := url.Parse(pageURL)
	if err != nil {
		return nil, err
	}

	normalized := base.ResolveReference(link)

	if normalized.Path == "/" {
		normalized.Path = ""
	}

	return normalized, nil
}

func ValidateLink(link *url.URL) bool {
	if link.String() == "" || strings.HasPrefix(link.String(), "#") {
		return false
	}

	if link.IsAbs() {
		if !strings.EqualFold(link.Scheme, "http") && !strings.EqualFold(link.Scheme, "https") {
			return false
		}

	}

	return true
}
