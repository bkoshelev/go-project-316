package crawler

import (
	httpclient "code/internal/http_client"
	"code/internal/link"
	"code/internal/page"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"sync"
	"time"
)

type Options struct {
	URL         string
	Depth       int
	Retries     int
	Delay       time.Duration
	Timeout     time.Duration
	UserAgent   string
	Concurrency int
	// IndentJSON,
	HTTPClient httpclient.HTTPClient
}

type Job struct {
	Type        string
	PageOptions page.PageOptions
	LinkOptions link.LinkOptions
}
type AnalyzeOutput struct {
	RootURL     string      `json:"root_url" binding:"required"`
	Depth       int         `json:"depth" binding:"required"`
	GeneratedAt string      `json:"generated_at" binding:"required"`
	Pages       []page.Page `json:"pages" binding:"required"`
}

type VisitedURLs struct {
	mu sync.RWMutex
	m  map[string]struct{}
}

func (v *VisitedURLs) Get(key string) (struct{}, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	val, ok := v.m[key]
	return val, ok
}

func (c *VisitedURLs) Add(value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[value] = struct{}{}
}

type Queue struct {
	Mu sync.RWMutex
	M  []Job
}

func (q *Queue) Shift() (Job, bool) {
	q.Mu.RLock()
	defer q.Mu.RUnlock()

	if len(q.M) > 0 {
		val := q.M[0]
		q.M = q.M[1:]

		return val, true
	}
	return Job{}, false
}

func (q *Queue) Add(value Job) {
	q.Mu.Lock()
	defer q.Mu.Unlock()

	q.M = append(q.M, value)
}

type LinksCache struct {
	PageURLs          []string
	LinkAnalyzeResult link.LinkAnalyzeResult
}

func finishAnalyze(opts Options, pages map[string]page.Page, cachedLinks map[string]LinksCache) AnalyzeOutput {

	for _, link := range cachedLinks {
		for _, pageURL := range link.PageURLs {

			currentPage, ok := pages[pageURL]

			if !ok {
				continue
			}

			switch {
			case link.LinkAnalyzeResult.IsBrokenLink:
				currentPage.BrokenLinks = append(
					currentPage.BrokenLinks, page.BrokenLink(link.LinkAnalyzeResult.LinkAnalyzeOutput),
				)
			case link.LinkAnalyzeResult.IsAsset:
				currentPage.Assets = append(
					currentPage.Assets, page.Asset(link.LinkAnalyzeResult.AssetAnalyzeOutput),
				)
			}

			pages[pageURL] = currentPage
		}
	}

	output := AnalyzeOutput{
		RootURL:     opts.URL,
		Depth:       opts.Depth,
		GeneratedAt: time.Now().Format(time.RFC3339),
		Pages:       slices.Collect(maps.Values(pages)),
	}

	return output
}

func worker(
	ctx context.Context,
	analyzePageJobs chan page.PageOptions,
	pageAnalyzeOutputs chan page.Page,
	analyzeLinksJobs chan link.LinkOptions,
	linkAnalyzeResults chan link.LinkAnalyzeResult,
	opts Options,
	wg *sync.WaitGroup,
	httpFetcher httpclient.HTTPFetch,
	visitedUrls *VisitedURLs,
	readyForNextJob chan struct{},
	analyzeJobsQueue *Queue,

) {
	for {
		select {
		case pageOpts := <-analyzePageJobs:
			slog.Info("Стартуем анализ страницы")
			func() {
				defer wg.Done()

				parsedPageURL, err := url.Parse(pageOpts.PageURL)
				if err != nil {
					select {
					case pageAnalyzeOutputs <- page.Page{
						URL:   pageOpts.PageURL,
						Depth: pageOpts.Depth,
						Error: err,
					}:
					case <-ctx.Done():
						return
					}
				}

				pageResult := page.AnalyzePage(ctx, pageOpts, httpFetcher)

				select {
				case pageAnalyzeOutputs <- pageResult.PageOutput:
				case <-ctx.Done():
					return
				}

				for _, linkOpts := range pageResult.Links {

					parsedLink, err := url.Parse(linkOpts.LinkURL)
					if err != nil {
						continue
					}

					if !link.ValidateLink(parsedLink) {
						continue
					}
					if !parsedLink.IsAbs() {
						parsedLink, err = link.NormalizeLink(linkOpts.LinkURL, linkOpts.PageURL)
						if err != nil {
							continue
						}
					}

					_, ok := visitedUrls.Get(parsedLink.String())
					if !ok {
						visitedUrls.Add(parsedLink.String())

						wg.Add(1)
						slog.Info("Ссылка добавлена в очередь работ")

						analyzeJobsQueue.Add(
							Job{
								Type:        "link",
								LinkOptions: link.LinkOptions{PageURL: parsedPageURL, LinkURL: parsedLink, Depth: linkOpts.Depth},
							},
						)
					} else {
						linkAnalyzeResults <- link.LinkAnalyzeResult{URL: parsedLink.String(), PageURL: linkOpts.PageURL}
					}

				}
				slog.Info("Анализ страницы завершен")
			}()

		case linkOpts := <-analyzeLinksJobs:
			slog.Info("Стартуем анализ ссылки")
			func() {
				defer wg.Done()

				linkResult := link.AnalyzeLink(ctx, linkOpts, httpFetcher)

				if linkResult.IsUnsupportedLink {
					return
				}

				if linkResult.IsAsset {
					linkAnalyzeResults <- linkResult
				}

				if linkResult.IsBrokenLink {
					slog.Info("Отчет анализа ссылки отправлен")
					linkAnalyzeResults <- linkResult
				}

				if linkResult.IsPage {
					if !linkResult.IsExternalHost &&
						linkResult.PageOptions.Depth <= opts.Depth {

						wg.Add(1)

						analyzeJobsQueue.Add(
							Job{
								Type:        "page",
								PageOptions: page.PageOptions(linkResult.PageOptions),
							},
						)
					}
				}

			}()
		case <-ctx.Done():
			return
		}
		select {
		case readyForNextJob <- struct{}{}:
		case <-ctx.Done():
			return
		}
	}
}

