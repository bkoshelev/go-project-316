package crawler

import (
	"cmp"
	"code/internal/fetcher"
	"code/internal/link"
	"code/internal/page"
	"context"
	"encoding/json"
	"errors"
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
	HTTPClient  fetcher.HTTPClient
}

type Job struct {
	Type        string
	URL         *url.URL
	PageOptions page.PageOptions
	LinkOptions link.LinkOptions
}
type Report struct {
	RootURL     string      `json:"root_url" binding:"required"`
	Depth       int         `json:"depth" binding:"required"`
	GeneratedAt string      `json:"generated_at" binding:"required"`
	Pages       []page.Page `json:"pages" binding:"required"`
}

type Queue struct {
	Mu           sync.RWMutex
	ReadyToWork  []Job
	InWork       int
	VisitedLinks []string
	Crawler      *Crawler
}

func (q *Queue) Shift() (Job, bool) {
	q.Mu.Lock()
	defer q.Mu.Unlock()

	if len(q.ReadyToWork) > 0 {
		val := q.ReadyToWork[0]
		q.ReadyToWork = q.ReadyToWork[1:]

		q.InWork++
		return val, true
	}

	return Job{}, false
}

func (q *Queue) Finish() {
	q.Mu.Lock()
	defer q.Mu.Unlock()

	q.InWork--

	select {
	case q.Crawler.Channels.jobFinished <- struct{}{}:
	default:
	}
}

func (q *Queue) IsEmpty() bool {
	q.Mu.Lock()
	defer q.Mu.Unlock()

	return len(q.ReadyToWork) == 0 && q.InWork == 0
}

func (q *Queue) Add(value Job) {
	q.Mu.Lock()
	defer q.Mu.Unlock()

	q.ReadyToWork = append(q.ReadyToWork, value)
	q.VisitedLinks = append(q.VisitedLinks, value.URL.String())

	select {
	case q.Crawler.Channels.newJobReadyForAnalyze <- struct{}{}:
	default:
	}
}

func (c *Queue) linkAlreadyInJobQueue(key string) bool {
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	return slices.Contains(c.VisitedLinks, key)
}

type LinksCache struct {
	LinkAnalyzeResults link.LinkAnalyzeResult
}

type Cache struct {
	Mu           sync.RWMutex
	linksInPages map[string][]string
	linksData    map[string]link.LinkAnalyzeResult
	pages        map[string]page.Page
}

func (c *Cache) AddLink(newLink link.LinkAnalyzeResult) {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	c.linksData[newLink.URL.String()] = newLink

}

func (c *Cache) AddLinkToPage(linkURL string, pageURL string) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	pageLinks, ok := c.linksInPages[pageURL]
	if !ok {
		c.linksInPages[pageURL] = []string{linkURL}
	} else {
		pageLinks = append(pageLinks, linkURL)
		c.linksInPages[pageURL] = pageLinks
	}
}

func (c *Cache) AddPage(page page.Page) {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	c.pages[page.URL] = page
}

type Channels struct {
	delayFinished         chan struct{}
	timerJob              chan struct{}
	analyzeJobs           chan Job
	newJobReadyForAnalyze chan struct{}
	jobFinished           chan struct{}
}

type Crawler struct {
	URL              *url.URL
	Depth            int
	Retries          int
	Delay            time.Duration
	Timeout          time.Duration
	UserAgent        string
	Concurrency      int
	IndentJSON       bool
	HTTPClient       fetcher.HTTPClient
	HTTPFetch        fetcher.HTTPFetch
	Channels         Channels
	AnalyzeJobsQueue *Queue
	ctx              context.Context
	cancel           context.CancelFunc
	cache            Cache
}

