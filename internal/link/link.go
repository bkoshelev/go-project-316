package link

import (
	"context"
	"errors"
	"io"
	"mime"
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
}

type LinkAnalyzeOutput struct {
	URL         string
	StatusCode  int
	CustomError error
}

type PageOptions struct {
	PageURL string
	Depth   int
}

type LinkAnalyzeResult struct {
	URL                string
	PageURL            string
	LinkAnalyzeOutput  LinkAnalyzeOutput
	AssetAnalyzeOutput AssetAnalyzeOutput
	PageOptions        page.PageOptions
	IsBrokenLink       bool
	IsPage             bool
	IsUnsupportedLink  bool
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

const (
	Script = "text/javascript"
	Style  = "text/css"
	Image  = "image/"
	Page   = "text/html"
	XML    = "text/xml"
	AppXML = "application/xml"
)

func AnalyzeLink(
	ctx context.Context,
	linkOptions LinkOptions,
	httpFetcher fetcher.HTTPFetch,
) LinkAnalyzeResult {
	ctx, cancel := context.WithTimeout(ctx, httpFetcher.Timeout)
	defer cancel()

	linkURL := linkOptions.LinkURL.String()
	pageURL := linkOptions.PageURL.String()

	resp, err := httpFetcher.MakeRequest(ctx, linkURL, http.MethodHead)
	if err != nil {
		return LinkAnalyzeResult{
			URL:          linkURL,
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

	if resp.StatusCode == http.StatusMethodNotAllowed || resp.ContentLength == -1 {
		resp, err = httpFetcher.MakeRequest(ctx, linkURL, http.MethodGet)
		if err != nil {
			return LinkAnalyzeResult{
				URL:          linkURL,
				PageURL:      pageURL,
				IsBrokenLink: true,
				LinkAnalyzeOutput: LinkAnalyzeOutput{
					URL:         linkURL,
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
		return LinkAnalyzeResult{
			URL:          linkURL,
			PageURL:      pageURL,
			IsBrokenLink: true,
			LinkAnalyzeOutput: LinkAnalyzeOutput{
				URL:         linkURL,
				StatusCode:  resp.StatusCode,
				CustomError: errors.New(http.StatusText(resp.StatusCode)),
			}}
	}

	contentType := resp.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)

	if mediaType == Page ||
		mediaType == XML ||
		mediaType == AppXML {
		return LinkAnalyzeResult{
			URL:            linkURL,
			IsPage:         true,
			IsExternalHost: linkOptions.LinkURL.Host != linkOptions.PageURL.Host,
			PageOptions: page.PageOptions{
				Depth:   linkOptions.Depth + 1,
				PageURL: linkURL,
			},
		}
	}

	var size int64
	assetType := ""

	switch {
	case mediaType == Style:
		assetType = "style"
	case mediaType == Script:
		assetType = "script"
	case strings.HasPrefix(mediaType, Image):
		assetType = "image"
	case err != nil:
		assetType = "other"
	default:
		assetType = "other"
	}

	if resp.ContentLength != -1 {
		size = resp.ContentLength
	} else {
		size, err = io.Copy(io.Discard, resp.Body)
		if err != nil {
			return LinkAnalyzeResult{
				PageURL: pageURL,
				URL:     linkURL,
				IsAsset: true,
				AssetAnalyzeOutput: AssetAnalyzeOutput{
					URL:         linkURL,
					StatusCode:  resp.StatusCode,
					Type:        assetType,
					CustomError: err,
				},
			}
		}
	}

	return LinkAnalyzeResult{
		URL:     linkURL,
		PageURL: pageURL,
		IsAsset: true,
		AssetAnalyzeOutput: AssetAnalyzeOutput{
			URL:        linkURL,
			StatusCode: resp.StatusCode,
			Type:       assetType,
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
