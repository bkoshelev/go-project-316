package crawler

import (
	"bytes"
	"code/internal/page"
	"context"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
)

func assertError(t testing.TB, want, got error) {
	t.Helper()

	if want == nil {
		assert.NoError(t, got)
		return
	}

	if !assert.Error(t, got) {
		return
	}

	if errors.Is(got, want) {
		assert.ErrorIs(t, got, want)
		return
	}

	assert.ErrorContains(t, got, want.Error())
}

func AssertBrokenLinks(t testing.TB, want, got []page.BrokenLink) {
	t.Helper()

	gotByURL := make(map[string]page.BrokenLink, len(got))
	for _, link := range got {
		gotByURL[link.URL] = link
	}

	for _, wantLink := range want {
		gotLink, exists := gotByURL[wantLink.URL]
		assert.Truef(t, exists, "broken link is missing: %s", wantLink.URL)
		assert.Equal(t, wantLink.StatusCode, gotLink.StatusCode)
		assertError(t, wantLink.Error, gotLink.Error)
	}
}

func AssertAssets(t testing.TB, want, got []page.Asset) {
	t.Helper()

	gotByURL := make(map[string]page.Asset, len(got))
	for _, asset := range got {
		gotByURL[asset.URL] = asset
	}

	for _, wantAsset := range want {
		gotAsset, exists := gotByURL[wantAsset.URL]
		assert.Truef(t, exists, "asset is missing: %s", wantAsset.URL)
		assert.Equal(t, wantAsset.StatusCode, gotAsset.StatusCode)
		assert.Equal(t, wantAsset.Type, gotAsset.Type)
		assert.Equal(t, wantAsset.SizeBytes, gotAsset.SizeBytes)
		assertError(t, wantAsset.Error, gotAsset.Error)
	}
}

func AssertAnalyzeOutput(t testing.TB, want, got AnalyzeOutput) {
	t.Helper()
	assert.Equal(t, want.Depth, got.Depth)
	assert.Equal(t, want.RootURL, got.RootURL)

	gotByURL := make(map[string]page.Page, len(got.Pages))
	for _, page := range got.Pages {
		gotByURL[page.URL] = page
	}

	for _, wantPage := range want.Pages {
		gotPage, exists := gotByURL[wantPage.URL]
		assert.Truef(t, exists, "broken link is missing: %s", wantPage.URL)

		assert.Equal(t, wantPage.Depth, gotPage.Depth)
		assert.Equal(t, wantPage.HTTPStatus, gotPage.HTTPStatus)
		assert.Equal(t, wantPage.Status, gotPage.Status)
		assert.Equal(t, wantPage.SEO, gotPage.SEO)
		assertError(t, wantPage.Error, gotPage.Error)
		AssertBrokenLinks(t, wantPage.BrokenLinks, gotPage.BrokenLinks)
		AssertAssets(t, wantPage.Assets, gotPage.Assets)
	}
}

type createHTTPClient func() (*httptest.Server, *http.Client)
type createWant func(URL string) AnalyzeOutput
type createOptions func(server *httptest.Server, client *http.Client) Options

