package crawler

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func AssertBrokenLinks(t testing.TB, want, got AnalyzeOutput) {
	t.Helper()

	for i := range want.Pages[0].BrokenLinks {
		assert.Equal(t, want.Pages[0].BrokenLinks[i].Url, got.Pages[0].BrokenLinks[i].Url)
		assert.Equal(t, want.Pages[0].BrokenLinks[i].StatusCode, got.Pages[0].BrokenLinks[i].StatusCode)
	}
}

func AssertAnalyzeOutput(t testing.TB, want, got AnalyzeOutput) {
	t.Helper()
	assert.Equal(t, want.Depth, got.Depth)
	assert.Equal(t, want.RootURL, got.RootURL)
	assert.Equal(t, want.Pages[0].HTTPStatus, got.Pages[0].HTTPStatus)
	assert.Equal(t, want.Pages[0].URL, got.Pages[0].URL)
	assert.Equal(t, want.Pages[0].Status, got.Pages[0].Status)

}

type createHTTPClient func() (*httptest.Server, *http.Client)
type createWant func(URL string) AnalyzeOutput
type assertOutput func(t testing.TB, want, got AnalyzeOutput)

func TestCrawler_Subtests(t *testing.T) {
	cases := []struct {
		name             string
		createHTTPClient createHTTPClient
		createWant       createWant
		assertOutput     assertOutput
	}{
		{
			"успешный ответ",
			func() (*httptest.Server, *http.Client) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				return server, server.Client()
			},
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
			func(t testing.TB, want, got AnalyzeOutput) {
				t.Helper()
				AssertAnalyzeOutput(t, want, got)
				assert.ErrorIs(t, got.Pages[0].Error, want.Pages[0].Error)
			},
		},
		{
			"вернулся код ошибки",
			func() (*httptest.Server, *http.Client) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "bad request", http.StatusBadRequest)
				}))
				return server, server.Client()
			},
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
			func(t testing.TB, want, got AnalyzeOutput) {
				t.Helper()
				AssertAnalyzeOutput(t, want, got)
				assert.ErrorIs(t, got.Pages[0].Error, want.Pages[0].Error)
			},
		},
		{
			"истекло время ожидания ответа",
			func() (*httptest.Server, *http.Client) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					time.Sleep(200 * time.Millisecond)
				}))
				client := server.Client()
				client.Timeout = 100 * time.Millisecond
				return server, client
			},
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
			func(t testing.TB, want, got AnalyzeOutput) {
				t.Helper()
				AssertAnalyzeOutput(t, want, got)
				assert.ErrorIs(t, got.Pages[0].Error, want.Pages[0].Error)
			},
		},
		{
			"сетевая ошибка",
			func() (*httptest.Server, *http.Client) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				server.Close()

				return server, server.Client()
			},
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
			func(t testing.TB, want, got AnalyzeOutput) {
				t.Helper()
				AssertAnalyzeOutput(t, want, got)
				assert.ErrorIs(t, got.Pages[0].Error, want.Pages[0].Error)
			},
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
						},
					},
				}
			}),
			func(t testing.TB, want, got AnalyzeOutput) {
				t.Helper()
				AssertAnalyzeOutput(t, want, got)
				AssertBrokenLinks(t, want, got)
				assert.ErrorIs(t, got.Pages[0].Error, want.Pages[0].Error)
				assert.ErrorContains(t, got.Pages[0].BrokenLinks[1].Error, want.Pages[0].BrokenLinks[1].Error.Error())
			},
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
			func(t testing.TB, want, got AnalyzeOutput) {
				t.Helper()
				AssertAnalyzeOutput(t, want, got)
				assert.ErrorIs(t, got.Pages[0].Error, want.Pages[0].Error)
				assert.Equal(t, want.Pages[0].Seo, got.Pages[0].Seo)
			},
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
			func(t testing.TB, want, got AnalyzeOutput) {
				t.Helper()
				AssertAnalyzeOutput(t, want, got)
				assert.ErrorIs(t, got.Pages[0].Error, want.Pages[0].Error)
				assert.Equal(t, want.Pages[0].Seo, got.Pages[0].Seo)
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {

			server, client := c.createHTTPClient()
			defer server.Close()

			got, err := AnalyzeURL(context.Background(), Options{
				URL:        server.URL,
				HTTPClient: client,
			})
			if err != nil {
				fmt.Println(err)
				panic("неверные входные данные")
			}

			want := c.createWant(server.URL)
			c.assertOutput(t, want, got)
		})
	}

}
