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
	"net/http"
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
	IndentJSON  bool
	HTTPClient  httpclient.HTTPClient
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

func (c Cache) AddLink(link link.LinkAnalyzeResult) {
	cachedLink, ok := c.links[link.URL]
	if !ok {
		c.links[link.URL] = LinksCache{
			LinkAnalyzeResult: link,
			PageURLs:          []string{link.PageURL},
		}
	} else {
		cachedLink.PageURLs = append(cachedLink.PageURLs, link.PageURL)
		c.links[link.URL] = cachedLink
	}
}

func (c Cache) AddPage(page page.Page) {
	c.pages[page.URL] = page
}

type Cache struct {
	links map[string]LinksCache
	pages map[string]page.Page
}

type Channels struct {
	readyForNextJob    chan struct{}
	delayFinished      chan struct{}
	timerJob           chan struct{}
	analyzePageJobs    chan page.PageOptions
	analyzeLinksJobs   chan link.LinkOptions
	pageAnalyzeOutputs chan page.Page
	linkAnalyzeResults chan link.LinkAnalyzeResult
}

type Crawler struct {
	URL              string
	Depth            int
	Retries          int
	Delay            time.Duration
	Timeout          time.Duration
	UserAgent        string
	Concurrency      int
	IndentJSON       bool
	HTTPClient       httpclient.HTTPClient
	VisitedUrls      VisitedURLs
	HTTPFetch        httpclient.HTTPFetch
	Channels         Channels
	analyzeJobsQueue *Queue
	ctx              context.Context
	cancel           context.CancelFunc
	wg               *sync.WaitGroup
	cache            Cache
}

func (c *Crawler) createOutput() AnalyzeOutput {

	for _, link := range c.cache.links {
		for _, pageURL := range link.PageURLs {

			currentPage, ok := c.cache.pages[pageURL]

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

			c.cache.pages[pageURL] = currentPage
		}
	}

	output := AnalyzeOutput{
		RootURL:     c.URL,
		Depth:       c.Depth,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Pages:       slices.Collect(maps.Values(c.cache.pages)),
	}

	return output
}

func worker(
	c *Crawler,
) {
	for {
		select {
		case pageOpts := <-c.Channels.analyzePageJobs:
			slog.Info("Стартуем анализ страницы")
			func() {
				defer c.wg.Done()

				parsedPageURL, err := url.Parse(pageOpts.PageURL)
				if err != nil {
					select {
					case c.Channels.pageAnalyzeOutputs <- page.Page{
						URL:         pageOpts.PageURL,
						Depth:       pageOpts.Depth,
						CustomError: err,
					}:
					case <-c.ctx.Done():
						return
					}
				}

				pageResult := page.AnalyzePage(c.ctx, pageOpts, c.HTTPFetch)

				select {
				case c.Channels.pageAnalyzeOutputs <- pageResult.PageOutput:
				case <-c.ctx.Done():
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

					_, ok := c.VisitedUrls.Get(parsedLink.String())
					if !ok {
						c.VisitedUrls.Add(parsedLink.String())

						c.wg.Add(1)
						slog.Info("Ссылка добавлена в очередь работ")

						c.analyzeJobsQueue.Add(
							Job{
								Type:        "link",
								LinkOptions: link.LinkOptions{PageURL: parsedPageURL, LinkURL: parsedLink, Depth: linkOpts.Depth},
							},
						)
					} else {
						c.Channels.linkAnalyzeResults <- link.LinkAnalyzeResult{URL: parsedLink.String(), PageURL: linkOpts.PageURL}
					}

				}
				slog.Info("Анализ страницы завершен")
			}()

		case linkOpts := <-c.Channels.analyzeLinksJobs:
			slog.Info("Стартуем анализ ссылки")
			func() {
				defer c.wg.Done()

				linkResult := link.AnalyzeLink(c.ctx, linkOpts, c.HTTPFetch)

				if linkResult.IsUnsupportedLink {
					return
				}

				if linkResult.IsAsset {
					c.Channels.linkAnalyzeResults <- linkResult
				}

				if linkResult.IsBrokenLink {
					slog.Info("Отчет анализа ссылки отправлен")
					c.Channels.linkAnalyzeResults <- linkResult
				}

				if linkResult.IsPage {
					if !linkResult.IsExternalHost &&
						linkResult.PageOptions.Depth <= c.Depth {
						c.wg.Add(1)

						c.analyzeJobsQueue.Add(
							Job{
								Type:        "page",
								PageOptions: page.PageOptions(linkResult.PageOptions),
							},
						)
					}
				}

			}()
		case <-c.ctx.Done():
			return
		}
		select {
		case c.Channels.readyForNextJob <- struct{}{}:
		case <-c.ctx.Done():
			return
		}
	}
}

