package link

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	httpclient "code/internal/http_client"
)

type LinkOptions struct {
	PageURL string
	LinkURL string
	Depth   int
}

type LinkAnalyzeOutput struct {
	URL        string
	PageIdx    int64
	StatusCode int
	Error      error
	PageURL    string
}

type PageOptions struct {
	PageURL string
	Depth   int
}

type LinkAnalyzeResult struct {
	LinkAnalyzeOutput LinkAnalyzeOutput
	IsBrokenLink      bool
	IsPage            bool
	IsUnsupportedLink bool
	IsAbs             bool
	Depth             int
}

func AnalyzeLink(
	ctx context.Context,
	linkOptions LinkOptions,
	httpFetcher httpclient.HTTPFetch,
) LinkAnalyzeResult {
	if linkOptions.LinkURL == "" {
		return LinkAnalyzeResult{IsUnsupportedLink: true}
	}

	parsedURL, err := url.Parse(linkOptions.LinkURL)
	if err != nil {
		return LinkAnalyzeResult{IsUnsupportedLink: true}

	}

	URL := linkOptions.LinkURL

	if !parsedURL.IsAbs() {
		normalizedURL, err := normalizeLink(linkOptions.LinkURL, linkOptions.PageURL)
		if err != nil {
			return LinkAnalyzeResult{IsUnsupportedLink: true}
		}

		URL = normalizedURL.String()
	}

	if !ValidateLink(URL) {
		return LinkAnalyzeResult{IsUnsupportedLink: true}
	}

	resp, err := httpFetcher.MakeRequest(ctx, URL, http.MethodHead)
	if err != nil {
		return LinkAnalyzeResult{
			IsBrokenLink: true,
			LinkAnalyzeOutput: LinkAnalyzeOutput{
				URL:     URL,
				Error:   err,
				PageURL: linkOptions.PageURL,
			}}

	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == 405 {
		resp, err = httpFetcher.MakeRequest(ctx, URL, http.MethodGet)
		if err != nil {
			return LinkAnalyzeResult{
				IsBrokenLink: true,
				LinkAnalyzeOutput: LinkAnalyzeOutput{
					URL:     URL,
					Error:   err,
					PageURL: linkOptions.PageURL,
				},
			}
		}
		defer func() {
			_ = resp.Body.Close()
		}()
	}

	if resp.StatusCode != 200 {
		return LinkAnalyzeResult{
			IsBrokenLink: true,
			LinkAnalyzeOutput: LinkAnalyzeOutput{
				URL:        URL,
				StatusCode: resp.StatusCode,
				PageURL:    linkOptions.PageURL,
			}}
	}

	return LinkAnalyzeResult{
		IsPage: true,
		IsAbs:  parsedURL.IsAbs(),
		Depth:  linkOptions.Depth,
		LinkAnalyzeOutput: LinkAnalyzeOutput{
			URL: URL,
		},
	}
}

func normalizeLink(rawLink, pageURL string) (*url.URL, error) {
	link, err := url.Parse(rawLink)
	if err != nil {
		return nil, err
	}

	base, err := url.Parse(pageURL)
	if err != nil {
		return nil, err
	}

	normalized := base.ResolveReference(link)

	return normalized, nil
}

func ValidateLink(link string) bool {
	if link == "" || strings.HasPrefix(link, "#") {
		return false
	}

	parsedLink, err := url.Parse(link)
	if err != nil {
		return false
	}

	if !strings.EqualFold(parsedLink.Scheme, "http") && !strings.EqualFold(parsedLink.Scheme, "https") {
		return false
	}

	return true
}
