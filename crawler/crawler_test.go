package crawler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"syscall"
	"testing"
	"text/template"
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

func AssertBrokenLinks(t testing.TB, want, got []BrokenLink) {
	t.Helper()

	gotByURL := make(map[string]BrokenLink, len(got))
	for _, link := range got {
		gotByURL[link.Url] = link
	}

	for _, wantLink := range want {
		gotLink, exists := gotByURL[wantLink.Url]
		assert.Truef(t, exists, "broken link is missing: %s", wantLink.Url)
		assert.Equal(t, wantLink.StatusCode, gotLink.StatusCode)
		assertError(t, wantLink.Error, gotLink.Error)
	}
}

func AssertAnalyzeOutput(t testing.TB, want, got AnalyzeOutput) {
	t.Helper()
	assert.Equal(t, want.Depth, got.Depth)
	assert.Equal(t, want.RootURL, got.RootURL)

	gotByURL := make(map[string]Page, len(got.Pages))
	for _, page := range got.Pages {
		gotByURL[page.URL] = page
	}

	for _, wantPage := range want.Pages {
		gotPage, exists := gotByURL[wantPage.URL]
		assert.Truef(t, exists, "broken link is missing: %s", wantPage.URL)

		assert.Equal(t, wantPage.Depth, gotPage.Depth)
		assert.Equal(t, wantPage.HTTPStatus, gotPage.HTTPStatus)
		assert.Equal(t, wantPage.Status, gotPage.Status)
		assert.Equal(t, wantPage.Seo, gotPage.Seo)
		assertError(t, wantPage.Error, gotPage.Error)
		AssertBrokenLinks(t, wantPage.BrokenLinks, gotPage.BrokenLinks)
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
				}
			}),
			func(URL string) AnalyzeOutput {
				return AnalyzeOutput{
					RootURL: URL,
					Depth:   0,
					Pages: []Page{
						{
							URL:         URL,
							Depth:       0,
							HTTPStatus:  200,
							Status:      "200 OK",
							Error:       nil,
							BrokenLinks: []BrokenLink{},
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
				}
			}),
			createWant(func(URL string) AnalyzeOutput {
				return AnalyzeOutput{
					RootURL: URL,
					Depth:   0,
					Pages: []Page{
						{
							URL:         URL,
							Depth:       0,
							HTTPStatus:  400,
							Status:      "400 Bad Request",
							BrokenLinks: []BrokenLink{},
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
				client.Timeout = 100 * time.Millisecond
				return server, client
			},
			createOptions(func(server *httptest.Server, client *http.Client) Options {
				return Options{
					URL:         server.URL,
					HTTPClient:  client,
					Depth:       0,
					Concurrency: 3,
				}
			}),
			createWant(func(URL string) AnalyzeOutput {
				return AnalyzeOutput{
					RootURL: URL,
					Depth:   0,
					Pages: []Page{
						{
							URL:         URL,
							Depth:       0,
							Error:       context.DeadlineExceeded,
							BrokenLinks: []BrokenLink{},
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
				}
			}),
			createWant(func(URL string) AnalyzeOutput {

				return AnalyzeOutput{
					RootURL: URL,
					Depth:   0,
					Pages: []Page{
						{
							URL:         URL,
							Depth:       0,
							Error:       syscall.ECONNREFUSED,
							BrokenLinks: []BrokenLink{},
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
					Pages: []Page{
						{
							URL:        URL,
							Depth:      0,
							Error:      nil,
							HTTPStatus: 200,
							Status:     "200 OK",
							BrokenLinks: []BrokenLink{
								{Url: URL + "/contacts", StatusCode: 404},
								{Url: "https://cdn." + parsedURL.Host + "/app.js", Error: errors.New("no such host")},
							},
							Seo: Seo{
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
				}
			}),
			createWant(func(URL string) AnalyzeOutput {

				return AnalyzeOutput{
					RootURL: URL,
					Depth:   0,
					Pages: []Page{
						{
							URL:        URL,
							Depth:      0,
							Error:      nil,
							HTTPStatus: 200,
							Status:     "200 OK",
							BrokenLinks: []BrokenLink{
								{Url: URL + "/about", StatusCode: 404},
							},
							Seo: Seo{
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
				}
			}),
			createWant(func(URL string) AnalyzeOutput {

				return AnalyzeOutput{
					RootURL: URL,
					Depth:   0,
					Pages: []Page{
						{
							URL:         URL,
							Depth:       0,
							Error:       nil,
							HTTPStatus:  200,
							Status:      "200 OK",
							BrokenLinks: []BrokenLink{},
							Seo: Seo{
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
			"проверка обхода в глубину",
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
					Pages: []Page{
						{
							URL:        URL,
							Depth:      0,
							Error:      nil,
							HTTPStatus: 200,
							Status:     "200 OK",
							BrokenLinks: []BrokenLink{
								{Url: "https://cdn." + parsedURL.Host + "/app.js", Error: errors.New("no such host")},
							},
							Seo: Seo{
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
							BrokenLinks: []BrokenLink{},
							Seo: Seo{
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
							BrokenLinks: []BrokenLink{},
							Seo: Seo{
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