func (c *Crawler) createOutput() Report {
	for _, pageData := range c.cache.pages {
		for _, linkURL := range c.cache.linksInPages[pageData.URL] {

			linkData, ok := c.cache.linksData[linkURL]

			if !ok {
				continue
			}

			switch {
			case linkData.IsBrokenLink:
				pageData.BrokenLinks = append(
					pageData.BrokenLinks, page.BrokenLink(linkData.LinkAnalyzeOutput),
				)
			case linkData.IsAsset:
				pageData.Assets = append(
					pageData.Assets, page.Asset(linkData.AssetAnalyzeOutput),
				)
			}

			c.cache.pages[pageData.URL] = pageData
		}

		slices.SortFunc(pageData.Assets, func(asset1, asset2 page.Asset) int {
			return cmp.Compare(asset1.Type, asset2.Type)
		})
		slices.SortFunc(pageData.BrokenLinks, func(brokenLink1, brokenLink2 page.BrokenLink) int {
			return cmp.Compare(brokenLink1.URL, brokenLink2.URL)
		})
	}

	pagesSlice := slices.Collect(maps.Values(c.cache.pages))

	slices.SortFunc(pagesSlice, func(page1, page2 page.Page) int {
		if result := cmp.Compare(page1.Depth, page2.Depth); result != 0 {
			return result
		}
		return cmp.Compare(page1.URL, page2.URL)
	})

	output := Report{
		RootURL:     c.URL.String(),
		Depth:       c.Depth,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Pages:       pagesSlice,
	}

	return output
}

func worker(
	c *Crawler,
) {
	for {
		select {
		case newJob := <-c.Channels.analyzeJobs:
			switch newJob.Type {
			case "page":
				pageOpts := newJob.PageOptions

				func() {
					defer func() {
						c.AnalyzeJobsQueue.Finish()
					}()

					pageResult := page.AnalyzePage(c.ctx, pageOpts, c.HTTPFetch)
					if pageResult.IsUnsupportedPage {
						return
					}
					c.cache.AddPage(pageResult.PageOutput)

					for _, linkOpts := range pageResult.Links {
						parsedLink, err := url.Parse(linkOpts.LinkURL)
						if err != nil {
							continue
						}

						if !link.ValidateLink(parsedLink) {
							continue
						}

						if !parsedLink.IsAbs() {
							parsedLink, err = link.NormalizeLink(linkOpts.LinkURL, linkOpts.PageURL.String())
							if err != nil {
								continue
							}
						}

						c.cache.AddLinkToPage(parsedLink.String(), linkOpts.PageURL.String())
						if !c.AnalyzeJobsQueue.linkAlreadyInJobQueue(parsedLink.String()) {

							c.AnalyzeJobsQueue.Add(
								Job{
									Type:        "link",
									URL:         parsedLink,
									LinkOptions: link.LinkOptions{PageURL: pageOpts.PageURL, LinkURL: parsedLink, Depth: linkOpts.Depth, Type: linkOpts.Type},
								},
							)
						}
					}
				}()
			case "link":
				linkOpts := newJob.LinkOptions

				func() {
					defer func() {
						c.AnalyzeJobsQueue.Finish()
					}()

					linkResult := link.AnalyzeLink(c.ctx, linkOpts, c.HTTPFetch)

					if linkResult.IsAsset || linkResult.IsBrokenLink {
						c.cache.AddLink(linkResult)
						return
					}

					if linkResult.IsPage {
						if !linkResult.IsExternalHost &&
							linkResult.PageOptions.Depth <= c.Depth {
							c.AnalyzeJobsQueue.Add(
								Job{
									Type:        "page",
									URL:         linkResult.URL,
									PageOptions: page.PageOptions(linkResult.PageOptions),
								},
							)
						}
					}

				}()
			}
		case <-c.ctx.Done():
			return
		}
	}
}