func TestCrawler_Subtests(t *testing.T) {
	cases := []struct {
		name             string
		createHTTPClient createHTTPClient
		createOptions    createOptions
		createWant       createWant
	}{
		{
			"успешный ответ",
			func() (*httptest.Server, *http.Client) {
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				return server, server.Client()
			},
			createOptions(func(server *httptest.Server, client *http.Client) Options {
				return Options{
					URL:         server.URL,
					HTTPClient:  server.Client(),
					Depth:       0,
					Concurrency: 3,
					Timeout:     time.Second * 15,
				}
			}),
			func(URL string) AnalyzeOutput {
				return AnalyzeOutput{
					RootURL: URL,
					Depth:   0,
					Pages: []page.Page{
						{
							URL:         URL,
							Depth:       0,
							HTTPStatus:  200,
							Status:      "200 OK",
							Error:       nil,
							BrokenLinks: []page.BrokenLink{},
						},
					},
				}
			},
		},
		{
			"вернулся код ошибки",
			func() (*httptest.Server, *http.Client) {
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "bad request", http.StatusBadRequest)
				}))
				return server, server.Client()
			},
			createOptions(func(server *httptest.Server, client *http.Client) Options {
				return Options{
					URL:         server.URL,
					HTTPClient:  server.Client(),
					Depth:       0,
					Concurrency: 3,
					Timeout:     time.Second * 15,
				}
			}),
			createWant(func(URL string) AnalyzeOutput {
				return AnalyzeOutput{
					RootURL: URL,
					Depth:   0,
					Pages: []page.Page{
						{
							URL:         URL,
							Depth:       0,
							HTTPStatus:  400,
							Status:      "400 Bad Request",
							BrokenLinks: []page.BrokenLink{},
						},
					},
				}
			}),
		},
		{
			"истекло время ожидания ответа",
			func() (*httptest.Server, *http.Client) {
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					time.Sleep(200 * time.Millisecond)
				}))
				client := server.Client()
				return server, client
			},
			createOptions(func(server *httptest.Server, client *http.Client) Options {
				return Options{
					URL:         server.URL,
					HTTPClient:  client,
					Depth:       0,
					Concurrency: 3,
					Timeout:     time.Millisecond * 100,
				}
			}),
			createWant(func(URL string) AnalyzeOutput {
				return AnalyzeOutput{
					RootURL: URL,
					Depth:   0,
					Pages: []page.Page{
						{
							URL:         URL,
							Depth:       0,
							Error:       context.DeadlineExceeded,
							BrokenLinks: []page.BrokenLink{},
						},
					},
				}
			}),
		},
		{
			"сетевая ошибка",
			func() (*httptest.Server, *http.Client) {
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				server.Close()

				return server, server.Client()
			},
			createOptions(func(server *httptest.Server, client *http.Client) Options {
				return Options{
					URL:         server.URL,
					HTTPClient:  server.Client(),
					Depth:       0,
					Concurrency: 3,
					Timeout:     time.Second * 15,
				}
			}),
			createWant(func(URL string) AnalyzeOutput {

				return AnalyzeOutput{
					RootURL: URL,
					Depth:   0,
					Pages: []page.Page{
						{
							URL:         URL,
							Depth:       0,
							Error:       syscall.ECONNREFUSED,
							BrokenLinks: []page.BrokenLink{},
						},
					},
				}
			}),
		},
		{
			"проверка на наличие битых ссылок",
			func() (*httptest.Server, *http.Client) {
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/", "/about":
						tmpl, err := template.ParseFiles("../mocks/index.html")

						if err != nil {
							panic("ошибка чтения html-файла")
						}

						err = tmpl.Execute(w, map[string]string{
							"BaseURL": r.Host,
						})

						if err != nil {
							panic("ошибка создания тела ответа")
						}
					case "/contacts":
						http.NotFound(w, r)
					default:
						http.NotFound(w, r)
					}
				}))

				return server, server.Client()
			},
			createOptions(func(server *httptest.Server, client *http.Client) Options {
				return Options{
					URL:         server.URL,
					HTTPClient:  server.Client(),
					Depth:       0,
					Concurrency: 3,
					Timeout:     time.Second * 15,
				}
			}),
			createWant(func(URL string) AnalyzeOutput {
				parsedURL, err := url.Parse(URL)

				if err != nil {
					panic("ошибка парсинга")
				}

				return AnalyzeOutput{
					RootURL: URL,
					Depth:   0,
					Pages: []page.Page{
						{
							URL:        URL,
							Depth:      0,
							Error:      nil,
							HTTPStatus: 200,
							Status:     "200 OK",
							BrokenLinks: []page.BrokenLink{
								{URL: URL + "/contacts", StatusCode: 404},
								{URL: "https://" + parsedURL.Host + "/app.js", StatusCode: 404},
							},
							SEO: page.SEO{
								HasTitle: true,
								Title:    "Simple Test",
							},
						},
					},
				}
			}),
		},
		{
			"проверка на получение seo-данных",
			func() (*httptest.Server, *http.Client) {
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

					switch r.URL.Path {
					case "/":
						data, err := os.ReadFile("../mocks/contacts.html")
						if err != nil {
							panic("ошибка чтения файла")
						}

						_, err = w.Write(data)
						if err != nil {
							panic("ошибка создания тела ответа")
						}
					default:
						http.NotFound(w, r)
					}
				}))

				return server, server.Client()
			},
			createOptions(func(server *httptest.Server, client *http.Client) Options {
				return Options{
					URL:         server.URL,
					HTTPClient:  server.Client(),
					Depth:       0,
					Concurrency: 3,
					Timeout:     time.Second * 15,
				}
			}),
			createWant(func(URL string) AnalyzeOutput {

				return AnalyzeOutput{
					RootURL: URL,
					Depth:   0,
					Pages: []page.Page{
						{
							URL:        URL,
							Depth:      0,
							Error:      nil,
							HTTPStatus: 200,
							Status:     "200 OK",
							BrokenLinks: []page.BrokenLink{
								{URL: URL + "/about", StatusCode: 404},
							},
							SEO: page.SEO{
								HasTitle:       true,
								Title:          "Contacts",
								HasDescription: true,
								Description:    "Тестовая страница Simple Test & проверка HTML-сущностей",
								HasH1:          true,
							},
						},
					},
				}
			}),
		},
		{
			"проверка на отсутствие seo-данных",
			func() (*httptest.Server, *http.Client) {
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

					switch r.URL.Path {
					case "/":
						data, err := os.ReadFile("../mocks/empty.html")
						if err != nil {
							panic("ошибка чтения файла")
						}

						_, err = w.Write(data)
						if err != nil {
							panic("ошибка создания тела ответа")
						}
					default:
						http.NotFound(w, r)
					}
				}))

				return server, server.Client()
			},
			createOptions(func(server *httptest.Server, client *http.Client) Options {
				return Options{
					URL:         server.URL,
					HTTPClient:  server.Client(),
					Depth:       0,
					Concurrency: 3,
					Timeout:     time.Second * 15,
				}
			}),
			createWant(func(URL string) AnalyzeOutput {

				return AnalyzeOutput{
					RootURL: URL,
					Depth:   0,
					Pages: []page.Page{
						{
							URL:         URL,
							Depth:       0,
							Error:       nil,
							HTTPStatus:  200,
							Status:      "200 OK",
							BrokenLinks: []page.BrokenLink{},
							SEO: page.SEO{
								HasTitle:       false,
								HasDescription: false,
								HasH1:          false,
							},
						},
					},
				}
			}),
		},
		{
			"проверка обхода в глубину (без задержки)",
			func() (*httptest.Server, *http.Client) {
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

					switch r.URL.Path {
					case "/":
						tmpl, err := template.ParseFiles("../mocks/index.html")

						if err != nil {
							panic("ошибка чтения html-файла")
						}

						err = tmpl.Execute(w, map[string]string{
							"BaseURL": r.Host,
						})

						if err != nil {
							panic("ошибка создания тела ответа")
						}
					case "/contacts":
						data, err := os.ReadFile("../mocks/contacts.html")
						if err != nil {
							panic("ошибка чтения файла")
						}

						_, err = w.Write(data)
						if err != nil {
							panic("ошибка создания тела ответа")
						}
					case "/about":
						data, err := os.ReadFile("../mocks/about.html")
						if err != nil {
							panic("ошибка чтения файла")
						}

						_, err = w.Write(data)
						if err != nil {
							panic("ошибка создания тела ответа")
						}
					default:
						http.NotFound(w, r)
					}
				}))

				return server, server.Client()
			},
			createOptions(func(server *httptest.Server, client *http.Client) Options {
				return Options{
					URL:         server.URL,
					HTTPClient:  server.Client(),
					Depth:       2,
					Concurrency: 3,
					Timeout:     time.Second * 15,
				}
			}),
			createWant(func(URL string) AnalyzeOutput {
				parsedURL, err := url.Parse(URL)

				if err != nil {
					panic("ошибка парсинга")
				}

				return AnalyzeOutput{
					RootURL: URL,
					Depth:   2,
					Pages: []page.Page{
						{
							URL:        URL,
							Depth:      0,
							Error:      nil,
							HTTPStatus: 200,
							Status:     "200 OK",
							BrokenLinks: []page.BrokenLink{
								{URL: "https://" + parsedURL.Host + "/app.js", StatusCode: 404},
							},
							SEO: page.SEO{
								HasTitle:       true,
								Title:          "Simple Test",
								HasDescription: false,
								HasH1:          false,
							},
						},
						{
							URL:         URL + "/contacts",
							Depth:       1,
							Error:       nil,
							HTTPStatus:  200,
							Status:      "200 OK",
							BrokenLinks: []page.BrokenLink{},
							SEO: page.SEO{
								HasTitle:       true,
								Title:          "Contacts",
								HasDescription: true,
								Description:    "Тестовая страница Simple Test & проверка HTML-сущностей",
								HasH1:          true,
							},
						},
						{
							URL:         URL + "/about",
							Depth:       1,
							Error:       nil,
							HTTPStatus:  200,
							Status:      "200 OK",
							BrokenLinks: []page.BrokenLink{},
							SEO: page.SEO{
								HasTitle:       true,
								Title:          "О сайте",
								HasDescription: false,
								HasH1:          false,
							},
						},
					},
				}
			}),
		},
		{
			"проверяем что retry работает",
			func() (*httptest.Server, *http.Client) {
				tryIdx := 0
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/":
						if tryIdx < 2 {
							http.Error(w, "временная ошибка сервера", http.StatusInternalServerError)
							tryIdx++
						} else {
							data, err := os.ReadFile("../mocks/empty.html")
							if err != nil {
								panic("ошибка чтения файла")
							}

							_, err = w.Write(data)
							if err != nil {
								panic("ошибка создания тела ответа")
							}
						}
					default:
						http.NotFound(w, r)
					}
				}))

				return server, server.Client()
			},
			createOptions(func(server *httptest.Server, client *http.Client) Options {
				return Options{
					URL:         server.URL,
					HTTPClient:  server.Client(),
					Depth:       2,
					Concurrency: 3,
					Timeout:     time.Second * 15,
					Retries:     2,
				}
			}),
			createWant(func(URL string) AnalyzeOutput {
				return AnalyzeOutput{
					RootURL: URL,
					Depth:   2,
					Pages: []page.Page{
						{
							URL:         URL,
							Depth:       0,
							Error:       nil,
							HTTPStatus:  200,
							Status:      "200 OK",
							BrokenLinks: []page.BrokenLink{},
							SEO: page.SEO{
								HasTitle:       false,
								HasDescription: false,
								HasH1:          false,
							},
						},
					},
				}
			}),
		},
		{
			"проверяем что после двух неудачных попыток запроса в отчете сохраняется ошибка",
			func() (*httptest.Server, *http.Client) {
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/":
						http.Error(w, "временная ошибка сервера", http.StatusInternalServerError)
					default:
						http.NotFound(w, r)
					}
				}))

				return server, server.Client()
			},
			createOptions(func(server *httptest.Server, client *http.Client) Options {
				return Options{
					URL:         server.URL,
					HTTPClient:  server.Client(),
					Depth:       2,
					Concurrency: 3,
					Timeout:     time.Second * 15,
					Retries:     2,
				}
			}),
			createWant(func(URL string) AnalyzeOutput {
				return AnalyzeOutput{
					RootURL: URL,
					Depth:   2,
					Pages: []page.Page{
						{
							URL:         URL,
							Depth:       0,
							Error:       nil,
							HTTPStatus:  500,
							Status:      "500 Internal Server Error",
							BrokenLinks: []page.BrokenLink{},
							SEO: page.SEO{
								HasTitle:       false,
								HasDescription: false,
								HasH1:          false,
							},
						},
					},
				}
			}),
		},
		{
			"проверяем что данные об ассете успешно сохраняются в отчете (+размер высчитывается из тела ответа)",
			func() (*httptest.Server, *http.Client) {
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/":
						tmpl, err := template.ParseFiles("../mocks/index.html")

						if err != nil {
							panic("ошибка чтения html-файла")
						}

						err = tmpl.Execute(w, map[string]string{
							"BaseURL": r.Host,
						})

						if err != nil {
							panic("ошибка создания тела ответа")
						}
					case "/app.js":
						w.Header().Set("Content-Type", "text/javascript")
						w.WriteHeader(http.StatusOK)

						_, err := io.WriteString(w, `console.log("fake script");`)
						if err != nil {
							t.Errorf("write script response: %v", err)
						}
					default:
						http.NotFound(w, r)
					}
				}))

				return server, server.Client()
			},
			createOptions(func(server *httptest.Server, client *http.Client) Options {
				return Options{
					URL:         server.URL,
					HTTPClient:  server.Client(),
					Depth:       0,
					Concurrency: 3,
					Timeout:     time.Second * 15,
				}
			}),
			createWant(func(URL string) AnalyzeOutput {
				parsedURL, err := url.Parse(URL)

				if err != nil {
					panic("ошибка парсинга")
				}

				return AnalyzeOutput{
					RootURL: URL,
					Depth:   0,
					Pages: []page.Page{
						{
							URL:        URL,
							Depth:      0,
							Error:      nil,
							HTTPStatus: 200,
							Status:     "200 OK",
							BrokenLinks: []page.BrokenLink{
								{URL: URL + "/contacts", StatusCode: 404},
							},
							SEO: page.SEO{
								HasTitle: true,
								Title:    "Simple Test",
							},
							Assets: []page.Asset{
								{URL: "https://" + parsedURL.Host + "/app.js", Type: "script", StatusCode: 200, SizeBytes: 27, Error: nil},
							},
						},
					},
				}
			}),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {

			server, client := c.createHTTPClient()
			defer server.Close()

			got := Analyze(
				context.Background(),
				c.createOptions(server, client),
			)

			want := c.createWant(server.URL)
			AssertAnalyzeOutput(t, want, got)
		})
	}

}

