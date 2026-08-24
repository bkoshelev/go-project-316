package httpclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Options struct {
	UserAgent    string
	HTTPClient   HTTPClient
	WaitPauseCh  <-chan struct{}
	StartPauseCh chan<- struct{}
	Timeout      time.Duration
	Retries      int
}

type HTTPFetch struct {
	UserAgent    string
	HTTPClient   HTTPClient
	WaitPauseCh  <-chan struct{}
	StartPauseCh chan<- struct{}
	Timeout      time.Duration
	Retries      int
}

func (h HTTPFetch) MakeRequest(ctx context.Context, URL, method string) (*http.Response, error) {
	tryIdx := 0

	for {
		resp, err := func() (*http.Response, error) {
			select {
			case <-h.WaitPauseCh:
			case <-ctx.Done():
				return nil, ctx.Err()
			}

			h.StartPauseCh <- struct{}{}
			defer func() {
				slog.Info("Запрос выполнен")
			}()
			slog.Info("Новый запрос")

			if method != "HEAD" && method != "GET" {
				return nil, errors.New("неверный тип запроса")
			}

			ctx, cancel := context.WithTimeout(ctx, h.Timeout)
			defer cancel()

			req, err := http.NewRequestWithContext(
				ctx,
				method,
				URL,
				nil,
			)

			if err != nil {
				return nil, fmt.Errorf("ошибка создания запроса: %w", err)
			}

			if h.UserAgent != "" {
				req.Header.Set("User-Agent", h.UserAgent)
			}

			resp, err := h.HTTPClient.Do(req)

			if err != nil {
				return nil, fmt.Errorf("ошибка выполнения запроса: %w", err)
			}

			return resp, nil
		}()

		if (err != nil || (resp.StatusCode >= 500 && resp.StatusCode < 600) || resp.StatusCode == http.StatusTooManyRequests) && tryIdx < h.Retries {
			if resp != nil {
				if err := resp.Body.Close(); err != nil {
					panic("не удалось закрыть тело ответа")
				}
			}
			tryIdx++
			continue
		} else {
			return resp, err
		}
	}

}

func CreateHTTPFetch(options Options) HTTPFetch {
	return HTTPFetch(options)
}
