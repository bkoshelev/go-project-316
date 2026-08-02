package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func AssertAnalyzeOutput(t *testing.T, want, got AnalyzeOutput) {
	assert.Equal(t, want.Depth, got.Depth)
	assert.Equal(t, want.RootURL, got.RootURL)
	assert.Equal(t, want.Pages[0].HTTPStatus, got.Pages[0].HTTPStatus)
	assert.Equal(t, want.Pages[0].URL, got.Pages[0].URL)
	assert.Equal(t, want.Pages[0].Status, got.Pages[0].Status)
	assert.ErrorIs(t, got.Pages[0].Error, want.Pages[0].Error)

}

type createHTTPClient func() (*httptest.Server, *http.Client)
type createWant func(URL string) AnalyzeOutput

func TestCrawler_Subtests(t *testing.T) {
	cases := []struct {
		name             string
		createHTTPClient createHTTPClient
		createWant       createWant
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
							URL:        URL,
							Depth:      0,
							HTTPStatus: 200,
							Status:     "200 OK",
							Error:      nil,
						},
					},
				}
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
							URL:        URL,
							Depth:      0,
							HTTPStatus: 400,
							Status:     "400 Bad Request",
						},
					},
				}
			}),
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
							URL:   URL,
							Depth: 0,
							Error: context.DeadlineExceeded,
						},
					},
				}
			}),
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
							URL:   URL,
							Depth: 0,
							Error: syscall.ECONNREFUSED,
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

			got, err := AnalyzeURL(context.Background(), Options{
				URL:        server.URL,
				HTTPClient: client,
			})
			if err != nil {
				fmt.Println(err)
				panic("неверные входные данные")
			}

			want := c.createWant(server.URL)
			AssertAnalyzeOutput(t, want, got)
		})
	}

}