type recordingHTTPClient struct {
	previousRequestTime time.Time
	t                   testing.TB
	requestsStartTime   []time.Time
}

func (c *recordingHTTPClient) Do(r *http.Request) (*http.Response, error) {
	c.requestsStartTime = append(c.requestsStartTime, time.Now())
	if c.previousRequestTime != (time.Time{}) {
		delay := time.Since(c.previousRequestTime)
		isDelayWasMade := delay >= time.Millisecond*250
		assert.Truef(c.t, isDelayWasMade, "задержка слишком мала = ", delay)
	}
	c.previousRequestTime = time.Now()

	var buf bytes.Buffer

	switch r.URL.Path {
	case "/":
		tmpl, _ := template.ParseFiles("../mocks/index.html")
		err := tmpl.Execute(&buf, map[string]string{
			"BaseURL": r.Host,
		})

		if err != nil {
			panic("ошибка чтения html-файла")
		}

	case "/contacts":
		data, _ := os.ReadFile("../mocks/contacts.html")
		buf.Write(data)

	case "/about":
		data, _ := os.ReadFile("../mocks/about.html")
		buf.Write(data)
	default:
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 OK",
			Body:       io.NopCloser(&buf),
			Request:    r,
		}, nil
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(&buf),
		Request:    r,
	}, nil
}

