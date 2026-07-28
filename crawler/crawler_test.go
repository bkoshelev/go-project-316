package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func AssertAnalyzeOutput(t *testing.T, want, got AnalyzeOutput) {
	assert.Equal(t, want.Depth, got.Depth)
	assert.Equal(t, want.RootURL, got.RootURL)
	assert.Equal(t, want.Pages, got.Pages)
}

func TestSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	got, err := Analyze(context.Background(), Options{
		URL:        server.URL,
		HTTPClient: server.Client(),
	})

	if err != nil {
		fmt.Println("что то пошло не так %w", err)
	}

	var gotJSON AnalyzeOutput
	err = json.Unmarshal(got, &gotJSON)
	if err != nil {
		panic("ошибка парсинга в JSON")
	}

	want := AnalyzeOutput{
		RootURL: server.URL,
		Depth:   0,
		Pages: []Page{
			{
				URL:        server.URL,
				Depth:      0,
				HTTPStatus: 200,
				Status:     "200 OK",
				Error:      "",
			},
		},
	}

	AssertAnalyzeOutput(t, want, gotJSON)
}

func TestError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	got, err := Analyze(context.Background(), Options{
		URL:        server.URL,
		HTTPClient: server.Client(),
	})

	if err != nil {
		fmt.Println("что то пошло не так %w", err)
	}

	var gotJSON AnalyzeOutput
	err = json.Unmarshal(got, &gotJSON)
	if err != nil {
		panic("ошибка парсинга в JSON")
	}

	want := AnalyzeOutput{
		RootURL: server.URL,
		Depth:   0,
		Pages: []Page{
			{
				URL:        server.URL,
				Depth:      0,
				HTTPStatus: 400,
				Status:     "400 Bad Request",
				Error:      "bad request\n",
			},
		},
	}

	AssertAnalyzeOutput(t, want, gotJSON)
}