func createNewCrawler(ctx context.Context, opts Options) (*Crawler, Report, bool) {

	parsedLink, err := url.Parse(opts.URL)
	if err != nil {
		return &Crawler{}, Report{
			RootURL:     opts.URL,
			Depth:       opts.Depth,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Pages: []page.Page{
				{URL: opts.URL, Depth: 0, CustomError: errors.New("invalid url")},
			},
		}, false
	}
	ctx, cancel := context.WithCancel(ctx)

	crawler := Crawler{
		URL:        parsedLink,
		Retries:    opts.Retries,
		Delay:      opts.Delay,
		UserAgent:  opts.UserAgent,
		IndentJSON: opts.IndentJSON,
		ctx:        ctx,
		cancel:     cancel,
	}

	crawler.AnalyzeJobsQueue = &Queue{
		ReadyToWork:  make([]Job, 0),
		InWork:       0,
		Crawler:      &crawler,
		VisitedLinks: []string{},
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
		timerJob:              make(chan struct{}),
		delayFinished:         make(chan struct{}),
		analyzeJobs:           make(chan Job, crawler.Concurrency),
		newJobReadyForAnalyze: make(chan struct{}, 1),
		jobFinished:           make(chan struct{}, 1),
	}

	crawler.cache = Cache{
		linksData:    map[string]link.LinkAnalyzeResult{},
		pages:        map[string]page.Page{},
		linksInPages: map[string][]string{},
	}

	crawler.HTTPFetch = fetcher.CreateHTTPFetch(fetcher.Options{
		UserAgent:    crawler.UserAgent,
		HTTPClient:   crawler.HTTPClient,
		WaitPauseCh:  crawler.Channels.delayFinished,
		StartPauseCh: crawler.Channels.timerJob,
		Timeout:      crawler.Timeout,
		Retries:      crawler.Retries,
	})
	return &crawler, Report{}, true
}

func (c *Crawler) validateOptions() (Report, bool) {

	if !link.ValidateLink(c.URL) {
		return Report{
			RootURL:     c.URL.String(),
			Depth:       c.Depth,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Pages: []page.Page{
				{URL: c.URL.String(), Depth: 0, CustomError: errors.New("invalid url")},
			},
		}, false
	}

	normalizedLink, err := link.NormalizeLink(c.URL.String(), c.URL.String())
	if err != nil {
		return Report{
			RootURL:     c.URL.String(),
			Depth:       c.Depth,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Pages: []page.Page{
				{URL: c.URL.String(), Depth: 0, CustomError: errors.New("invalid url")},
			},
		}, false
	}
	c.URL = normalizedLink

	return Report{}, true
}

func (c *Crawler) prepareAnalyze() {
	go func() {

		for {
			select {
			case <-c.Channels.newJobReadyForAnalyze:
			case <-c.Channels.jobFinished:
				if c.AnalyzeJobsQueue.IsEmpty() {
					c.cancel()
				}
			case <-c.ctx.Done():
				return
			}

			for {
				newJob, ok := c.AnalyzeJobsQueue.Shift()
				if !ok {
					break
				}

				select {
				case <-c.ctx.Done():
					return
				case c.Channels.analyzeJobs <- newJob:
					continue
				}
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

	c.AnalyzeJobsQueue.Add(
		Job{
			Type: "page",
			URL:  c.URL,
			PageOptions: page.PageOptions{
				PageURL: c.URL,
				Depth:   0,
			},
		},
	)

	select {
	case c.Channels.delayFinished <- struct{}{}:
	case <-c.ctx.Done():
		return
	}
}

func (output Report) Format(indentJSON bool) ([]byte, error) {
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

func createReport(ctx context.Context, opts Options) Report {
	crawler, output, isValid := createNewCrawler(ctx, opts)

	if !isValid {
		return output
	}

	output, isValid = crawler.validateOptions()

	if !isValid {
		return output
	}

	crawler.prepareAnalyze()
	crawler.createWorkers()
	crawler.startAnalyze()

	<-crawler.ctx.Done()
	return crawler.createOutput()
}

func Analyze(ctx context.Context, opts Options) ([]byte, error) {
	report := createReport(ctx, opts)
	return report.Format(opts.IndentJSON)
}