func TestCrawler_WithDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		HTTPClient := &recordingHTTPClient{t: t}
		windowStart := time.Now()
		windowEnd := windowStart.Add(time.Second)

		Analyze(
			context.Background(),
			Options{
				URL:         "https://test.url/",
				HTTPClient:  HTTPClient,
				Depth:       2,
				Concurrency: 5,
				Timeout:     time.Second * 15,
				Delay:       time.Millisecond * 250,
			},
		)

		requestPerSecond := 0

		for _, request := range HTTPClient.requestsStartTime {
			if request.Before(windowEnd) {
				requestPerSecond++
			}
		}

		assert.Truef(t, requestPerSecond <= 4, "превышено количество запросов в секунду")
	})
}

// * Проверяем что ручная отмена контекста останавливает повторные запросы
type contextCanceHTTPClient struct {
	t testing.TB
}

func (c *contextCanceHTTPClient) Do(r *http.Request) (*http.Response, error) {
	var buf bytes.Buffer

	switch r.URL.Path {
	case "/":
		select {
		case <-time.After(200 * time.Millisecond):
			break
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Body:       io.NopCloser(&buf),
			Request:    r,
		}, nil
	default:
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 OK",
			Body:       io.NopCloser(&buf),
			Request:    r,
		}, nil
	}
}