func Analyze(ctx context.Context, opts Options) AnalyzeOutput {
	parsedLink, err := url.Parse(opts.URL)
	if err != nil {
		return AnalyzeOutput{
			RootURL:     opts.URL,
			Depth:       opts.Depth,
			GeneratedAt: time.Now().Format(time.RFC3339),
			Pages: []page.Page{
				{URL: opts.URL, Depth: 0, Error: errors.New("invalid url")},
			},
		}
	}
	if !link.ValidateLink(parsedLink) {
		return AnalyzeOutput{
			RootURL:     opts.URL,
			Depth:       opts.Depth,
			GeneratedAt: time.Now().Format(time.RFC3339),
			Pages: []page.Page{
				{URL: opts.URL, Depth: 0, Error: errors.New("invalid url")},
			},
		}
	}

	ctx, cancel := context.WithCancel(ctx)

	visitedUrls := VisitedURLs{
		mu: sync.RWMutex{},
		m:  make(map[string]struct{}),
	}

	var wg sync.WaitGroup

	analyzePageJobs := make(chan page.PageOptions)
	analyzeLinksJobs := make(chan link.LinkOptions)

	pageAnalyzeOutputs := make(chan page.Page)
	linkAnalyzeResults := make(chan link.LinkAnalyzeResult)

	analyzeJobsQueue := Queue{
		Mu: sync.RWMutex{},
		M:  make([]Job, 0),
	}

	readyForNextJob := make(chan struct{})

	timerJob := make(chan struct{})
	delayFinished := make(chan struct{})

	HTTPFetch := httpclient.CreateHTTPFetch(httpclient.Options{
		UserAgent:    opts.UserAgent,
		HTTPClient:   opts.HTTPClient,
		WaitPauseCh:  delayFinished,
		StartPauseCh: timerJob,
		Timeout:      opts.Timeout,
		Retries:      opts.Retries,
	})

	for w := 0; w < opts.Concurrency; w++ {
		go worker(
			ctx,
			analyzePageJobs,
			pageAnalyzeOutputs,
			analyzeLinksJobs,
			linkAnalyzeResults,
			opts,
			&wg,
			HTTPFetch,
			&visitedUrls,
			readyForNextJob,
			&analyzeJobsQueue,
		)
	}

	go func() {
		for {
			select {
			case <-readyForNextJob:
				newJob, ok := analyzeJobsQueue.Shift()
				if ok {
					slog.Info("Новая задача на анализ страницы создана")
					if newJob.Type == "page" {
						analyzePageJobs <- newJob.PageOptions
					} else {
						analyzeLinksJobs <- newJob.LinkOptions
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		for {
			select {
			case <-timerJob:
				timer := time.NewTimer(opts.Delay)

				select {
				case <-timer.C:
					select {
					case delayFinished <- struct{}{}:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return

			}
		}
	}()

	// стартуем анализ
	func() {
		wg.Add(1)
		visitedUrls.Add(opts.URL)

		analyzeJobsQueue.Add(
			Job{
				Type: "page",
				PageOptions: page.PageOptions{
					PageURL: opts.URL,
					Depth:   0,
				},
			},
		)
		readyForNextJob <- struct{}{}
		delayFinished <- struct{}{}
	}()

	go func() {
		wg.Wait()
		cancel()
	}()

	pages := map[string]page.Page{}
	links := map[string]LinksCache{}

	for {
		select {
		case page := <-pageAnalyzeOutputs:
			pages[page.URL] = page
		case link := <-linkAnalyzeResults:
			cachedLink, ok := links[link.URL]
			if !ok {
				links[link.URL] = LinksCache{
					LinkAnalyzeResult: link,
					PageURLs:          []string{link.PageURL},
				}
			} else {
				cachedLink.PageURLs = append(cachedLink.PageURLs, link.PageURL)
				links[link.URL] = cachedLink
			}
		case <-ctx.Done():
			return finishAnalyze(opts, pages, links)
		}
	}
}

func (output AnalyzeOutput) Format() []byte {
	fmtOutput, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		panic("ошибка форматирования результата")
	}
	return fmtOutput
}