func createNewCrawler(ctx context.Context, opts Options) *Crawler {
	ctx, cancel := context.WithCancel(ctx)

	crawler := Crawler{
		URL:        opts.URL,
		Retries:    opts.Retries,
		Delay:      opts.Delay,
		UserAgent:  opts.UserAgent,
		IndentJSON: opts.IndentJSON,
		VisitedUrls: VisitedURLs{
			mu: sync.RWMutex{},
			m:  make(map[string]struct{}),
		},
		ctx:    ctx,
		cancel: cancel,
		wg:     &sync.WaitGroup{},
		analyzeJobsQueue: &Queue{
			Mu: sync.RWMutex{},
			M:  make([]Job, 0),
		},
	}

	if opts.Depth < 0 {
		crawler.Depth = 1
	} else {
		crawler.Depth = opts.Depth
	}

	if opts.Timeout == 0 {
		crawler.Timeout = time.Second * 15
	} else {
		crawler.Timeout = opts.Timeout
	}

	if opts.Concurrency == 0 {
		crawler.Concurrency = 1
	} else {
		crawler.Concurrency = opts.Concurrency
	}

	if opts.HTTPClient == nil {
		crawler.HTTPClient = &http.Client{}
	} else {
		crawler.HTTPClient = opts.HTTPClient
	}

	crawler.Channels = Channels{
		readyForNextJob:    make(chan struct{}),
		timerJob:           make(chan struct{}),
		delayFinished:      make(chan struct{}),
		analyzePageJobs:    make(chan page.PageOptions),
		analyzeLinksJobs:   make(chan link.LinkOptions),
		pageAnalyzeOutputs: make(chan page.Page),
		linkAnalyzeResults: make(chan link.LinkAnalyzeResult),
	}

	crawler.cache = Cache{
		links: map[string]LinksCache{},
		pages: map[string]page.Page{},
	}

	crawler.HTTPFetch = httpclient.CreateHTTPFetch(httpclient.Options{
		UserAgent:    crawler.UserAgent,
		HTTPClient:   crawler.HTTPClient,
		WaitPauseCh:  crawler.Channels.delayFinished,
		StartPauseCh: crawler.Channels.timerJob,
		Timeout:      crawler.Timeout,
		Retries:      crawler.Retries,
	})
	return &crawler
}

func (c *Crawler) validateOptions() (AnalyzeOutput, bool) {
	parsedLink, err := url.Parse(c.URL)
	if err != nil {
		return AnalyzeOutput{
			RootURL:     c.URL,
			Depth:       c.Depth,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Pages: []page.Page{
				{URL: c.URL, Depth: 0, CustomError: errors.New("invalid url")},
			},
		}, false
	}
	if !link.ValidateLink(parsedLink) {
		return AnalyzeOutput{
			RootURL:     c.URL,
			Depth:       c.Depth,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Pages: []page.Page{
				{URL: c.URL, Depth: 0, CustomError: errors.New("invalid url")},
			},
		}, false
	}
	return AnalyzeOutput{}, true
}

func (c *Crawler) prepareAnalyze() {
	go func() {
		for {
			select {
			case <-c.Channels.readyForNextJob:
				newJob, ok := c.analyzeJobsQueue.Shift()
				if ok {
					slog.Info("Новая задача на анализ страницы создана")
					switch newJob.Type {
					case "page":
						c.Channels.analyzePageJobs <- newJob.PageOptions
					case "link":
						c.Channels.analyzeLinksJobs <- newJob.LinkOptions
					}
				}
			case <-c.ctx.Done():
				return
			}
		}
	}()

	go func() {
		for {
			select {
			case <-c.Channels.timerJob:
				timer := time.NewTimer(c.Delay)

				select {
				case <-timer.C:
					select {
					case c.Channels.delayFinished <- struct{}{}:
					case <-c.ctx.Done():
						return
					}
				case <-c.ctx.Done():
					return
				}
			case <-c.ctx.Done():
				return
			}
		}
	}()

}

func (c *Crawler) createWorkers() {
	for w := 0; w < c.Concurrency; w++ {
		go worker(
			c,
		)
	}
}

func (c *Crawler) startAnalyze() {
	// стартуем анализ
	c.wg.Add(1)
	c.VisitedUrls.Add(c.URL)

	c.analyzeJobsQueue.Add(
		Job{
			Type: "page",
			PageOptions: page.PageOptions{
				PageURL: c.URL,
				Depth:   0,
			},
		},
	)
	c.Channels.readyForNextJob <- struct{}{}
	c.Channels.delayFinished <- struct{}{}

	go func() {
		c.wg.Wait()
		c.cancel()
	}()

}

func (output AnalyzeOutput) Format(indentJSON bool) ([]byte, error) {
	var fmtOutput []byte
	var err error

	if indentJSON {
		fmtOutput, err = json.MarshalIndent(output, "", "  ")
	} else {
		fmtOutput, err = json.Marshal(output)
	}

	if err != nil {
		return nil, err
	}
	return fmtOutput, nil
}

func AnalyzeToJSONOutput(ctx context.Context, opts Options) AnalyzeOutput {
	crawler := createNewCrawler(ctx, opts)
	output, isValid := crawler.validateOptions()

	if !isValid {
		return output
	}

	crawler.prepareAnalyze()
	crawler.createWorkers()
	crawler.startAnalyze()

	for {
		select {
		case page := <-crawler.Channels.pageAnalyzeOutputs:
			crawler.cache.AddPage(page)
		case link := <-crawler.Channels.linkAnalyzeResults:
			crawler.cache.AddLink(link)
		case <-crawler.ctx.Done():
			return crawler.createOutput()
		}
	}
}

func Analyze(ctx context.Context, opts Options) ([]byte, error) {
	outputJSON := AnalyzeToJSONOutput(ctx, opts)
	return outputJSON.Format(opts.IndentJSON)
}
