package crawler

import (
	"net/url"
	"strings"
)

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
	normalized.Fragment = ""
	if normalized.Host == "" || !isHTTPScheme(normalized.Scheme) {
		return nil, err
	}

	return normalized, nil
}

func isHTTPScheme(scheme string) bool {
	return strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https")
}

func validateLink(rawLink string) bool {
	candidate := rawLink
	if candidate == "" || strings.HasPrefix(candidate, "#") {
		return false
	}

	link, err := url.Parse(candidate)
	if err != nil {
		return false
	}

	if link.Scheme != "" && !isHTTPScheme(link.Scheme) {
		return false
	}

	return true
}