func TestCrawler_RetriesAndContextCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		go func() {
			select {
			case <-time.After(350 * time.Millisecond):
				cancel()
			case <-ctx.Done():
			}

		}()

		HTTPClient := &contextCanceHTTPClient{t: t}

		got := Analyze(
			ctx,
			Options{
				URL:         "https://test.url/",
				HTTPClient:  HTTPClient,
				Depth:       2,
				Concurrency: 5,
				Timeout:     time.Second * 15,
				Delay:       time.Millisecond * 250,
				Retries:     2,
			},
		)

		want := AnalyzeOutput{
			RootURL: "https://test.url/",
			Depth:   2,
			Pages:   []page.Page{},
		}
		AssertAnalyzeOutput(t, want, got)
	})
}

// * Проверяем что при дублирующихся ассетах на разных страницах повторный запрос не происходит
type assetHTTPClient struct {
	t                   testing.TB
	requestToAssetCount int
}

func (c *assetHTTPClient) Do(r *http.Request) (*http.Response, error) {
	var buf bytes.Buffer

	switch r.URL.Path {
	case "/":
		tmpl, _ := template.ParseFiles("../mocks/index.html")
		err := tmpl.Execute(&buf, map[string]string{
			"BaseURL": r.Host,
		})

		if err != nil {
			panic("ошибка чтения html-файла")
		}
	case "/contacts":
		tmpl, _ := template.ParseFiles("../mocks/contacts.html")
		err := tmpl.Execute(&buf, map[string]string{
			"BaseURL": r.Host,
		})

		if err != nil {
			panic("ошибка чтения html-файла")
		}
	case "/about":
		data, _ := os.ReadFile("../mocks/about.html")
		buf.Write(data)
	case "/app.js":
		c.requestToAssetCount++
		_, err := io.WriteString(&buf, `console.log("fake script");`)
		if err != nil {
			panic(err)
		}

		return &http.Response{
			Header:     http.Header{"Content-Type": []string{"text/javascript"}},
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(&buf),
			Request:    r,
		}, nil
	default:
		return &http.Response{
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			StatusCode: http.StatusNotFound,
			Status:     "404 OK",
			Body:       io.NopCloser(&buf),
			Request:    r,
		}, nil
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Status:     "200 OK",
		Body:       io.NopCloser(&buf),
		Request:    r,
	}, nil
}

func TestCrawler_Assets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		HTTPClient := &assetHTTPClient{t: t}

		Analyze(
			context.Background(),
			Options{
				URL:         "https://test.url/",
				HTTPClient:  HTTPClient,
				Depth:       2,
				Concurrency: 5,
				Timeout:     time.Second * 15,
			},
		)

		assert.Truef(t, HTTPClient.requestToAssetCount == 1, "broken link is missing")

	})
}
